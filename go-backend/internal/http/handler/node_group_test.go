package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// groupFixture builds one node with four enabled trojan inbounds and
// returns the handler, the repo and the inbound ids by name.
func groupFixture(t *testing.T) (*Handler, *repo.Repository, map[string]int64) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/groups.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, port, created_time, status) VALUES('n', 's', '203.0.113.1', '1000-2000', ?, 1)`, now).Error; err != nil {
		t.Fatal(err)
	}
	nodeID := mustLastInsertID(t, r, "node")
	if err := r.DB().Exec(`INSERT INTO node_kernel(node_id, kernel_type, version, status, created_time, updated_time) VALUES(?, 'xray', 'latest', 'pending', ?, ?)`, nodeID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	kernelID := mustLastInsertID(t, r, "kernel")
	ids := map[string]int64{}
	for i, name := range []string{"hk-1", "hk-2", "jp-1", "us-1"} {
		if err := r.DB().Exec(`INSERT INTO node_inbound(kernel_id, node_id, name, protocol, listen, port, settings, enabled, created_time, updated_time)
			VALUES(?, ?, ?, 'trojan', '::', ?, '{"password":"owner-pw"}', 1, ?, ?)`, kernelID, nodeID, name, 10443+i, now, now).Error; err != nil {
			t.Fatal(err)
		}
		ids[name] = mustLastInsertID(t, r, "inbound "+name)
	}
	return h, r, ids
}

func saveGroup(t *testing.T, r *repo.Repository, name string, inboundIDs []int64, options string) *model.NodeGroup {
	t.Helper()
	g := &model.NodeGroup{Name: name, InboundIDs: joinIDs(inboundIDs), Options: options, CreatedTime: time.Now().UnixMilli(), UpdatedTime: time.Now().UnixMilli()}
	if err := r.SaveNodeGroup(g); err != nil {
		t.Fatal(err)
	}
	return g
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

func saveShareSub(t *testing.T, r *repo.Repository, name, token string, groupIDs []int64, options string) *model.Subscription {
	t.Helper()
	return createShareSub(t, r, &model.Subscription{
		Name: name, Kind: model.SubscriptionShare, Token: token, UUID: "uuid-" + name, Password: "pw-" + name,
		GroupIDs: formatOrderedIDList(groupIDs), Options: options, Enabled: 1,
	})
}

// createShareSub persists a fully-formed share subscription in one
// create: traffic columns are only written on create (updates omit
// them by design), so fixtures set every field up front.
func createShareSub(t *testing.T, r *repo.Repository, s *model.Subscription) *model.Subscription {
	t.Helper()
	now := time.Now().UnixMilli()
	if s.CreatedTime == 0 {
		s.CreatedTime = now
	}
	s.UpdatedTime = now
	if err := r.SaveSubscription(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestNodeGroupsSharedAndOrdered: two subscriptions share one group
// (both render its nodes), and a subscription listing groups [G2, G1]
// splices G2's nodes before G1's.
func TestNodeGroupsSharedAndOrdered(t *testing.T) {
	h, r, ids := groupFixture(t)
	g1 := saveGroup(t, r, "HK组", []int64{ids["hk-1"], ids["hk-2"]}, `{"prefix":"[G1] "}`)
	g2 := saveGroup(t, r, "JP组", []int64{ids["jp-1"]}, `{"prefix":"[G2] "}`)

	subA := saveShareSub(t, r, "A", strings.Repeat("a", 32), []int64{g2.ID, g1.ID}, "")
	subB := saveShareSub(t, r, "B", strings.Repeat("b", 32), []int64{g1.ID}, "")

	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	namesOf := func(s *model.Subscription) []string {
		var out []string
		for _, e := range h.subRenderEndpoints(s, rows) {
			out = append(out, e.Name)
		}
		return out
	}
	gotA := namesOf(subA)
	wantA := []string{"[G2] jp-1", "[G1] hk-1", "[G1] hk-2"}
	if strings.Join(gotA, ",") != strings.Join(wantA, ",") {
		t.Fatalf("sub A splice order: got %v want %v", gotA, wantA)
	}
	gotB := namesOf(subB)
	wantB := []string{"[G1] hk-1", "[G1] hk-2"}
	if strings.Join(gotB, ",") != strings.Join(wantB, ",") {
		t.Fatalf("sub B shared group: got %v want %v", gotB, wantB)
	}

	// Editing the shared group once changes both subscriptions.
	g1.Options = `{"prefix":"[HK] "}`
	if err := r.SaveNodeGroup(g1); err != nil {
		t.Fatal(err)
	}
	if got := namesOf(subB); !strings.HasPrefix(got[0], "[HK] ") {
		t.Fatalf("group edit must propagate to sharing subscriptions: %v", got)
	}
	if got := namesOf(subA); !strings.HasPrefix(got[1], "[HK] ") {
		t.Fatalf("group edit must propagate to sub A too: %v", got)
	}

	// Deleting the group leaves the subscriptions rendering without it.
	if err := r.DeleteNodeGroup(g1.ID); err != nil {
		t.Fatal(err)
	}
	if got := namesOf(subB); len(got) != 0 {
		t.Fatalf("deleted group must drop out of renders: %v", got)
	}
	if got := namesOf(subA); len(got) != 1 || got[0] != "[G2] jp-1" {
		t.Fatalf("remaining group must keep rendering: %v", got)
	}
}

// TestGroupInboundIDsFeedCredentialPush: a subscription whose only
// sources are a group's inbounds must have its credentials accepted on
// those inbounds (the raw demand map the kernel payload is built from).
func TestGroupInboundIDsFeedCredentialPush(t *testing.T) {
	h, r, ids := groupFixture(t)
	g := saveGroup(t, r, "HK组", []int64{ids["hk-1"]}, "")
	s := saveShareSub(t, r, "G", strings.Repeat("c", 32), []int64{g.ID}, "")

	byInbound := h.rawActiveUsersByInbound(time.Now())
	carries := func(inboundID int64) bool {
		for _, u := range byInbound[inboundID] {
			if u.ID == s.ID {
				return true
			}
		}
		return false
	}
	if !carries(ids["hk-1"]) {
		t.Fatalf("group inbound must carry the subscription's credentials: %v", byInbound)
	}
	if carries(ids["jp-1"]) {
		t.Fatalf("unrelated inbound must not carry the subscription's credentials: %v", byInbound)
	}
}

// TestNodeGroupHandlers exercises the five group routes end to end.
func TestNodeGroupHandlers(t *testing.T) {
	h, r, ids := groupFixture(t)
	_ = r
	call := func(path string, body string) map[string]interface{} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		switch path {
		case "/api/v1/subscription/group/list":
			h.nodeGroupList(rec, req)
		case "/api/v1/subscription/group/save":
			h.nodeGroupSave(rec, req)
		case "/api/v1/subscription/group/delete":
			h.nodeGroupDelete(rec, req)
		case "/api/v1/subscription/group/order":
			h.nodeGroupOrder(rec, req)
		case "/api/v1/subscription/group/preview":
			h.nodeGroupPreview(rec, req)
		}
		var env struct {
			Code int                    `json:"code"`
			Msg  string                 `json:"msg"`
			Data map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: bad envelope: %v (%s)", path, err, rec.Body.String())
		}
		if env.Code != 0 {
			t.Fatalf("%s: code=%d msg=%s", path, env.Code, env.Msg)
		}
		return env.Data
	}

	// Create two groups through the save route.
	call("/api/v1/subscription/group/save", `{"name":"一组","inboundIds":[`+strconv.FormatInt(ids["hk-1"], 10)+`],"options":{"prefix":"[1] "}}`)
	call("/api/v1/subscription/group/save", `{"name":"二组","inboundIds":[`+strconv.FormatInt(ids["jp-1"], 10)+`],"options":{}}`)
	list := call("/api/v1/subscription/group/list", `{}`)
	items, _ := list["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("group list: %v", list)
	}
	first, _ := items[0].(map[string]interface{})
	if first["name"] != "一组" || first["nodeCount"].(float64) != 1 {
		t.Fatalf("group view: %v", first)
	}
	g1ID := int64(first["id"].(float64))
	second, _ := items[1].(map[string]interface{})
	g2ID := int64(second["id"].(float64))

	// Preview runs the pipeline without saving.
	preview := call("/api/v1/subscription/group/preview", `{"name":"预览","inboundIds":[`+strconv.FormatInt(ids["hk-1"], 10)+`,`+strconv.FormatInt(ids["hk-2"], 10)+`],"options":{"exclude":"hk-2"}}`)
	pItems, _ := preview["items"].([]interface{})
	if len(pItems) != 1 || preview["total"].(float64) != 2 {
		t.Fatalf("group preview: %v", preview)
	}

	// A subscription referencing the group shows up in its view.
	sub := saveShareSub(t, r, "引用方", strings.Repeat("d", 32), []int64{g1ID}, "")
	_ = sub
	list = call("/api/v1/subscription/group/list", `{}`)
	items, _ = list["items"].([]interface{})
	first, _ = items[0].(map[string]interface{})
	subs, _ := first["subscriptions"].([]interface{})
	if len(subs) != 1 {
		t.Fatalf("group view must list referencing subscriptions: %v", first)
	}

	// Reorder: 二组 first.
	call("/api/v1/subscription/group/order", `{"ids":[`+strconv.FormatInt(g2ID, 10)+`,`+strconv.FormatInt(g1ID, 10)+`]}`)
	list = call("/api/v1/subscription/group/list", `{}`)
	items, _ = list["items"].([]interface{})
	if items[0].(map[string]interface{})["name"] != "二组" {
		t.Fatalf("group order not applied: %v", items[0])
	}

	// Unknown inbound ids are refused, not silently dropped.
	bad := httptest.NewRequest(http.MethodPost, "/api/v1/subscription/group/save", strings.NewReader(`{"name":"坏组","inboundIds":[999999]}`))
	rec := httptest.NewRecorder()
	h.nodeGroupSave(rec, bad)
	if !strings.Contains(rec.Body.String(), "入站不存在") {
		t.Fatalf("unknown inbound must be refused: %s", rec.Body.String())
	}

	// Delete.
	call("/api/v1/subscription/group/delete", `{"id":`+strconv.FormatInt(g1ID, 10)+`}`)
	list = call("/api/v1/subscription/group/list", `{}`)
	items, _ = list["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("after delete: %v", list)
	}
}

// fetchSub runs openSubscription for a token and returns status + body.
func fetchSub(t *testing.T, h *Handler, token, ua string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/open_api/sub/"+token, nil)
	req.Header.Set("User-Agent", ua)
	rec := httptest.NewRecorder()
	h.openSubscription(rec, req)
	return rec.Code, rec.Body.String()
}

// TestOpenSubscriptionInfoNodes: with the info-nodes switch on, an
// active subscription's fetch leads with the expiry and traffic
// placeholder nodes; without it, no placeholders appear.
func TestOpenSubscriptionInfoNodes(t *testing.T) {
	h, r, ids := groupFixture(t)
	_ = ids
	expire := time.Date(2027, 3, 4, 0, 0, 0, 0, time.Local).UnixMilli()
	s := createShareSub(t, r, &model.Subscription{
		Name: "信息", Kind: model.SubscriptionShare, Token: strings.Repeat("e", 32), UUID: "uuid-info", Password: "pw-info",
		SourceAll: 1, Options: `{"infoNodes":true}`, Enabled: 1, ExpireTime: expire,
		TrafficLimit: 10 * 1024 * 1024 * 1024, UploadBytes: 1024 * 1024 * 1024, DownloadBytes: 2 * 1024 * 1024 * 1024,
	})
	code, body := fetchSub(t, h, s.Token, "clash")
	if code != http.StatusOK {
		t.Fatalf("fetch: %d %s", code, body)
	}
	expireNode := "⏳ 到期 2027-03-04"
	trafficNode := "📊 剩余 7.00GB / 10.00GB"
	iExpire, iTraffic, iNode := strings.Index(body, expireNode), strings.Index(body, trafficNode), strings.Index(body, "hk-1")
	if iExpire < 0 || iTraffic < 0 || iNode < 0 {
		t.Fatalf("info nodes missing: expire=%d traffic=%d node=%d\n%s", iExpire, iTraffic, iNode, body)
	}
	if !(iExpire < iNode && iTraffic < iNode) {
		t.Fatalf("info nodes must lead the list:\n%s", body)
	}

	plain := createShareSub(t, r, &model.Subscription{
		Name: "无信息", Kind: model.SubscriptionShare, Token: strings.Repeat("f", 32), UUID: "uuid-plain", Password: "pw-plain",
		SourceAll: 1, Enabled: 1,
	})
	code, body = fetchSub(t, h, plain.Token, "clash")
	if code != http.StatusOK || strings.Contains(body, "⏳") || strings.Contains(body, "📊") {
		t.Fatalf("switch off must render no placeholders: %d\n%s", code, body)
	}
}

// TestOpenSubscriptionInactiveReasonNodes: a disabled, expired or
// over-quota subscription answers 200 with a single reason placeholder
// instead of the old 403, so clients replace their stale node list.
func TestOpenSubscriptionInactiveReasonNodes(t *testing.T) {
	h, r, ids := groupFixture(t)
	_ = ids
	now := time.Now().UnixMilli()

	disabled := createShareSub(t, r, &model.Subscription{
		Name: "停用", Kind: model.SubscriptionShare, Token: strings.Repeat("1", 32), UUID: "uuid-off", Password: "pw-off",
		SourceAll: 1, Enabled: 0,
	})
	expired := createShareSub(t, r, &model.Subscription{
		Name: "过期", Kind: model.SubscriptionShare, Token: strings.Repeat("2", 32), UUID: "uuid-exp", Password: "pw-exp",
		SourceAll: 1, Enabled: 1, ExpireTime: now - 1000,
	})
	over := createShareSub(t, r, &model.Subscription{
		Name: "超量", Kind: model.SubscriptionShare, Token: strings.Repeat("3", 32), UUID: "uuid-over", Password: "pw-over",
		SourceAll: 1, Enabled: 1, TrafficLimit: 100, UploadBytes: 200,
	})

	cases := []struct {
		token  string
		reason string
	}{
		{disabled.Token, "⚠️ 订阅已停用，请联系管理员"},
		{expired.Token, "⚠️ 订阅已过期，请联系管理员"},
		{over.Token, "⚠️ 流量已用完，请联系管理员"},
	}
	for _, c := range cases {
		code, body := fetchSub(t, h, c.token, "clash")
		if code != http.StatusOK {
			t.Fatalf("%s: status %d (body %s)", c.reason, code, body)
		}
		if !strings.Contains(body, c.reason) {
			t.Fatalf("reason node missing:\n%s", body)
		}
		if strings.Contains(body, "hk-1") {
			t.Fatalf("stopped subscription must not serve real nodes:\n%s", body)
		}
		// Base64 (link-list) clients get the reason as a placeholder
		// ss link too.
		code, body = fetchSub(t, h, c.token, "v2rayN")
		if code != http.StatusOK {
			t.Fatalf("%s base64: status %d", c.reason, code)
		}
		raw, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			t.Fatalf("base64 body: %v", err)
		}
		if !strings.Contains(string(raw), "127.0.0.1:1") {
			t.Fatalf("base64 reason placeholder missing: %s", raw)
		}
	}
}
