package handler

import (
	"testing"
)

// R2 B09-001: user_tunnel 的 GB 流量上限键 flow 在 update / assign 两写面
// 缺 FIX2-A 同族守卫——在场但不可解析（"abc" / 1.5）被 asInt64 静默
// 吞成 0，而 flow=0 是「不限额」语义，等于配额被无声取消且回执成功；
// assign 面还绕过 -1 哨兵的用户默认回填。补守卫后两面均须报错且不落库，
// 合法值行为不变。夹具复用 r7_g02（user 2 默认 flow=100；UT id=10 flow=100）。

func TestR2K1UserTunnelUpdateRejectsUnparseableFlow(t *testing.T) {
	f := seedR7G02Fixture(t)

	utFlow := func() int64 {
		t.Helper()
		row, err := f.r.GetUserTunnelByID(10)
		if err != nil || row == nil {
			t.Fatalf("get user_tunnel 10: %v / %v", row, err)
		}
		return row.Flow
	}

	for _, bad := range []interface{}{"abc", 1.5} {
		if code := f.post(t, f.h.userTunnelUpdate, map[string]interface{}{"id": 10, "flow": bad}); code == 0 {
			t.Fatalf("userTunnelUpdate flow=%v accepted, want rejection", bad)
		}
		if got := utFlow(); got != 100 {
			t.Fatalf("flow persisted as %d after rejected update (flow=%v), want 100", got, bad)
		}
	}

	// 合法值照常落库。
	if code := f.post(t, f.h.userTunnelUpdate, map[string]interface{}{"id": 10, "flow": 7}); code != 0 {
		t.Fatal("userTunnelUpdate flow=7 rejected, want success")
	}
	if got := utFlow(); got != 7 {
		t.Fatalf("flow=%d after valid update, want 7", got)
	}
}

func TestR2K1UserTunnelAssignRejectsUnparseableFlow(t *testing.T) {
	f := seedR7G02Fixture(t)

	// 原始复现形态：flow="abc" 与 flowMiB 同场——曾静默落 0 而非用户默认 100。
	if code := f.post(t, f.h.userTunnelAssign, map[string]interface{}{
		"userId": 2, "tunnelId": 2, "flow": "abc", "flowMiB": 0,
	}); code == 0 {
		t.Fatal("userTunnelAssign flow=\"abc\" accepted, want rejection")
	}
	if n := mustQueryInt(t, f.r, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 2 AND tunnel_id = 2`); n != 0 {
		t.Fatalf("rejected assign persisted %d rows", n)
	}

	// 合法显式 flow 照常分配并落值。
	if code := f.post(t, f.h.userTunnelAssign, map[string]interface{}{
		"userId": 2, "tunnelId": 2, "flow": 5,
	}); code != 0 {
		t.Fatal("userTunnelAssign flow=5 rejected, want success")
	}
	var flow int64
	if err := f.r.DB().Raw(`SELECT flow FROM user_tunnel WHERE user_id = 2 AND tunnel_id = 2`).Scan(&flow).Error; err != nil {
		t.Fatalf("read assigned flow: %v", err)
	}
	if flow != 5 {
		t.Fatalf("assigned flow=%d, want 5", flow)
	}
}
