package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// R2 K1 写面枚举复扫残差（与 B03-001/B09-004 同机制：字符串字段对
// 列宽无长度门，PG 吐原始落库错误、SQLite 静默存入）：
//   - Node.Name varchar(100) / InterfaceName varchar(200)：nodeCreate、
//     nodeUpdate 此前无校验；
//   - Tunnel.Name varchar(100)：tunnelCreate、tunnelUpdate 此前无校验；
//   - ServiceMonitor.Name varchar(100)：monitor create/update 此前无校验；
//   - ViteConfig.Name varchar(200)：config 两写入口此前无键长校验；
//   - Node.ExtraIPs：此前只剥离不校验（B04-001 同族），坏端口段被吞。
// 本文件逐面锁定：超长/坏值点名拒绝、不落库；合法值行为不变。

func TestR2K1EnumNodeNameAndExtraIPsBounded(t *testing.T) {
	r, h := r2b2Open(t)
	name101 := strings.Repeat("n", 101)

	payload, _ := json.Marshal(map[string]interface{}{
		"name": name101, "serverIp": "198.51.100.77", "forwardMode": "agent",
	})
	if out := pcFixCreateNode(t, h, string(payload)); out.Code == 0 {
		t.Fatal("nodeCreate accepted 101-rune name")
	}
	payload, _ = json.Marshal(map[string]interface{}{
		"name": "k1-enum-iface", "serverIp": "198.51.100.77",
		"interfaceName": strings.Repeat("i", 201), "forwardMode": "agent",
	})
	if out := pcFixCreateNode(t, h, string(payload)); out.Code == 0 {
		t.Fatal("nodeCreate accepted 201-rune interfaceName")
	}
	payload, _ = json.Marshal(map[string]interface{}{
		"name": "k1-enum-extra", "serverIp": "198.51.100.77",
		"extraIPs": "example.com:abc", "forwardMode": "agent",
	})
	if out := pcFixCreateNode(t, h, string(payload)); out.Code == 0 {
		t.Fatal("nodeCreate accepted extraIPs with non-numeric port")
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node`); n != 0 {
		t.Fatalf("rejected nodeCreate calls persisted %d rows", n)
	}

	// 对照：合法 extraIPs 单端口仍剥离落库。
	payload, _ = json.Marshal(map[string]interface{}{
		"name": "k1-enum-good", "serverIp": "198.51.100.77",
		"extraIPs": "198.51.100.77:8443,2001:db8::1", "forwardMode": "agent",
	})
	if out := pcFixCreateNode(t, h, string(payload)); out.Code != 0 {
		t.Fatalf("nodeCreate rejected valid extraIPs: %+v", out)
	}
	_, _, _, _, _, extra := pcFixNodeAddrs(t, r, "k1-enum-good")
	if extra != "198.51.100.77,2001:db8::1" {
		t.Fatalf("stored extra_ips = %q, want stripped list", extra)
	}
}

func TestR2K1EnumTunnelNameBounded(t *testing.T) {
	fx := pcIntSetup(t)
	name101 := strings.Repeat("t", 101)
	out := pcIntCreateAttempt(t, fx, name101, "")
	if out.Code == 0 {
		t.Fatal("tunnelCreate accepted 101-rune name")
	}
	if n := pcIntTunnelCount(t, fx, name101); n != 0 {
		t.Fatalf("rejected tunnelCreate persisted %d rows", n)
	}
}

func TestR2K1EnumMonitorNameAndConfigKeyBounded(t *testing.T) {
	r, h, adminID := k1AdminFixture(t)
	call := func(fn func(http.ResponseWriter, *http.Request), path, body string) envelope {
		w := httptest.NewRecorder()
		fn(w, claimsCtxReq(http.MethodPost, path, "", body, adminID, 0))
		return decodeEnvelope(t, w)
	}

	name101 := strings.Repeat("m", 101)
	body := fmt.Sprintf(`{"name":%q,"type":"tcp","target":"8.8.8.8:53","intervalSec":60,"timeoutSec":5}`, name101)
	if env := call(h.monitorServiceCreate, "/api/v1/monitor/services/create", body); env.Code == 0 {
		t.Fatal("monitorServiceCreate accepted 101-rune name")
	}
	if monitors, err := r.ListServiceMonitors(); err != nil || len(monitors) != 0 {
		t.Fatalf("rejected monitor create persisted: %v / %v", monitors, err)
	}

	key201 := strings.Repeat("k", 201)
	body = fmt.Sprintf(`{"name":%q,"value":"v"}`, key201)
	if env := call(h.updateSingleConfig, "/api/v1/config/update-single", body); env.Code == 0 {
		t.Fatal("updateSingleConfig accepted 201-rune key")
	}
	if cfg, _ := r.GetConfigByName(key201); cfg != nil {
		t.Fatalf("rejected config key persisted: %+v", cfg)
	}
}
