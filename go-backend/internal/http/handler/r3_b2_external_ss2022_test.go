package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R3 批 2（R3X-008）保存层补点：旧格式（proxy 化之前）导入节点的编辑
// 分支绕 Normalize 直写 method/password，必须复用与导入期同一的
// SS2022 密钥校验 helper，坏 key 在保存期被拒、原值不动；合法四形态
// 与 legacy 明文照常保存。

func setupR3B2LegacyExternal(t *testing.T) (*Handler, *repo.Repository, int64) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/r3b2.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	row := model.NodeInbound{
		Name: "legacy-ss", Protocol: kernel.ProtocolShadowsocks,
		Listen: "203.0.113.10", Port: 8388, ShareHost: "203.0.113.10",
		Settings:    `{"method":"aes-256-gcm","password":"old-pass"}`,
		Enabled:     1,
		CreatedTime: 1, UpdatedTime: 1,
	}
	if err := r.DB().Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return New(r, "unit-test-secret"), r, row.ID
}

func r3b2ExternalUpdate(t *testing.T, h *Handler, id int64, method, password string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{
		"id": id, "name": "legacy-ss", "host": "203.0.113.10", "port": 8388,
		"method": method, "password": password,
	})
	req := httptest.NewRequest(http.MethodPost, "/inbound/external/update", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.inboundExternalUpdate(w, req)
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	return resp.Code, resp.Msg
}

func r3b2StoredSettings(t *testing.T, r *repo.Repository, id int64) kernel.Settings {
	t.Helper()
	row, err := r.GetNodeInbound(id)
	if err != nil || row == nil {
		t.Fatalf("reload inbound: %v", err)
	}
	var s kernel.Settings
	if err := json.Unmarshal([]byte(row.Settings), &s); err != nil {
		t.Fatalf("settings json: %v (%q)", err, row.Settings)
	}
	return s
}

func TestR3B2ExternalUpdateLegacyBranchSS2022(t *testing.T) {
	h, r, id := setupR3B2LegacyExternal(t)
	const c256 = "2022-blake3-aes-256-gcm"
	raw32 := make([]byte, 32)
	for i := range raw32 {
		raw32[i] = byte(i)
	}
	goodStd := base64.StdEncoding.EncodeToString(raw32)

	// 坏 key（明文）在旧格式编辑分支必须被拒，且存量值不动。
	code, msg := r3b2ExternalUpdate(t, h, id, c256, "plainpassword")
	if code == 0 {
		t.Fatalf("坏 SS2022 key 必须在保存期被拒，got code=0 msg=%q", msg)
	}
	if !strings.Contains(msg, "SS2022") {
		t.Fatalf("报错须点名 SS2022：%q", msg)
	}
	if s := r3b2StoredSettings(t, r, id); s.Method != "aes-256-gcm" || s.Password != "old-pass" {
		t.Fatalf("拒绝后存量不得改动：%+v", s)
	}

	// 错字节数（16B key 喂 32B cipher）同样被拒。
	key16 := base64.StdEncoding.EncodeToString(raw32[:16])
	if code, msg := r3b2ExternalUpdate(t, h, id, c256, key16); code == 0 {
		t.Fatalf("错字节数 SS2022 key 必须被拒，got code=0 msg=%q", msg)
	}

	// 合法 Std 形态可保存（防误拒）。
	if code, msg := r3b2ExternalUpdate(t, h, id, c256, goodStd); code != 0 {
		t.Fatalf("合法 SS2022 key 应保存成功：code=%d msg=%q", code, msg)
	}
	if s := r3b2StoredSettings(t, r, id); s.Method != c256 || s.Password != goodStd {
		t.Fatalf("保存后值不对：%+v", s)
	}

	// legacy 明文照常保存（不查）。
	if code, msg := r3b2ExternalUpdate(t, h, id, "aes-256-gcm", "plainpassword"); code != 0 {
		t.Fatalf("legacy 明文应保存成功：code=%d msg=%q", code, msg)
	}
	if s := r3b2StoredSettings(t, r, id); s.Password != "plainpassword" {
		t.Fatalf("legacy 保存后值不对：%+v", s)
	}
}
