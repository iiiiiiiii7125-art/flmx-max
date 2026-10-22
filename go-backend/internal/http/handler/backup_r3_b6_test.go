package handler

import (
	"strings"
	"testing"
)

// R3 批 6 / R3X-005 facet (a) handler 面：导入成员替换撤销的 UT 经
// ImportResult.revokedUserTunnels 回传后，backupImport 必须逐对清理
// 其转发（与运行时撤销路径同一 cleanupForwardsForUserTunnel 口径）。
// 夹具 seedB2Fixture：UT10=U2/T1 组建授权 + forward20；导入把 U2
// 移出用户组 1 后，UT10 与 forward20 必须清、手工面不受影响。
func TestR3B6BackupImportRevokedForwardsCleaned(t *testing.T) {
	f := seedB2Fixture(t)

	code, body := f.post(t, f.h.backupImport, map[string]interface{}{
		"types":   []string{"userGroups"},
		"version": "1.0",
		"userGroups": []map[string]interface{}{
			{"id": 1, "name": "g-one", "status": 1, "createdTime": 1000, "users": []int64{4}},
		},
	})
	if code != 0 {
		t.Fatalf("backupImport code = %d, body = %s", code, body)
	}
	if !strings.Contains(body, `"revokedUserTunnels"`) {
		t.Fatalf("R3X-005(a): import receipt must carry revokedUserTunnels for the revoked UT, body = %s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 10`); n != 0 {
		t.Fatalf("R3X-005(a): group-created UT10 of removed member must be revoked by import, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_tunnel_id = 10`); n != 0 {
		t.Fatalf("R3X-005(a): grant ledger for UT10 must be revoked by import, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 20`); n != 0 {
		t.Fatalf("R3X-005(a): forward20 on the revoked UT must be cleaned by the handler hook, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 21`); n != 1 {
		t.Fatalf("forward21 on the manual tunnel must survive, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id IN (11, 12, 13)`); n != 3 {
		t.Fatalf("manual and remaining-member UTs must survive, got %d", n)
	}
}
