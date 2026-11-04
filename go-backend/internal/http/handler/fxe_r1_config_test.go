package handler

import (
	"testing"
)

// R1-11-02 回归：jwt_secret 的 DB 配置行零消费，config/update 与
// config/update-single 写非空值必须点名拒绝（指引部署面轮换），
// 不得再假成功落库；既有行保持不变。
func TestConfigUpdateRejectsJWTSecretWrite(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)
	seedConfigValue(t, r, "jwt_secret", "keep-me")

	resp := postG3(t, router, "/api/v1/config/update", adminToken, `{"jwt_secret":"rotated-value"}`)
	assertHandlerCodeMsgContains(t, resp, "jwt_secret")
	if cfg, err := r.GetConfigByName("jwt_secret"); err != nil || cfg == nil || cfg.Value != "keep-me" {
		t.Fatalf("batch update must not land jwt_secret, got %#v err=%v", cfg, err)
	}

	resp = postG3(t, router, "/api/v1/config/update-single", adminToken, `{"name":"jwt_secret","value":"rotated-value"}`)
	assertHandlerCodeMsgContains(t, resp, "jwt_secret")
	if cfg, err := r.GetConfigByName("jwt_secret"); err != nil || cfg == nil || cfg.Value != "keep-me" {
		t.Fatalf("single update must not land jwt_secret, got %#v err=%v", cfg, err)
	}
}
