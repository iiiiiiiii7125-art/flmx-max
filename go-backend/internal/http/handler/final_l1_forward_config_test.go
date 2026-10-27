package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/store/repo"
)

// 终审修复轮 Lane 3（L1-F-06/07/08）：空标签主机名 `a..b` 在转发目标、
// 配置键与订阅源 URL 保存面被接受。先红后绿回归测试——本文件先写只调
// 已存在函数的部分，修复前应全部失败；helper 单测在修复实现后补。

// L1-F-06 机制单测：validateNoEmptyLabelAddr（forwardCreate /
// forwardUpdate 共用谓词；update 面与 create 面同一代码路径）。
func TestFinalL1ValidateNoEmptyLabelAddr(t *testing.T) {
	cases := []struct {
		addr    string
		wantErr bool
	}{
		{"a..b:80", true},
		{"a...b:443", true},
		{"a..b", true}, // 无端口也判 host
		{"example.com:80", false},
		{"example.com", false},
		{"203.0.113.9:80", false},
		{"[2001:db8::1]:80", false},
		{"2001:db8::1", false}, // 裸 IPv6 字面量
		{"a..b:80,example.com:80", true},
		{"example.com:80,example.com:81", false},
		{"example.com:80\nexample.com:81", false},
		{"", false}, // 空值由调用方另行处理
	}
	for _, c := range cases {
		err := validateNoEmptyLabelAddr(c.addr)
		if c.wantErr && err == nil {
			t.Errorf("validateNoEmptyLabelAddr(%q) must error", c.addr)
		}
		if !c.wantErr && err != nil {
			t.Errorf("validateNoEmptyLabelAddr(%q) must pass, got %v", c.addr, err)
		}
		if err != nil && !strings.Contains(err.Error(), "空标签") {
			t.Errorf("validateNoEmptyLabelAddr(%q) error must name empty label, got %v", c.addr, err)
		}
	}
}

// L1-F-08 机制单测：validateNoEmptyLabelURLHost（订阅源保存面与
// github_proxy_url 配置键共用谓词）。
func TestFinalL1ValidateNoEmptyLabelURLHost(t *testing.T) {
	cases := []struct {
		raw     string
		wantErr bool
	}{
		{"https://a..b/sub", true},
		{"https://a...b/x", true},
		{"http://a..b:8080/x", true},
		{"https://example.com/x", false},
		{"https://203.0.113.9/x", false},
		{"https://user@example.com/x", false}, // userinfo 不影响 hostname
		{"", false},           // 空值由调用方另行处理
		{"not a url", false},  // 解析失败不由本谓词拒
		{"https://", false},    // 无 hostname，不由本谓词拒
	}
	for _, c := range cases {
		err := validateNoEmptyLabelURLHost(c.raw)
		if c.wantErr && err == nil {
			t.Errorf("validateNoEmptyLabelURLHost(%q) must error", c.raw)
		}
		if !c.wantErr && err != nil {
			t.Errorf("validateNoEmptyLabelURLHost(%q) must pass, got %v", c.raw, err)
		}
	}
}

// L1-F-06：管理员创建转发，目标 `a..b:80` 必须被拒（修复前：管理员面
// 不调 IsSafeRemoteAddr，直接创建成功 code=0）。
func TestFinalL1ForwardCreateAdminRejectsEmptyLabelTarget(t *testing.T) {
	h, tunnelID := setupRealmStrategyFixture(t)

	code, msg := callForwardCreate(t, h, map[string]interface{}{
		"tunnelId": tunnelID, "name": "empty-label-single",
		"remoteAddr": "a..b:80",
		"inPort": 30009, "strategy": "fifo",
	})
	if code == 0 {
		t.Fatalf("L1-F-06: admin forwardCreate remoteAddr=a..b:80 must be rejected, got success")
	}
	if !strings.Contains(msg, "空标签") {
		t.Fatalf("L1-F-06: rejection must name empty label, msg=%s", msg)
	}

	// `a...b` 同样拒。
	code, msg = callForwardCreate(t, h, map[string]interface{}{
		"tunnelId": tunnelID, "name": "empty-label-triple",
		"remoteAddr": "a...b:443",
		"inPort": 30010, "strategy": "fifo",
	})
	if code == 0 {
		t.Fatalf("L1-F-06: admin forwardCreate remoteAddr=a...b:443 must be rejected, got success")
	}
	if !strings.Contains(msg, "空标签") {
		t.Fatalf("L1-F-06: rejection must name empty label, msg=%s", msg)
	}

	// 多目标 round：唯一拒因应为空标签（策略本身合法）。
	code, msg = callForwardCreate(t, h, map[string]interface{}{
		"tunnelId": tunnelID, "name": "empty-label-multi",
		"remoteAddr": "203.0.113.5:443,a..b:80",
		"inPort": 30011, "strategy": "round",
	})
	if code == 0 {
		t.Fatalf("L1-F-06: admin forwardCreate multi-target with a..b must be rejected, got success")
	}
	if !strings.Contains(msg, "空标签") {
		t.Fatalf("L1-F-06: rejection must name empty label, msg=%s", msg)
	}
}

