package handler

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// C-02（流量审计）：超限分享订阅轮换原站 SS 共享口令。
// 覆盖：SS 原站口令被轮换为合法 2022 PSK；VLESS 原站、外部入站、
// 伴生本体均不动；同回合二调不重复轮换；流量重置后再次超限可再轮换；
// 非分享订阅不轮换。
func TestRotateQuotaOriginPasswords(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	now := time.Now()
	yesterday := now.Add(-24 * time.Hour).UnixMilli()

	// 添三个应被跳过的原站：VLESS（非 SS）、外部 SS（无内核）、
	// SS 伴生（只轮原站，不轮伴生本体）。
	vless := seedChainInbound(t, r, k, "c02-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Settings: kernel.Settings{UUID: "11111111-2222-3333-4444-555555555555"}}, 0)
	extSettings, _ := json.Marshal(kernel.Settings{Method: meteringMethod, Password: "ext-old-password"})
	ext := &model.NodeInbound{KernelID: 0, NodeID: origin.NodeID, Name: "c02-ext-ss",
		Protocol: kernel.ProtocolShadowsocks, Port: 9999, Enabled: 1,
		Settings: string(extSettings), CreatedTime: now.UnixMilli(), UpdatedTime: now.UnixMilli()}
	if err := r.CreateNodeInbound(ext); err != nil {
		t.Fatal(err)
	}
	compSettings, _ := json.Marshal(kernel.Settings{Method: meteringMethod, Password: "comp-old-password"})
	comp := &model.NodeInbound{KernelID: k.ID, NodeID: origin.NodeID, Name: "c02-comp",
		Protocol: kernel.ProtocolShadowsocks, Port: 18388, Enabled: 1, CompanionOf: origin.ID,
		Settings: string(compSettings), CreatedTime: now.UnixMilli(), UpdatedTime: now.UnixMilli()}
	if err := r.CreateNodeInbound(comp); err != nil {
		t.Fatal(err)
	}

	// 订阅超限：限额 1000B，已用 5000B；计费回合始于昨天。
	// 先抓三跳过项的 Settings 原文，轮换后逐字比对。
	vlessBefore := c02InboundSettings(t, r, vless.ID)
	extBefore := c02InboundSettings(t, r, ext.ID)
	compBefore := c02InboundSettings(t, r, comp.ID)
	if err := r.DB().Model(&model.Subscription{}).Where("id = ?", sub.ID).Updates(map[string]interface{}{
		"traffic_limit": 1000, "upload_bytes": 3000, "download_bytes": 2000,
		"traffic_since": yesterday,
	}).Error; err != nil {
		t.Fatal(err)
	}
	sub.TrafficLimit, sub.UploadBytes, sub.DownloadBytes, sub.TrafficSince = 1000, 3000, 2000, yesterday

	var rows []model.NodeInbound // nil：测懒加载
	if rotated := h.rotateQuotaOriginPasswords(sub, &rows, now); !rotated {
		t.Fatalf("over-quota share sub must rotate an origin password")
	}
	// SS 原站口令已换为合法 2022 PSK（16 字节）。
	rotatedPW := c02InboundPassword(t, r, origin.ID)
	if rotatedPW == meteringSharedPW {
		t.Fatalf("origin SS password unchanged after rotation")
	}
	raw, err := base64.StdEncoding.DecodeString(rotatedPW)
	if err != nil || len(raw) != 16 {
		t.Fatalf("rotated password is not a valid 16-byte 2022 PSK: %q err=%v", rotatedPW, err)
	}
	// 三个跳过项的 Settings 原样（口令与全部字段均不动）。
	if got := c02InboundSettings(t, r, vless.ID); got != vlessBefore {
		t.Fatalf("vless origin settings changed: %q vs %q", got, vlessBefore)
	}
	if got := c02InboundSettings(t, r, ext.ID); got != extBefore {
		t.Fatalf("external inbound settings changed: %q vs %q", got, extBefore)
	}
	if got := c02InboundSettings(t, r, comp.ID); got != compBefore {
		t.Fatalf("companion settings changed: %q vs %q", got, compBefore)
	}
	// 戳已落库（读回验证），且 AppliedActive 停放为 -1 待下发
	// 重试（R5R8-001 同款；下发成功后由 sync 循环写回 active）。
	fresh, err := r.GetSubscription(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.QuotaRotatedAt < now.UnixMilli()-1000 || fresh.QuotaRotatedAt > now.UnixMilli()+1000 {
		t.Fatalf("quota_rotated_at = %d, want ~now %d", fresh.QuotaRotatedAt, now.UnixMilli())
	}
	if fresh.AppliedActive != -1 {
		t.Fatalf("rotated sub AppliedActive = %d, want -1 (parked for push retry)", fresh.AppliedActive)
	}
	// 同回合二调：不重复轮换。
	if rotated := h.rotateQuotaOriginPasswords(fresh, &rows, now.Add(time.Minute)); rotated {
		t.Fatalf("second call in the same episode must not rotate again")
	}
	if got := c02InboundPassword(t, r, origin.ID); got != rotatedPW {
		t.Fatalf("origin password changed on second call: %q vs %q", got, rotatedPW)
	}
	// 流量重置后再次超限：TrafficSince 前进，重新轮换。
	resetAt := now.Add(2 * time.Minute).UnixMilli()
	if err := r.ResetSubscriptionTraffic(sub.ID, resetAt); err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Model(&model.Subscription{}).Where("id = ?", sub.ID).Updates(map[string]interface{}{
		"upload_bytes": 4000, "download_bytes": 2000,
	}).Error; err != nil {
		t.Fatal(err)
	}
	fresh2, err := r.GetSubscription(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh2.UploadBytes, fresh2.DownloadBytes = 4000, 2000
	if rotated := h.rotateQuotaOriginPasswords(fresh2, &rows, now.Add(3*time.Minute)); !rotated {
		t.Fatalf("over-quota again after reset must rotate again")
	}
	if got := c02InboundPassword(t, r, origin.ID); got == rotatedPW {
		t.Fatalf("origin password unchanged after second-episode rotation")
	}
}

// 非分享订阅即使超限也不轮换（个人订阅无共享口令可轮）。
func TestRotateQuotaOriginPasswordsSkipsOwnSubscription(t *testing.T) {
	r, h, _, origin, _ := seedMeteringFixture(t)
	now := time.Now()
	own := &model.Subscription{Name: "c02-own", Kind: model.SubscriptionOwn, Token: "e0000000000000000000000000000000",
		UUID: "u-own", Password: "own-pw", Enabled: 1, SourceAll: 1,
		TrafficLimit: 1000, UploadBytes: 5000, CreatedTime: now.UnixMilli(), UpdatedTime: now.UnixMilli()}
	if err := r.SaveSubscription(own); err != nil {
		t.Fatal(err)
	}
	var rows []model.NodeInbound
	if rotated := h.rotateQuotaOriginPasswords(own, &rows, now); rotated {
		t.Fatalf("own subscription must never rotate origin passwords")
	}
	if got := c02InboundPassword(t, r, origin.ID); got != meteringSharedPW {
		t.Fatalf("origin password changed for own sub: %q", got)
	}
}

// C-(5)：停用/过期分享订阅即使超限也不轮换原站口令——轮换只会逼
// 全部共口令订阅重拉链接，而失活同步早已把凭据从节点撤下。
// 对照：启用且未过期的超限分享订阅仍轮换（首测已覆）。
func TestRotateQuotaOriginPasswordsSkipsDisabledAndExpired(t *testing.T) {
	r, h, _, origin, sub := seedMeteringFixture(t)
	now := time.Now()
	yesterday := now.Add(-24 * time.Hour).UnixMilli()
	if err := r.DB().Model(&model.Subscription{}).Where("id = ?", sub.ID).Updates(map[string]interface{}{
		"traffic_limit": 1000, "upload_bytes": 3000, "download_bytes": 2000,
		"traffic_since": yesterday,
	}).Error; err != nil {
		t.Fatal(err)
	}
	sub.TrafficLimit, sub.UploadBytes, sub.DownloadBytes, sub.TrafficSince = 1000, 3000, 2000, yesterday
	var rows []model.NodeInbound

	// 停用腿：Enabled=0。
	disabled := *sub
	disabled.Enabled = 0
	if rotated := h.rotateQuotaOriginPasswords(&disabled, &rows, now); rotated {
		t.Fatalf("disabled over-quota share sub must not rotate")
	}
	if got := c02InboundPassword(t, r, origin.ID); got != meteringSharedPW {
		t.Fatalf("origin password changed for disabled sub: %q", got)
	}
	if fresh, err := r.GetSubscription(sub.ID); err != nil {
		t.Fatal(err)
	} else if fresh.QuotaRotatedAt != 0 {
		t.Fatalf("quota_rotated_at = %d after disabled leg, want 0", fresh.QuotaRotatedAt)
	}

	// 过期腿：ExpireTime 一小时前（启用）。
	expired := *sub
	expired.ExpireTime = now.Add(-time.Hour).UnixMilli()
	if rotated := h.rotateQuotaOriginPasswords(&expired, &rows, now); rotated {
		t.Fatalf("expired over-quota share sub must not rotate")
	}
	if got := c02InboundPassword(t, r, origin.ID); got != meteringSharedPW {
		t.Fatalf("origin password changed for expired sub: %q", got)
	}
	if fresh, err := r.GetSubscription(sub.ID); err != nil {
		t.Fatal(err)
	} else if fresh.QuotaRotatedAt != 0 {
		t.Fatalf("quota_rotated_at = %d after expired leg, want 0", fresh.QuotaRotatedAt)
	}

	// 启用且未过期仍轮换（回路正控制）。
	if rotated := h.rotateQuotaOriginPasswords(sub, &rows, now); !rotated {
		t.Fatalf("enabled unexpired over-quota share sub must rotate")
	}
	if got := c02InboundPassword(t, r, origin.ID); got == meteringSharedPW {
		t.Fatalf("origin password unchanged for enabled unexpired sub")
	}
}

func c02InboundSettings(t *testing.T, r *repo.Repository, id int64) string {
	t.Helper()
	row, err := r.GetNodeInbound(id)
	if err != nil {
		t.Fatal(err)
	}
	return row.Settings
}

func c02InboundPassword(t *testing.T, r *repo.Repository, id int64) string {
	t.Helper()
	var settings kernel.Settings
	if err := json.Unmarshal([]byte(c02InboundSettings(t, r, id)), &settings); err != nil {
		t.Fatalf("inbound %d settings unparsable: %v", id, err)
	}
	return settings.Password
}
