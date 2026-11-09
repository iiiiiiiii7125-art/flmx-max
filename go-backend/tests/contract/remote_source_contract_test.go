package contract_test

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"go-backend/internal/auth"
)

func ssLink(host, port, name string) string {
	cred := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:pw"))
	return "ss://" + cred + "@" + host + ":" + port + "#" + name
}

func TestRemoteSourceContract(t *testing.T) {
	// 订阅源是本机 httptest 服务（127.0.0.1），须经 allowlist 放行私网抓取；
	// 产品默认拒绝未 allowlist 的私网地址（import.md 明文行为），契约侧显式放行。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	secret := "contract-jwt-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)

	var mu sync.Mutex
	body := strings.Join([]string{ssLink("203.0.113.1", "8388", "香港 01"), ssLink("203.0.113.2", "8388", "日本 01")}, "\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Subscription-Userinfo", "upload=1; download=2; total=3; expire=4")
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(body))))
	}))
	defer srv.Close()
	serve := func(lines ...string) {
		mu.Lock()
		body = strings.Join(lines, "\n")
		mu.Unlock()
	}

	if out := kernelContractPost(t, router, admin, "/api/v1/subscription/source/save", `{"name":"机场","url":"ftp://x"}`); out.Code == 0 {
		t.Fatal("non-http url must be rejected")
	}
	out := kernelContractPost(t, router, admin, "/api/v1/subscription/source/save",
		`{"name":"机场","url":"`+srv.URL+`","intervalHours":12}`)
	data := out.Data.(map[string]interface{})
	src := data["source"].(map[string]interface{})
	srcID := int64(src["id"].(float64))
	if out.Code != 0 || valueAsInt(src["nodeCount"]) != 2 || src["userinfo"] != "upload=1; download=2; total=3; expire=4" {
		t.Fatalf("create: %+v", out)
	}
	hk := mustQueryInt64(t, r, `SELECT id FROM node_inbound WHERE name = '香港 01' AND source_id = ?`, srcID)

	// Inbound list names the source; a subscription can take the whole source.
	list := kernelContractPost(t, router, admin, "/api/v1/inbound/list", `{}`).Data.([]interface{})
	if len(list) != 2 || list[0].(map[string]interface{})["sourceName"] != "机场" {
		t.Fatalf("inbound list: %v", list)
	}
	sub := kernelContractPost(t, router, admin, "/api/v1/subscription/save",
		`{"name":"合并","sourceAll":false,"inboundIds":[],"sourceIds":[`+jsonInt64(srcID)+`]}`)
	subView := sub.Data.(map[string]interface{})
	if sub.Code != 0 || valueAsInt(subView["nodeCount"]) != 2 || len(subView["inboundIds"].([]interface{})) != 0 {
		t.Fatalf("subscription from source: %+v", sub)
	}

	// The provider moves 香港 01, drops 日本 01 and adds 美国 01.
	serve(ssLink("203.0.113.9", "8388", "香港 01"), ssLink("203.0.113.3", "443", "美国 01"))
	out = kernelContractPost(t, router, admin, "/api/v1/subscription/source/refresh", `{"id":`+jsonInt64(srcID)+`}`)
	res := out.Data.(map[string]interface{})["result"].(map[string]interface{})
	if out.Code != 0 || valueAsInt(res["added"]) != 1 || valueAsInt(res["updated"]) != 1 || valueAsInt(res["removed"]) != 1 {
		t.Fatalf("refresh: %+v", out)
	}
	if got := mustQueryString(t, r, `SELECT listen FROM node_inbound WHERE id = ?`, hk); got != "203.0.113.9" {
		t.Fatalf("moved node must keep its id and get the new address, got %q", got)
	}
	subs := kernelContractPost(t, router, admin, "/api/v1/subscription/list", `{}`).Data.(map[string]interface{})["items"].([]interface{})
	for _, it := range subs {
		if v := it.(map[string]interface{}); v["name"] == "合并" && valueAsInt(v["nodeCount"]) != 2 {
			t.Fatalf("new source nodes follow into the subscription: %v", v)
		}
	}

	// An empty answer keeps the nodes and records the error.
	serve("")
	if out := kernelContractPost(t, router, admin, "/api/v1/subscription/source/refresh", `{"id":`+jsonInt64(srcID)+`}`); out.Code == 0 {
		t.Fatal("empty subscription must fail")
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node_inbound WHERE source_id = ?`, srcID); n != 2 {
		t.Fatalf("nodes kept on a failed refresh, got %d", n)
	}
	src = kernelContractPost(t, router, admin, "/api/v1/subscription/source/list", `{}`).Data.([]interface{})[0].(map[string]interface{})
	if src["lastError"] == "" {
		t.Fatal("refresh error must be recorded")
	}

	if out := kernelContractPost(t, router, admin, "/api/v1/subscription/source/delete", `{"id":`+jsonInt64(srcID)+`,"keepNodes":true}`); out.Code != 0 {
		t.Fatalf("delete: %+v", out)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node_inbound WHERE source_id = 0`); n != 2 {
		t.Fatalf("keepNodes leaves plain imports, got %d", n)
	}
}