// L1-F-06（realm 校验器）：validateRealmForwardStrategy 经手 forward
// 目标 host，同口径补语法层空标签判；非 realm 隧道不由它管（gost 面
// 由调用方 helper 覆盖），须保持放行。
func TestFinalL1RealmStrategyRejectsEmptyLabel(t *testing.T) {
	realmTunnel := &tunnelRecord{Engine: "realm"}
	gostTunnel := &tunnelRecord{Engine: ""}

	if err := validateRealmForwardStrategy(realmTunnel, "a..b:80", "round"); err == nil {
		t.Fatal("L1-F-06: realm forward target a..b:80 must be rejected")
	} else if !strings.Contains(err.Error(), "空标签") {
		t.Fatalf("L1-F-06: rejection must name empty label, err=%v", err)
	}
	if err := validateRealmForwardStrategy(realmTunnel, "a...b:443", "hash"); err == nil {
		t.Fatal("L1-F-06: realm forward target a...b:443 must be rejected")
	}
	if err := validateRealmForwardStrategy(realmTunnel, "203.0.113.5:443,a..b:80", "round"); err == nil {
		t.Fatal("L1-F-06: realm multi-target with a..b must be rejected")
	}
	if err := validateRealmForwardStrategy(realmTunnel, "example.com:80", "round"); err != nil {
		t.Fatalf("L1-F-06: valid realm target must pass: %v", err)
	}
	if err := validateRealmForwardStrategy(realmTunnel, "203.0.113.9:80", "round"); err != nil {
		t.Fatalf("L1-F-06: IP literal realm target must pass: %v", err)
	}
	if err := validateRealmForwardStrategy(gostTunnel, "a..b:80", "fifo"); err != nil {
		t.Fatalf("L1-F-06: gost tunnel must not be gated by realm validator: %v", err)
	}
}

// L1-F-07 / L1-F-08：normalizeAndValidateConfigValue 对 panel_domain /
// ip / github_proxy_url 三键补空标签判（修复前：default 直通原值）。
func TestFinalL1NormalizeConfigRejectsEmptyLabel(t *testing.T) {
	cases := []struct {
		key, value string
		wantErr    bool
	}{
		// panel_domain（裸域名）
		{"panel_domain", "a..b", true},
		{"panel_domain", "a...b", true},
		{"panel_domain", ".example.com", true},
		{"panel_domain", "example.com.", true},
		{"panel_domain", "example.com", false},
		{"panel_domain", "a.b", false},
		{"panel_domain", "", false}, // 空值走可清空语义，不在此拒
		// ip（domain:port 形态）
		{"ip", "a..b:443", true},
		{"ip", "a...b:443", true},
		{"ip", "a..b", true}, // 无端口也判 host
		{"ip", "example.com:443", false},
		{"ip", "203.0.113.9:443", false},
		{"ip", "[2001:db8::1]:443", false},
		// github_proxy_url（URL，L1-F-08 与 L1-F-07 同一函数一次写好）
		{"github_proxy_url", "https://a..b/x", true},
		{"github_proxy_url", "https://a...b/x", true},
		{"github_proxy_url", "https://example.com/x", false},
		{"github_proxy_url", "https://203.0.113.9/x", false},
		{"github_proxy_url", "", false}, // 空值走可清空语义
	}
	for _, c := range cases {
		got, err := normalizeAndValidateConfigValue(c.key, c.value)
		if c.wantErr {
			if err == nil {
				t.Errorf("L1-F-07/08: normalizeAndValidateConfigValue(%q,%q) must error, got value=%q", c.key, c.value, got)
			} else if !strings.Contains(err.Error(), "空标签") {
				t.Errorf("L1-F-07/08: error must name empty label, key=%q value=%q err=%v", c.key, c.value, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("L1-F-07/08: normalizeAndValidateConfigValue(%q,%q) must pass, got err=%v", c.key, c.value, err)
		}
	}
}

// L1-F-08：订阅源 URL 保存面 `https://a..b/...` 必须在保存期被拒，
// 而不是存入后等同步期才 fail-closed（修复前：保存成功、同步报错）。
func TestFinalL1RemoteSourceSaveRejectsEmptyLabelURL(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/remote-l1f08.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	body := `{"name":"坏订阅源","kind":"url","url":"https://a..b/sub","intervalHours":6}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/remote-source/save", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.remoteSourceSave(rec, req)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if out.Code == 0 {
		t.Fatalf("L1-F-08: remoteSourceSave url=https://a..b/sub must be rejected, got success")
	}
	if !strings.Contains(out.Msg, "空标签") {
		t.Fatalf("L1-F-08: rejection must name empty label, msg=%s", out.Msg)
	}
	// 坏值不得落库。
	if src, _ := r.GetRemoteSource(1); src != nil {
		t.Fatalf("L1-F-08: empty-label source must not be stored, got id=%d url=%q", src.ID, src.URL)
	}
}
