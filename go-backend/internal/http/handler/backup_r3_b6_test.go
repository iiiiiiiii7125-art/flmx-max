package handler

import (
	"strings"
	"testing"
)

// R3 批 6 / R3X-005 facet (a) handler 面（R5-FGRP 改版）：导入成员
// 替换只换成员行——被移出成员的组建授权、台账与其转发逐行保留，
// 回执不带 revokedUserTunnels（分组面恒无撤销）。夹具
// seedB2Fixture：UT10=U2/T1 组建授权 + forward20；导入把 U2 移出
// 用户组 1 后，UT10/台账/forward20 必须全数存续、手工面不受影响。
func TestR3B6BackupImportMemberReplacementKeepsGrantsAndForwards(t *testing.T) {
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
	if strings.Contains(body, `"revokedUserTunnels"`) {
		t.Fatalf("R5-FGRP: import receipt must not report revoked UTs for group facets, body = %s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_group_user WHERE user_group_id = 1 AND user_id = 2`); n != 0 {
		t.Fatalf("member row for removed user 2 must be replaced away, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 10`); n != 1 {
		t.Fatalf("R5-FGRP: group-created UT10 of removed member must survive import, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_tunnel_id = 10`); n != 1 {
		t.Fatalf("R5-FGRP: grant ledger for UT10 must survive import, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 20`); n != 1 {
		t.Fatalf("R5-FGRP: forward20 on UT10 must survive import, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 21`); n != 1 {
		t.Fatalf("forward21 on the manual tunnel must survive, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id IN (11, 12, 13)`); n != 3 {
		t.Fatalf("manual and remaining-member UTs must survive, got %d", n)
	}
}