// TestFileSourceContract：文件上传的订阅源（role 标记保留）经
// sourceIds 整源加入订阅，节点以原样明文节点下发。
// （2.1.2：原「按源做链式中转」契约随订阅链式删除同去——源不再
// 自动播种到任何链式侧，chainRelaySources 等键保存时被忽略。）
func TestFileSourceContract(t *testing.T) {
	secret := "contract-jwt-secret"
	router, _ := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin_user", 0, secret)
	save := func(body string) map[string]interface{} {
		out := kernelContractPost(t, router, admin, "/api/v1/subscription/source/save", body)
		if out.Code != 0 {
			t.Fatalf("save %s: %+v", body, out)
		}
		return out.Data.(map[string]interface{})["source"].(map[string]interface{})
	}
	quote := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "\n", `\n`) }

	if out := kernelContractPost(t, router, admin, "/api/v1/subscription/source/save", `{"name":"机场","kind":"file"}`); out.Code == 0 {
		t.Fatal("a file source needs a file")
	}
	airport := save(`{"name":"机场","kind":"file","role":"relay","url":"https://ignored.test","intervalHours":6,"content":"` +
		quote(ssLink("203.0.113.1", "8388", "香港 01")+"\n"+ssLink("203.0.113.2", "8388", "日本 01")) + `"}`)
	if airport["kind"] != "file" || airport["role"] != "relay" || airport["url"] != "" || valueAsInt(airport["intervalHours"]) != 0 || valueAsInt(airport["nodeCount"]) != 2 {
		t.Fatalf("file source: %v", airport)
	}
	airportID := jsonInt64(int64(airport["id"].(float64)))
	landing := save(`{"name":"落地","kind":"file","role":"landing","content":"` + quote(ssLink("198.51.100.1", "8388", "美国 落地")) + `"}`)
	landingID := jsonInt64(int64(landing["id"].(float64)))
	if out := kernelContractPost(t, router, admin, "/api/v1/subscription/source/refresh", `{"id":`+airportID+`}`); out.Code == 0 {
		t.Fatal("a file source is not refreshed from a URL")
	}

	sub := kernelContractPost(t, router, admin, "/api/v1/subscription/save", `{"name":"源订阅","sourceAll":false,"inboundIds":[],"sourceIds":[`+airportID+`,`+landingID+`]}`)
	if sub.Code != 0 || valueAsInt(sub.Data.(map[string]interface{})["nodeCount"]) != 3 {
		t.Fatalf("sources join the subscription: %+v", sub)
	}
	path := sub.Data.(map[string]interface{})["path"].(string)
	check := func(names ...string) {
		t.Helper()
		var cfg struct {
			Proxies []map[string]interface{} `yaml:"proxies"`
			Groups  []struct {
				Name    string   `yaml:"name"`
				Proxies []string `yaml:"proxies"`
			} `yaml:"proxy-groups"`
		}
		if err := yaml.Unmarshal(getSub(t, router, path+"?format=clash", "").Body.Bytes(), &cfg); err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, p := range cfg.Proxies {
			got[p["name"].(string)] = true
			if p["dialer-proxy"] != nil {
				t.Fatalf("plain rendering must not chain: %v", p)
			}
		}
		for _, n := range names {
			if !got[n] {
				t.Fatalf("node %q missing from plain profile: %v", n, got)
			}
		}
		for _, g := range cfg.Groups {
			if g.Name == "中转节点" || g.Name == "落地节点" {
				t.Fatalf("no chain groups in plain rendering: %v", cfg.Groups)
			}
		}
	}
	check("香港 01", "日本 01", "美国 落地")
	// The airport renames its node; the new name renders as-is.
	save(`{"id":` + airportID + `,"name":"机场","kind":"file","role":"relay","content":"` +
		quote(ssLink("203.0.113.1", "8388", "香港 01 2x")+"\n"+ssLink("203.0.113.2", "8388", "日本 01")) + `"}`)
	check("香港 01 2x", "日本 01", "美国 落地")
}
