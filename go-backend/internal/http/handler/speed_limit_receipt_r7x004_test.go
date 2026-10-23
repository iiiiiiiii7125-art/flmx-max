package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R7X-004（R7-E-01）：限速规则改/删的重下发（R6X-017 通道）在
// nftables 隧道面丢弃「限速不生效」warnings、两 handler 恒 OKEmpty。
// 修复口径：resyncForwardsForSpeedLimit 改走
// syncForwardServicesWithWarnings 汇总 warnings，speedLimitUpdate /
// speedLimitDelete 以与 forwardUpdate 同构的 data.warnings 回执应答。
// 反向验证两格：
//   - nft 面改/删后回执必含 nft 限速不生效 warnings（修复前必红）；
//   - gost 面改/删后回执不得含该 warnings（防误报过修，恒绿对照）。
//
// 删除格的谓词说明（防假红/假绿）：规则删除后，被删规则本身经
// GetSpeedLimitSpeed（status=1 过滤 + 行已删）不再构成 ineffective，
// 故删除格给转发同时挂一条仍有效的 ip_speed_id 规则 F——删除 R 后
// 转发仍有限速口径（F）且在 nft 面不生效，warnings 应披露的正是
// 这批受重下发转发的现行限速不生效事实，与改值格同口径。

const r7x004NftWarningFragment = "nftables 模式不支持限速和连接数限制"

type r7x004Envelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data *struct {
		Warnings []string `json:"warnings"`
	} `json:"data"`
}

func r7x004Decode(t *testing.T, res *httptest.ResponseRecorder) r7x004Envelope {
	t.Helper()
	var env r7x004Envelope
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, res.Body.String())
	}
	return env
}

func r7x004HasNftWarning(env r7x004Envelope) bool {
	if env.Data == nil {
		return false
	}
	for _, w := range env.Data.Warnings {
		if strings.Contains(w, r7x004NftWarningFragment) {
			return true
		}
	}
	return false
}

// r7x004NftSetup 建 nftables 隧道 + 一条引用限速规则的转发。
// forwardRef: speed_id 指向 ruleID；withIPFallback 时 ip_speed_id
// 同时指向另一条有效规则（删除格的现行限速口径）。
func r7x004NftSetup(t *testing.T, name string, withIPFallback bool) (*Handler, int64, int64) {
	t.Helper()
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	r := h.repo
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, name, fixture.nodeID)

	mkLimit := func(limitName string, speed int) int64 {
		t.Helper()
		sl := &model.SpeedLimit{Name: limitName, Speed: speed, CreatedTime: time.Now().UnixMilli(), Status: 1}
		if err := r.DB().Create(sl).Error; err != nil {
			t.Fatalf("seed speed_limit %s: %v", limitName, err)
		}
		return sl.ID
	}
	ruleID := mkLimit(name+"-rule", 10)
	var ipSpeedArg interface{}
	if withIPFallback {
		ipSpeedArg = mkLimit(name+"-ip-fallback", 5)
	}
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", 20201)
	if err := r.DB().Exec(`UPDATE forward SET speed_id = ?, ip_speed_id = ? WHERE id = ?`, ruleID, ipSpeedArg, forward.ID).Error; err != nil {
		t.Fatalf("bind speed limits to forward: %v", err)
	}
	return h, ruleID, forward.ID
}

// 改值格（nft）：speed 10→20（规则仍有效）后回执须含 nft warnings。
func TestR7X004SpeedLimitUpdateNftReceiptCarriesWarnings(t *testing.T) {
	h, ruleID, _ := r7x004NftSetup(t, "r7x004-nft-update", false)

	res := httptest.NewRecorder()
	h.speedLimitUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": ruleID, "speed": 20}))
	env := r7x004Decode(t, res)
	if env.Code != 0 {
		t.Fatalf("speedLimitUpdate failed: code=%d msg=%q body=%s", env.Code, env.Msg, res.Body.String())
	}
	if !r7x004HasNftWarning(env) {
		t.Fatalf("R7X-004: nft speedLimitUpdate receipt must carry %q warnings, got body=%s", r7x004NftWarningFragment, res.Body.String())
	}
	if row, err := h.repo.GetSpeedLimit(ruleID); err != nil || row == nil || row.Speed != 20 {
		t.Fatalf("update must persist in DB (no rollback), got %+v err=%v", row, err)
	}
}

// 删除格（nft）：删除 R 后转发仍挂有效 ip 规则 F、nft 面不生效，
// 回执须含 nft warnings；DB 中 R 已删（不回滚）。
func TestR7X004SpeedLimitDeleteNftReceiptCarriesWarnings(t *testing.T) {
	h, ruleID, _ := r7x004NftSetup(t, "r7x004-nft-delete", true)

	res := httptest.NewRecorder()
	h.speedLimitDelete(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": ruleID}))
	env := r7x004Decode(t, res)
	if env.Code != 0 {
		t.Fatalf("speedLimitDelete failed: code=%d msg=%q body=%s", env.Code, env.Msg, res.Body.String())
	}
	if !r7x004HasNftWarning(env) {
		t.Fatalf("R7X-004: nft speedLimitDelete receipt must carry %q warnings, got body=%s", r7x004NftWarningFragment, res.Body.String())
	}
	if row, err := h.repo.GetSpeedLimit(ruleID); err != nil || row != nil {
		t.Fatalf("delete must persist in DB (no rollback), got %+v err=%v", row, err)
	}
}

// 对照格（gost）：同一改/删序列在 gost 隧道面回执不得含 nft
// warnings（gost 正常执法、无此披露义务；防把 warnings 无条件
// 塞回执的过修）。复用 R6X-017 的 gost 夹具（mock agent 在线）。
func TestR7X004SpeedLimitGostReceiptHasNoNftWarning(t *testing.T) {
	s := r6x017Setup(t, 1, 2)

	res := httptest.NewRecorder()
	s.h.speedLimitUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": s.ruleID, "speed": 20}))
	env := r7x004Decode(t, res)
	if env.Code != 0 {
		t.Fatalf("gost speedLimitUpdate failed: code=%d msg=%q body=%s", env.Code, env.Msg, res.Body.String())
	}
	if r7x004HasNftWarning(env) || strings.Contains(res.Body.String(), r7x004NftWarningFragment) {
		t.Fatalf("R7X-004 over-fix: gost speedLimitUpdate receipt must not carry nft warning, got body=%s", res.Body.String())
	}

	res2 := httptest.NewRecorder()
	s.h.speedLimitDelete(res2, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": s.ruleID}))
	env2 := r7x004Decode(t, res2)
	if env2.Code != 0 {
		t.Fatalf("gost speedLimitDelete failed: code=%d msg=%q body=%s", env2.Code, env2.Msg, res2.Body.String())
	}
	if r7x004HasNftWarning(env2) || strings.Contains(res2.Body.String(), r7x004NftWarningFragment) {
		t.Fatalf("R7X-004 over-fix: gost speedLimitDelete receipt must not carry nft warning, got body=%s", res2.Body.String())
	}
}
