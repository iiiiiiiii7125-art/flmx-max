package contract_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
)

// R5-FGRP 契约：分组只限定未来分配的可选范围——绑定/授权不物化
// 任何实体授权，解绑/撤权不收回既有手工授权；分配时范围外显式
// 拒绝并点名，范围内照常。

func fgrpPost(t *testing.T, router http.Handler, path, body, token string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", token)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	var payload struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response for %s: %v; body=%s", path, err, res.Body.String())
	}
	return payload.Code, res.Body.String()
}

func TestGroupScopeBindMaterializesNothingAndUnbindKeepsManualGrant(t *testing.T) {
	secret := "contract-jwt-secret"
	router, repo := setupContractRouter(t, secret)
	now := time.Now().UnixMilli()

	if err := repo.DB().Exec(`
		INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status)
		VALUES(200, 'group_user_contract', '3c85cdebade1c51cf64ca9f3c09d182d', 1, 2727251700000, 99999, 0, 0, 1, 99999, ?, ?, 1)
	`, now, now).Error; err != nil {
		t.Fatalf("insert test user: %v", err)
	}

	if err := repo.DB().Exec(`
		INSERT INTO tunnel(name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx)
		VALUES('group-contract-tunnel', 1.0, 1, 'tls', 99999, ?, ?, 1, NULL, 0)
	`, now, now).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	tunnelID := mustLastInsertID(t, repo, "group-contract-tunnel")

	if err := repo.DB().Exec(`INSERT INTO user_group(name, created_time, updated_time, status) VALUES('ug-contract', ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert user_group: %v", err)
	}
	userGroupID := mustLastInsertID(t, repo, "ug-contract")

	if err := repo.DB().Exec(`INSERT INTO tunnel_group(name, created_time, updated_time, status) VALUES('tg-contract', ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert tunnel_group: %v", err)
	}
	tunnelGroupID := mustLastInsertID(t, repo, "tg-contract")

	if err := repo.DB().Exec(`INSERT INTO tunnel_group_tunnel(tunnel_group_id, tunnel_id, created_time) VALUES(?, ?, ?)`, tunnelGroupID, tunnelID, now).Error; err != nil {
		t.Fatalf("insert tunnel_group_tunnel: %v", err)
	}
	if err := repo.DB().Exec(`INSERT INTO group_permission(user_group_id, tunnel_group_id, created_time) VALUES(?, ?, ?)`, userGroupID, tunnelGroupID, now).Error; err != nil {
		t.Fatalf("insert group_permission: %v", err)
	}

	adminToken, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("generate admin token: %v", err)
	}

	// 绑定成员：只登记成员关系，不得物化任何授权/台账。
	code, body := fgrpPost(t, router, "/api/v1/group/user/assign", `{"groupId":`+jsonNumber(userGroupID)+`,"userIds":[200]}`, adminToken)
	if code != 0 {
		t.Fatalf("bind must succeed, code=%d body=%s", code, body)
	}
	if n := mustQueryInt(t, repo, `SELECT COUNT(1) FROM user_tunnel WHERE user_id = 200 AND tunnel_id = ?`, tunnelID); n != 0 {
		t.Fatalf("R5-FGRP: bind must not materialize user_tunnel, got %d", n)
	}
	if n := mustQueryInt(t, repo, `SELECT COUNT(1) FROM group_permission_grant`); n != 0 {
		t.Fatalf("R5-FGRP: bind must not write grant ledger rows, got %d", n)
	}

	// 范围内手工分配照常成功，且不写台账（授权来源就是手工分配本身）。
	code, body = fgrpPost(t, router, "/api/v1/tunnel/user/assign", `{"userId":200,"tunnelId":`+jsonNumber(tunnelID)+`}`, adminToken)
	if code != 0 {
		t.Fatalf("in-scope assign must succeed, code=%d body=%s", code, body)
	}
	userTunnelID := mustQueryInt64(t, repo, `SELECT id FROM user_tunnel WHERE user_id = 200 AND tunnel_id = ?`, tunnelID)
	if n := mustQueryInt(t, repo, `SELECT COUNT(1) FROM group_permission_grant WHERE user_tunnel_id = ?`, userTunnelID); n != 0 {
		t.Fatalf("manual assign must not write grant ledger rows, got %d", n)
	}

	// 解绑：成员关系清除，既有手工授权逐行保留。
	code, body = fgrpPost(t, router, "/api/v1/group/user/assign", `{"groupId":`+jsonNumber(userGroupID)+`,"userIds":[]}`, adminToken)
	if code != 0 {
		t.Fatalf("unbind must succeed, code=%d body=%s", code, body)
	}
	if n := mustQueryInt(t, repo, `SELECT COUNT(1) FROM user_tunnel WHERE id = ?`, userTunnelID); n != 1 {
		t.Fatalf("R5-FGRP: manual grant must survive unbind, got %d", n)
	}
}

