package contract_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/store/repo"
)

func TestSubscriptionContract(t *testing.T) {
	secret := "contract-jwt-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	landing := seedKernelNode(t, r, "landing")
	if err := r.DB().Exec(`UPDATE node SET server_ip = '198.51.100.9' WHERE id = ?`, landing).Error; err != nil {
		t.Fatal(err)
	}
	vless := seedInboundOnNode(t, router, r, admin, landing,
		`{"nodeId":`+jsonInt64(landing)+`,"kernelType":"xray","name":"东京 VLESS","protocol":"vless","port":443,"settings":{"flow":"xtls-rprx-vision"},"security":{"type":"reality"}}`)
	ss := seedInboundOnNode(t, router, r, admin, landing,
		`{"nodeId":`+jsonInt64(landing)+`,"kernelType":"xray","name":"香港 SS","protocol":"shadowsocks","port":8388}`)
	var ownerSettings struct {
		UUID string `json:"uuid"`
	}
	_ = json.Unmarshal([]byte(mustQueryString(t, r, `SELECT settings FROM node_inbound WHERE id = ?`, vless)), &ownerSettings)

	sub := func(path string) (int, string, string) {
		resp := getSub(t, router, path+"?format=base64", "")
		body, _ := base64.StdEncoding.DecodeString(resp.Body.String())
		return resp.Code, string(body), resp.Header().Get("Subscription-Userinfo")
	}

	// A default subscription (with its own credentials) always exists and is
	// what the old API returns.
	list := kernelContractPost(t, router, admin, "/api/v1/subscription/list", `{}`).Data.(map[string]interface{})
	items := list["items"].([]interface{})
	if len(items) != 1 || items[0].(map[string]interface{})["kind"] != "share" {
		t.Fatalf("default subscription: %v", list)
	}
	// 2.1.2：列表响应不再携带 regions / ruleGroups 选项清单（功能已删）。
	if _, ok := list["regions"]; ok {
		t.Fatalf("list must not carry the removed regions key: %v", list)
	}
	if _, ok := list["ruleGroups"]; ok {
		t.Fatalf("list must not carry the removed ruleGroups key: %v", list)
	}
	def := items[0].(map[string]interface{})
	info := kernelContractPost(t, router, admin, "/api/v1/inbound/sub/info", `{}`).Data.(map[string]interface{})
	if info["path"] != def["path"] || valueAsInt(def["nodeCount"]) != 2 {
		t.Fatalf("legacy info must point at the default subscription: %v vs %v", info, def)
	}

	t.Run("own subscription with scope", func(t *testing.T) {
		// 2.1.2：处理流水线（地区/国旗/改名/过滤/脚本）已整块删除，
		// 订阅选项只剩中转范围三档。两个自建入站都是直连端点：
		// direct 档全出、relay 档全滤、存量 relayIds 保存时折算。
		out := kernelContractPost(t, router, admin, "/api/v1/subscription/save",
			`{"name":"只要直连","sourceAll":true,"options":{"lines":"direct"}}`)
		if out.Code != 0 {
			t.Fatalf("save: %+v", out)
		}
		code, body, _ := sub(out.Data.(map[string]interface{})["path"].(string))
		if code != 200 || !strings.Contains(body, "vless://") || !strings.Contains(body, "ss://") {
			t.Fatalf("direct scope carries both own inbounds: %d %s", code, body)
		}
		out = kernelContractPost(t, router, admin, "/api/v1/subscription/save",
			`{"name":"存量折算","sourceAll":true,"options":{"relayIds":[0]}}`)
		if out.Code != 0 {
			t.Fatalf("legacy save: %+v", out)
		}
		if v := out.Data.(map[string]interface{})["options"].(map[string]interface{}); v["lines"] != "direct" {
			t.Fatalf("relayIds [0] must fold to lines=direct in the saved view: %v", v)
		}
		code, body, _ = sub(out.Data.(map[string]interface{})["path"].(string))
		if code != 200 || !strings.Contains(body, "vless://") {
			t.Fatalf("folded direct scope: %d %s", code, body)
		}
		out = kernelContractPost(t, router, admin, "/api/v1/subscription/save",
			`{"name":"只要中转","sourceAll":true,"options":{"lines":"relay"}}`)
		if out.Code != 0 {
			t.Fatalf("save relay scope: %+v", out)
		}
		code, body, _ = sub(out.Data.(map[string]interface{})["path"].(string))
		if code != 200 || strings.Contains(body, "vless://") || strings.Contains(body, "ss://") {
			t.Fatalf("relay scope must filter out direct inbounds: %d %s", code, body)
		}
		// 已删的处理键保存时被忽略（不再有校验面）：坏正则不再报错，
		// 订阅按原样节点下发。
		if out := kernelContractPost(t, router, admin, "/api/v1/subscription/save",
			`{"name":"旧键","sourceAll":true,"options":{"include":"/[/","regions":["🇭🇰 香港"]}}`); out.Code != 0 {
			t.Fatalf("removed option keys must be ignored on save: %+v", out)
		}
		// 预览：节点名原样（name == original），无处理改名、无丢弃。
		prev := kernelContractPost(t, router, admin, "/api/v1/subscription/preview",
			`{"sourceAll":true,"options":{}}`).Data.(map[string]interface{})
		pi := prev["items"].([]interface{})
		if len(pi) != 2 || valueAsInt(prev["dropped"]) != 0 {
			t.Fatalf("preview: %v", prev)
		}
		for _, it := range pi {
			m := it.(map[string]interface{})
			if m["name"] != m["original"] {
				t.Fatalf("preview must echo raw names: %v", m)
			}
		}
	})

	t.Run("share subscription lifecycle", func(t *testing.T) {
		if out := kernelContractPost(t, router, admin, "/api/v1/subscription/save", `{"name":"小明","kind":"share","sourceAll":false,"inboundIds":[]}`); out.Code == 0 {
			t.Fatal("picking nothing must be rejected")
		}
		// R1-C08（B10-002）：不存在的入站 ID 不再被静默丢弃——save
		// 整体报错。本用例旧版在此混入 999 并预期其被过滤，该口径
		// 已废止；拒绝行为由 c08 契约测试专门守
		// （TestC08SubscriptionSaveUnknownRejectedCompanionAnnotated）。
		out := kernelContractPost(t, router, admin, "/api/v1/subscription/save",
			`{"name":"小明","kind":"share","inboundIds":[`+jsonInt64(vless)+`,`+jsonInt64(ss)+`]}`)
		if out.Code != 0 {
			t.Fatalf("create: %+v", out)
		}
		view := out.Data.(map[string]interface{})
		id := int64(view["id"].(float64))
		path := view["path"].(string)
		if len(view["inboundIds"].([]interface{})) != 2 || len(view["sharedIds"].([]interface{})) != 1 || view["active"] != true {
			t.Fatalf("view: %v", view)
		}
		uuid := mustQueryString(t, r, `SELECT uuid FROM subscription WHERE id = ?`, id)
		code, body, hdr := sub(path)
		if code != 200 || !strings.Contains(body, "vless://"+uuid+"@") || strings.Contains(body, ownerSettings.UUID) ||
			!strings.Contains(hdr, "upload=0") {
			t.Fatalf("share uses its own uuid and hides traffic: %d %s %s", code, body, hdr)
		}
		if _, ownBody, _ := sub(def["path"].(string)); strings.Contains(ownBody, uuid) {
			t.Fatal("own subscription must not carry share credentials")
		}

		past := time.Now().Add(-time.Hour).UnixMilli()
		kernelContractPost(t, router, admin, "/api/v1/subscription/save",
			`{"id":`+jsonInt64(id)+`,"name":"小明","kind":"share","inboundIds":[`+jsonInt64(vless)+`],"expireTime":`+jsonInt64(past)+`}`)
		// E-05 (port351): an expired subscription no longer answers 403;
		// it renders 200 with only a reason placeholder node, so clients
		// replace the stale list and show why it stopped working. The
		// real credentials must not leak into that rendering.
		if code, body, _ := sub(path); code != 200 || strings.Contains(body, "vless://"+uuid+"@") {
			t.Fatalf("expired: %d %s", code, body)
		}
		future := time.Now().Add(48 * time.Hour).UnixMilli()
		kernelContractPost(t, router, admin, "/api/v1/subscription/save",
			`{"id":`+jsonInt64(id)+`,"name":"小明","kind":"share","inboundIds":[`+jsonInt64(vless)+`],"expireTime":`+jsonInt64(future)+`,"enabled":false}`)
		// E-05 (port351): disabled renders 200 + reason placeholder, not 403.
		if code, body, _ := sub(path); code != 200 || strings.Contains(body, "vless://"+uuid+"@") {
			t.Fatalf("disabled: %d %s", code, body)
		}
		kernelContractPost(t, router, admin, "/api/v1/subscription/save",
			`{"id":`+jsonInt64(id)+`,"name":"小明","kind":"share","inboundIds":[`+jsonInt64(vless)+`],"expireTime":`+jsonInt64(future)+`,"enabled":true}`)
		if code, _, hdr := sub(path); code != 200 || !strings.Contains(hdr, "expire="+jsonInt64(future/1000)) {
			t.Fatalf("re-enabled: %d %s", code, hdr)
		}

		// Traffic limit: reported in the header, cut off once reached.
		out = kernelContractPost(t, router, admin, "/api/v1/subscription/save",
			`{"id":`+jsonInt64(id)+`,"name":"小明","kind":"share","inboundIds":[`+jsonInt64(vless)+`],"expireTime":`+jsonInt64(future)+`,"trafficLimit":1000,"resetDay":5}`)
		if v := out.Data.(map[string]interface{}); out.Code != 0 || valueAsInt(v["trafficLimit"]) != 1000 || valueAsInt(v["resetDay"]) != 5 {
			t.Fatalf("limit save: %+v", out)
		}
		if out := kernelContractPost(t, router, admin, "/api/v1/subscription/save",
			`{"id":`+jsonInt64(id)+`,"name":"小明","kind":"share","inboundIds":[`+jsonInt64(vless)+`],"resetDay":31}`); out.Code == 0 {
			t.Fatal("reset day 31 must be rejected")
		}
		// FIX132-C（A3-04）：基线缺失只建基线。先以零值样本建基线，
		// 后续 200/500 才全额成为已计流量（本用例的原断言不变）。
		if err := r.ApplySubscriptionCounterSamples(landing, "e", []repo.SubscriptionCounterSample{
			{SubscriptionID: id, Direction: "up", Bytes: 0}, {SubscriptionID: id, Direction: "down", Bytes: 0}}, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
		if err := r.ApplySubscriptionCounterSamples(landing, "e", []repo.SubscriptionCounterSample{
			{SubscriptionID: id, Direction: "up", Bytes: 200}, {SubscriptionID: id, Direction: "down", Bytes: 500}}, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
		if code, _, hdr := sub(path); code != 200 || !strings.HasPrefix(hdr, "upload=200; download=500; total=1000;") {
			t.Fatalf("share traffic header: %d %s", code, hdr)
		}
		_ = r.ApplySubscriptionCounterSamples(landing, "e", []repo.SubscriptionCounterSample{{SubscriptionID: id, Direction: "down", Bytes: 900}}, time.Now().UnixMilli())
		// E-05 (port351): over the traffic limit renders 200 + reason
		// placeholder, not 403.
		if code, body, _ := sub(path); code != 200 || strings.Contains(body, "vless://"+uuid+"@") {
			t.Fatalf("over the limit: %d %s", code, body)
		}
		out = kernelContractPost(t, router, admin, "/api/v1/subscription/reset-traffic", `{"id":`+jsonInt64(id)+`}`)
		if v := out.Data.(map[string]interface{}); out.Code != 0 || valueAsInt(v["downloadBytes"]) != 0 || v["active"] != true {
			t.Fatalf("reset traffic: %+v", out)
		}
		if code, _, _ := sub(path); code != 200 {
			t.Fatalf("after reset: %d", code)
		}
		// The hourly history keeps the traffic a reset zeroes.
		for _, rg := range []string{"hour", "day", "month"} {
			out = kernelContractPost(t, router, admin, "/api/v1/subscription/traffic", `{"id":`+jsonInt64(id)+`,"range":"`+rg+`","tzOffset":480}`)
			v := out.Data.(map[string]interface{})
			points := v["points"].([]interface{})
			last := points[len(points)-1].(map[string]interface{})
			if out.Code != 0 || valueAsInt(v["up"]) != 200 || valueAsInt(v["down"]) != 900 || valueAsInt(last["down"]) != 900 {
				t.Fatalf("%s traffic: %+v", rg, out)
			}
		}

		out = kernelContractPost(t, router, admin, "/api/v1/subscription/reset", `{"id":`+jsonInt64(id)+`}`)
		newPath := out.Data.(map[string]interface{})["path"].(string)
		if code, _, _ := sub(path); code != http.StatusNotFound || newPath == path ||
			mustQueryString(t, r, `SELECT uuid FROM subscription WHERE id = ?`, id) == uuid {
			t.Fatal("reset must rotate link and credentials")
		}
		kernelContractPost(t, router, admin, "/api/v1/subscription/delete", `{"id":`+jsonInt64(id)+`}`)
		// 我方 F-058：节点离线时删除先落墓碑、硬删待凭据下发成功后才完成，
		// 故此处手动走完成路径（与后台重试同一函数）；流量时序须随硬删
		// 同一事务清零（上游为同步硬删，断言点位按我方语义调整）。
		if err := r.DeleteSubscriptionWithAssignments(id); err != nil {
			t.Fatal(err)
		}
		if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM subscription_traffic WHERE subscription_id = ?`, id); n != 0 {
			t.Fatalf("history of a deleted subscription must go, got %d rows", n)
		}
		if code, _, _ := sub(newPath); code != http.StatusNotFound {
			t.Fatal("deleted subscription link must stop working")
		}
	})
}
