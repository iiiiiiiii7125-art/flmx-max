package contract_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-backend/internal/auth"
	"go-backend/internal/store/model"
)

func TestInboundBackupRoundTripContract(t *testing.T) {
	secret := "contract-jwt-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	landing := seedKernelNode(t, r, "landing")
	relayNode := seedKernelNode(t, r, "relay")

	inboundID := seedInboundOnNode(t, router, r, admin, landing,
		`{"nodeId":`+jsonInt64(landing)+`,"kernelType":"xray","name":"东京","protocol":"vless","port":443,"settings":{"flow":"xtls-rprx-vision"},"security":{"type":"reality"}}`)
	link := "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:secret")) + "@198.51.100.20:8388#ext"
	if out := kernelContractPost(t, router, admin, "/api/v1/inbound/import", `{"links":"`+link+`"}`); out.Code != 0 {
		t.Fatalf("import: %+v", out)
	}
	_, forwardID := seedRelayForward(t, r, 1, relayNode, 0, 30005, inboundID)
	settingsBefore := mustQueryString(t, r, `SELECT settings FROM node_inbound WHERE id = ?`, inboundID)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/export", bytes.NewBufferString(`{"types":["inbounds"]}`))
	req.Header.Set("Authorization", admin)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	var backup map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&backup); err != nil {
		t.Fatal(err)
	}
	section, _ := backup["inbounds"].(map[string]interface{})
	if section == nil || len(section["kernels"].([]interface{})) != 1 || len(section["inbounds"].([]interface{})) != 2 ||
		len(section["relays"].([]interface{})) != 1 {
		t.Fatalf("export must carry kernels, inbounds (imported too) and relays: %v", backup["inbounds"])
	}

	for _, table := range []string{"node_inbound_relay", "node_inbound", "node_kernel"} {
		if err := r.DB().Exec(`DELETE FROM ` + table).Error; err != nil {
			t.Fatal(err)
		}
	}

	backup["types"] = []string{"inbounds"}
	raw, _ := json.Marshal(backup)
	out := kernelContractPost(t, router, admin, "/api/v1/backup/import", string(raw))
	if out.Code != 0 || valueAsInt(out.Data.(map[string]interface{})["inboundsImported"]) != 2 {
		t.Fatalf("import backup: %+v", out)
	}
	if mustQueryInt(t, r, `SELECT COUNT(*) FROM node_inbound`) != 2 ||
		mustQueryString(t, r, `SELECT settings FROM node_inbound WHERE id = ?`, inboundID) != settingsBefore {
		t.Fatal("inbounds must come back unchanged")
	}
	if mustQueryInt(t, r, `SELECT COUNT(*) FROM node_inbound_relay WHERE inbound_id = ? AND forward_id = ?`, inboundID, forwardID) != 1 {
		t.Fatal("the relay link must come back")
	}
	if mustQueryString(t, r, `SELECT applied_hash FROM node_kernel WHERE node_id = ?`, landing) != "" {
		t.Fatal("a restored kernel must be pushed to its node again")
	}

	// Importing the same backup twice upserts instead of duplicating.
	if out := kernelContractPost(t, router, admin, "/api/v1/backup/import", string(raw)); out.Code != 0 {
		t.Fatalf("re-import: %+v", out)
	}
	if mustQueryInt(t, r, `SELECT COUNT(*) FROM node_inbound`) != 2 || mustQueryInt(t, r, `SELECT COUNT(*) FROM node_kernel`) != 1 {
		t.Fatal("re-import must not duplicate rows")
	}
}

// 3.3.29 #87-j：订阅源 kind/role 必须进备份往返——导出带出、导入还原，
// 旧备份（无 kind）导入归一为 'url'，二次导入按新值更新。
func TestRemoteSourceBackupKindRoleContract(t *testing.T) {
	secret := "contract-jwt-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)

	if err := r.SaveRemoteSource(&model.RemoteSource{Name: "文件源", Kind: model.RemoteSourceFile, Role: "relay", Enabled: 1, CreatedTime: 1, UpdatedTime: 1}); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveRemoteSource(&model.RemoteSource{Name: "网址源", URL: "https://example.test/sub", Kind: model.RemoteSourceURL, Role: "landing", Enabled: 1, CreatedTime: 1, UpdatedTime: 1}); err != nil {
		t.Fatal(err)
	}

	export := func() map[string]interface{} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/export", bytes.NewBufferString(`{"types":["inbounds"]}`))
		req.Header.Set("Authorization", admin)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		var backup map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&backup); err != nil {
			t.Fatal(err)
		}
		return backup
	}
	backup := export()
	sources, _ := backup["inbounds"].(map[string]interface{})["sources"].([]interface{})
	if len(sources) != 2 {
		t.Fatalf("export must carry both sources: %v", backup["inbounds"])
	}
	byName := map[string]map[string]interface{}{}
	for _, s := range sources {
		m := s.(map[string]interface{})
		byName[m["name"].(string)] = m
	}
	if byName["文件源"]["kind"] != "file" || byName["文件源"]["role"] != "relay" ||
		byName["网址源"]["kind"] != "url" || byName["网址源"]["role"] != "landing" {
		t.Fatalf("export must carry kind/role: %v", sources)
	}

	wipe := func() {
		if err := r.DB().Exec(`DELETE FROM remote_source`).Error; err != nil {
			t.Fatal(err)
		}
	}
	importRaw := func(b map[string]interface{}) {
		b["types"] = []string{"inbounds"}
		raw, _ := json.Marshal(b)
		if out := kernelContractPost(t, router, admin, "/api/v1/backup/import", string(raw)); out.Code != 0 {
			t.Fatalf("import backup: %+v", out)
		}
	}

	wipe()
	importRaw(backup)
	if got := mustQueryString(t, r, `SELECT kind || '/' || role FROM remote_source WHERE name = '文件源'`); got != "file/relay" {
		t.Fatalf("file source round trip: %q", got)
	}
	if got := mustQueryString(t, r, `SELECT kind || '/' || role FROM remote_source WHERE name = '网址源'`); got != "url/landing" {
		t.Fatalf("url source round trip: %q", got)
	}

	// 旧备份没有 kind 字段：导入一律归一 'url'，role 照旧保留。
	for _, s := range sources {
		delete(s.(map[string]interface{}), "kind")
	}
	wipe()
	importRaw(backup)
	if got := mustQueryString(t, r, `SELECT kind || '/' || role FROM remote_source WHERE name = '文件源'`); got != "url/relay" {
		t.Fatalf("old backup must normalize kind to url: %q", got)
	}
}