func TestGroupPermissionAssignRegistersScopeAndRemoveKeepsManualGrant(t *testing.T) {
	secret := "contract-jwt-secret"
	router, repo := setupContractRouter(t, secret)
	now := time.Now().UnixMilli()

	if err := repo.DB().Exec(`
		INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status)
		VALUES(201, 'group_user_permission_remove', '3c85cdebade1c51cf64ca9f3c09d182d', 1, 2727251700000, 99999, 0, 0, 1, 99999, ?, ?, 1)
	`, now, now).Error; err != nil {
		t.Fatalf("insert test user: %v", err)
	}

	if err := repo.DB().Exec(`
		INSERT INTO tunnel(name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx)
		VALUES('group-remove-tunnel', 1.0, 1, 'tls', 99999, ?, ?, 1, NULL, 0)
	`, now, now).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	tunnelID := mustLastInsertID(t, repo, "group-remove-tunnel")

	if err := repo.DB().Exec(`
		INSERT INTO tunnel(name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx)
		VALUES('group-outside-tunnel', 1.0, 1, 'tls', 99999, ?, ?, 1, NULL, 0)
	`, now, now).Error; err != nil {
		t.Fatalf("insert outside tunnel: %v", err)
	}
	outsideTunnelID := mustLastInsertID(t, repo, "group-outside-tunnel")

	if err := repo.DB().Exec(`INSERT INTO user_group(name, created_time, updated_time, status) VALUES('ug-remove-contract', ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert user_group: %v", err)
	}
	userGroupID := mustLastInsertID(t, repo, "ug-remove-contract")

	if err := repo.DB().Exec(`INSERT INTO tunnel_group(name, created_time, updated_time, status) VALUES('tg-remove-contract', ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert tunnel_group: %v", err)
	}
	tunnelGroupID := mustLastInsertID(t, repo, "tg-remove-contract")

	adminToken, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("generate admin token: %v", err)
	}

	code, body := fgrpPost(t, router, "/api/v1/group/tunnel/assign", `{"groupId":`+jsonNumber(tunnelGroupID)+`,"tunnelIds":[`+jsonNumber(tunnelID)+`]}`, adminToken)
	if code != 0 {
		t.Fatalf("tunnel group assign must succeed, code=%d body=%s", code, body)
	}
	code, body = fgrpPost(t, router, "/api/v1/group/user/assign", `{"groupId":`+jsonNumber(userGroupID)+`,"userIds":[201]}`, adminToken)
	if code != 0 {
		t.Fatalf("user group assign must succeed, code=%d body=%s", code, body)
	}
	code, body = fgrpPost(t, router, "/api/v1/group/permission/assign", `{"userGroupId":`+jsonNumber(userGroupID)+`,"tunnelGroupId":`+jsonNumber(tunnelGroupID)+`}`, adminToken)
	if code != 0 {
		t.Fatalf("permission assign must succeed, code=%d body=%s", code, body)
	}
	permissionID := mustQueryInt64(t, repo, `SELECT id FROM group_permission WHERE user_group_id = ? AND tunnel_group_id = ?`, userGroupID, tunnelGroupID)

	// 组授权只登记范围：零物化。
	if n := mustQueryInt(t, repo, `SELECT COUNT(1) FROM user_tunnel WHERE user_id = 201`); n != 0 {
		t.Fatalf("R5-FGRP: permission assign must not materialize user_tunnel, got %d", n)
	}
	if n := mustQueryInt(t, repo, `SELECT COUNT(1) FROM group_permission_grant`); n != 0 {
		t.Fatalf("R5-FGRP: permission assign must not write grant ledger rows, got %d", n)
	}

	// 范围外分配：显式拒绝并点名（错误信息含隧道名与范围字样），零落行。
	code, body = fgrpPost(t, router, "/api/v1/tunnel/user/assign", `{"userId":201,"tunnelId":`+jsonNumber(outsideTunnelID)+`}`, adminToken)
	if code == 0 {
		t.Fatalf("out-of-scope assign must be rejected; body=%s", body)
	}
	if !strings.Contains(body, "授权范围") || !strings.Contains(body, "group-outside-tunnel") {
		t.Fatalf("rejection must name the tunnel and the scope; body=%s", body)
	}
	if n := mustQueryInt(t, repo, `SELECT COUNT(1) FROM user_tunnel WHERE user_id = 201 AND tunnel_id = ?`, outsideTunnelID); n != 0 {
		t.Fatalf("rejected assign must not persist, got %d", n)
	}

	// 范围内分配照常。
	code, body = fgrpPost(t, router, "/api/v1/tunnel/user/assign", `{"userId":201,"tunnelId":`+jsonNumber(tunnelID)+`}`, adminToken)
	if code != 0 {
		t.Fatalf("in-scope assign must succeed, code=%d body=%s", code, body)
	}
	userTunnelID := mustQueryInt64(t, repo, `SELECT id FROM user_tunnel WHERE user_id = 201 AND tunnel_id = ?`, tunnelID)

	// 撤权：只删范围登记行，既有手工授权保留；撤权后范围约束解除。
	code, body = fgrpPost(t, router, "/api/v1/group/permission/remove", `{"id":`+jsonNumber(permissionID)+`}`, adminToken)
	if code != 0 {
		t.Fatalf("permission remove must succeed, code=%d body=%s", code, body)
	}
	if n := mustQueryInt(t, repo, `SELECT COUNT(1) FROM group_permission WHERE id = ?`, permissionID); n != 0 {
		t.Fatalf("expected group_permission removed, got %d", n)
	}
	if n := mustQueryInt(t, repo, `SELECT COUNT(1) FROM user_tunnel WHERE id = ?`, userTunnelID); n != 1 {
		t.Fatalf("R5-FGRP: manual grant must survive permission remove, got %d", n)
	}
	code, body = fgrpPost(t, router, "/api/v1/tunnel/user/assign", `{"userId":201,"tunnelId":`+jsonNumber(outsideTunnelID)+`}`, adminToken)
	if code != 0 {
		t.Fatalf("assign after permission remove must be unconstrained, code=%d body=%s", code, body)
	}
}
