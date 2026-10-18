package handler

import (
	"testing"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

// H-17: buildFlowUploadBatch 必须丢弃负流量值。
// /flow/upload 仅凭节点 secret 鉴权；伪造 {"d":-N} 曾可经
// gorm.Expr("in_flow + ?", 负数) 直接扣减计数器，绕过配额。
func TestBuildFlowUploadBatchDropsNegativeValues(t *testing.T) {
	h := &Handler{}
	metas := map[int64]repo.FlowUploadForwardMeta{
		70: {ForwardID: 70, UserID: 2, UserTunnelID: 1, TunnelID: 9, TrafficRatio: 1, TunnelFlow: 1},
	}
	items := []flowItem{
		{N: "70_2_1_tcp", U: 100, D: 200},    // 合法上报
		{N: "70_2_1_tcp", U: 0, D: -1000000}, // 伪造负下行
		{N: "70_2_1_tcp", U: -50, D: 0},      // 伪造负上行
	}
	batch := h.buildFlowUploadBatch(items, metas)
	if len(batch.flowDeltas) != 1 {
		t.Fatalf("expected 1 flow delta, got %d", len(batch.flowDeltas))
	}
	d := batch.flowDeltas[0]
	if d.InFlow != 200 || d.OutFlow != 100 {
		t.Fatalf("negative values leaked into deltas: %+v", d)
	}
	if got := batch.quotaUsage[2]; got != 300 {
		t.Fatalf("negative values leaked into quota usage: %d", got)
	}
}

// M-04: 登录限流器——突发超过上限后拒绝，窗口过期后恢复，不影响其他 IP。
func TestLoginRateLimiterBlocksAfterBurst(t *testing.T) {
	limiter := &loginRateLimiter{buckets: make(map[string]*loginAttemptBucket)}
	now := time.Now()
	for i := 0; i < loginRateLimitMaxAttempts; i++ {
		if !limiter.allow("1.2.3.4", now) {
			t.Fatalf("attempt %d unexpectedly blocked", i+1)
		}
	}
	if limiter.allow("1.2.3.4", now) {
		t.Fatalf("burst beyond limit was allowed")
	}
	if !limiter.allow("5.6.7.8", now) {
		t.Fatalf("unrelated IP blocked")
	}
	if !limiter.allow("1.2.3.4", now.Add(loginRateLimitWindow+time.Second)) {
		t.Fatalf("window expiry did not reset the bucket")
	}
}

// S-03: 联邦 peer 下发的 config 含 shell hook 时必须被拒绝。
func TestValidatePeerResourceReferencesRejectsShellHooks(t *testing.T) {
	for _, key := range []string{"preUp", "postUp", "preDown", "postDown"} {
		bad := map[string]interface{}{
			"name":     "svc",
			"metadata": map[string]interface{}{key: []interface{}{"id >/tmp/pwned"}},
		}
		if err := validatePeerResourceReferences(bad, "service"); err == nil {
			t.Fatalf("shell hook %q in peer config accepted", key)
		}
	}
	// 空 hook 列表无害，应放行
	okCfg := map[string]interface{}{
		"name":     "svc",
		"metadata": map[string]interface{}{"postUp": []interface{}{}},
	}
	if err := validatePeerResourceReferences(okCfg, "service"); err != nil {
		t.Fatalf("empty hook list rejected: %v", err)
	}
}

// M-05: isStoredDefaultPassword 精确识别种子默认口令哈希。
func TestIsStoredDefaultPassword(t *testing.T) {
	hash, err := security.HashPassword("admin@user")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !isStoredDefaultPassword(0, hash) {
		t.Fatalf("default password not detected")
	}
	changed, err := security.HashPassword("my-secure-pwd-123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if isStoredDefaultPassword(0, changed) {
		t.Fatalf("changed password flagged as default")
	}
	if isStoredDefaultPassword(1, hash) {
		t.Fatalf("non-admin flagged")
	}
}
