package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R2-02 防静默收尾（用户 2026-11-11 裁定：只留防静默提示，其余不
// 做）：relayOnly 导入节点不产端点、订阅行为与范围语义逐字不变，
// 只把「静默」改成「出声」——① 订阅保存回执 warning 逐个点名无在
// 册中转端点的纯中转导入节点；② 装配每次跳过 relayOnly／只监听
// 本机行时落日志点名；③ 纯中转源的保存/同步回执带源级提示。本
// 文件逐面钉死三面与行为不变对照。

const (
	r202LinkROa = "vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=none#ro-node-a"
	r202LinkROb = "vless://11111111-2222-3333-4444-555555555555@203.0.113.11:443?security=none#ro-node-b"
	r202LinkOK  = "vless://11111111-2222-3333-4444-555555555555@203.0.113.20:443?security=none#ok-node"
)

func r202Setup(t *testing.T) (*repo.Repository, *Handler) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "r202.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, New(r, "unit-test-secret")
}

func r202ImportSource(t *testing.T, r *repo.Repository, h *Handler, name string, relayOnly bool, content string) *model.RemoteSource {
	t.Helper()
	now := time.Now().UnixMilli()
	src := &model.RemoteSource{
		Name: name, Kind: model.RemoteSourceFile, RelayOnly: boolToInt(relayOnly),
		Enabled: 1, CreatedTime: now, UpdatedTime: now,
	}
	if err := r.SaveRemoteSource(src); err != nil {
		t.Fatalf("save source %s: %v", name, err)
	}
	if _, err := h.syncRemoteSource(context.Background(), src, content); err != nil {
		t.Fatalf("sync source %s: %v", name, err)
	}
	return src
}

func r202RowsOf(t *testing.T, r *repo.Repository, sourceID int64) []model.NodeInbound {
	t.Helper()
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatalf("list inbounds: %v", err)
	}
	var out []model.NodeInbound
	for _, row := range rows {
		if row.SourceID == sourceID {
			out = append(out, row)
		}
	}
	return out
}

func r202CaptureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	fn()
	return buf.String()
}

// 面 ①＋行为不变：四种选择形态下回执点名集合精确（源选/显式选/
// 全部），正常节点恒不点名；relayOnly 订阅装配结果仍为空、正常
// 订阅仍为 1 个直连端点（行为逐字不变）。
func TestR202SaveReceiptNamesRelayOnlySelection(t *testing.T) {
	r, h := r202Setup(t)
	roSrc := r202ImportSource(t, r, h, "ro-src", true, r202LinkROa+"\n"+r202LinkROb)
	okSrc := r202ImportSource(t, r, h, "ok-src", false, r202LinkOK)
	roRows := r202RowsOf(t, r, roSrc.ID)
	if len(roRows) != 2 {
		t.Fatalf("ro source rows = %d, want 2", len(roRows))
	}
	for _, row := range roRows {
		if row.RelayOnly != 1 {
			t.Fatalf("imported row %q relay_only = %d, want 1", row.Name, row.RelayOnly)
		}
	}

	// 源选：逐个点名两个纯中转节点。
	out := callSubscriptionSaveR4(t, h, map[string]interface{}{
		"name": "ro-sub", "sourceIds": []int64{roSrc.ID},
	})
	if out.Code != 0 {
		t.Fatalf("save ro-sub: code=%d msg=%q", out.Code, out.Msg)
	}
	for _, want := range []string{"纯中转导入节点", "不可订阅、不会出现在订阅中", "ro-node-a", "ro-node-b"} {
		if !strings.Contains(out.Warning, want) {
			t.Fatalf("ro-sub warning must contain %q, got %q", want, out.Warning)
		}
	}
	if strings.Contains(out.Warning, "ok-node") {
		t.Fatalf("ro-sub warning must not name ok-node, got %q", out.Warning)
	}

	// 显式选：只点名被选中的那一个。
	out = callSubscriptionSaveR4(t, h, map[string]interface{}{
		"name": "explicit-sub", "inboundIds": []int64{roRows[0].ID},
	})
	if out.Code != 0 {
		t.Fatalf("save explicit-sub: code=%d msg=%q", out.Code, out.Msg)
	}
	if !strings.Contains(out.Warning, roRows[0].Name) || strings.Contains(out.Warning, roRows[1].Name) {
		t.Fatalf("explicit-sub warning must name only %q, got %q", roRows[0].Name, out.Warning)
	}

	// 正常源：无点名（信封 warning 缺席）。
	out = callSubscriptionSaveR4(t, h, map[string]interface{}{
		"name": "ok-sub", "sourceIds": []int64{okSrc.ID},
	})
	if out.Code != 0 {
		t.Fatalf("save ok-sub: code=%d msg=%q", out.Code, out.Msg)
	}
	if out.Warning != "" {
		t.Fatalf("ok-sub warning must be empty, got %q", out.Warning)
	}

	// 全部：两个纯中转节点都点名，正常节点不点名。
	out = callSubscriptionSaveR4(t, h, map[string]interface{}{
		"name": "all-sub", "sourceAll": true,
	})
	if out.Code != 0 {
		t.Fatalf("save all-sub: code=%d msg=%q", out.Code, out.Msg)
	}
	if !strings.Contains(out.Warning, "ro-node-a") || !strings.Contains(out.Warning, "ro-node-b") || strings.Contains(out.Warning, "ok-node") {
		t.Fatalf("all-sub warning mismatch, got %q", out.Warning)
	}

	// 行为不变：relayOnly 订阅装配仍为 0 端点、计数 0；正常订阅
	// 仍为 1 个直连端点。
	subs, err := r.ListSubscriptions()
	if err != nil {
		t.Fatalf("list subs: %v", err)
	}
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatalf("list rows: %v", err)
	}
	byName := map[string]*model.Subscription{}
	for i := range subs {
		byName[subs[i].Name] = &subs[i]
	}
	if eps := h.subEndpoints(byName["ro-sub"], rows); len(eps) != 0 {
		t.Fatalf("ro-sub endpoints = %d, want 0 (behavior unchanged)", len(eps))
	}
	if view := h.subscriptionView(byName["ro-sub"], rows, time.Now(), ""); view.NodeCount != 0 {
		t.Fatalf("ro-sub nodeCount = %d, want 0", view.NodeCount)
	}
	if eps := h.subEndpoints(byName["ok-sub"], rows); len(eps) != 1 || !eps[0].Direct {
		t.Fatalf("ok-sub endpoints = %+v, want exactly 1 direct", eps)
	}
}

// 面 ②：装配跳过 relayOnly 行与只监听本机行时各落一条点名日志；
// 端点产出与此前逐字一致（两者皆 0、正常行 1 个直连端点）。
func TestR202AssemblyLogNamesSkips(t *testing.T) {
	r, h := r202Setup(t)
	roSrc := r202ImportSource(t, r, h, "ro-src", true, r202LinkROa)
	now := time.Now().UnixMilli()
	localRow := seedExternalRow(t, r, "vless://11111111-2222-3333-4444-555555555555@127.0.0.1:443?security=none#local-node", roSrc.ID, now)
	okRow := seedExternalRow(t, r, r202LinkOK, roSrc.ID, now)
	roRow := r202RowsOf(t, r, roSrc.ID)[0]

	var eps int
	logs := r202CaptureLog(t, func() {
		endpoints, _ := h.inboundEndpoints([]model.NodeInbound{roRow, *localRow, *okRow})
		eps = len(endpoints)
	})
	if eps != 1 {
		t.Fatalf("endpoints = %d, want 1 (only the normal row's direct endpoint)", eps)
	}
	if !strings.Contains(logs, `relay-only inbound "ro-node-a"`) {
		t.Fatalf("assembly log must name the relay-only skip, logs:\n%s", logs)
	}
	if !strings.Contains(logs, `local-only inbound "local-node"`) {
		t.Fatalf("assembly log must name the local-only skip, logs:\n%s", logs)
	}

	logs = r202CaptureLog(t, func() {
		endpoints, _ := h.inboundEndpoints([]model.NodeInbound{*okRow})
		if len(endpoints) != 1 {
			t.Fatalf("control endpoints = %d, want 1", len(endpoints))
		}
	})
	if strings.Contains(logs, "relay-only inbound") || strings.Contains(logs, "local-only inbound") {
		t.Fatalf("control assembly must not log skips, logs:\n%s", logs)
	}
}

// 精确性边界：relayOnly 节点挂有在册中转端点时它经中转出现在订
// 阅中——回执不得点名（点名文案「不会出现在订阅中」必须恒真），
// 且装配确实产出该中转端点（既有行为不变）。
func TestR202RelayOnlyWithLiveRelayNotNamed(t *testing.T) {
	r, h := r202Setup(t)
	roSrc := r202ImportSource(t, r, h, "ro-src", true, r202LinkROa)
	roRow := r202RowsOf(t, r, roSrc.ID)[0]
	now := time.Now().UnixMilli()

	frontID := seedRelayTargetNode(t, r, "r202-front", "198.51.100.9")
	tunnelID := seedRelayTargetTunnel(t, r, "r202-tun", frontID, 30000)
	fwdID, err := r.CreateForwardTx(1, "admin", "r202-fwd", tunnelID, "203.0.113.10:443", "fifo", now, 1,
		[]int64{frontID}, 30011, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("create forward: %v", err)
	}
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{
		InboundID: roRow.ID, ForwardID: fwdID, Name: "r202-relay", Managed: 1, CreatedTime: now,
	}); err != nil {
		t.Fatalf("create relay: %v", err)
	}
	// 夹具自检：中转视图必须是在册活端点，否则本格无意义。
	views, err := h.relayViews(0)
	if err != nil || len(views) != 1 || !views[0].ForwardExists || views[0].ForwardStatus != 1 || len(views[0].Entries) == 0 {
		t.Fatalf("fixture relay view not live: views=%+v err=%v", views, err)
	}

	out := callSubscriptionSaveR4(t, h, map[string]interface{}{
		"name": "relayed-sub", "sourceIds": []int64{roSrc.ID},
	})
	if out.Code != 0 {
		t.Fatalf("save relayed-sub: code=%d msg=%q", out.Code, out.Msg)
	}
	if out.Warning != "" {
		t.Fatalf("relayed relay-only node must not be named, warning=%q", out.Warning)
	}
	subs, err := r.ListSubscriptions()
	if err != nil {
		t.Fatalf("list subs: %v", err)
	}
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatalf("list rows: %v", err)
	}
	eps := h.subEndpoints(&subs[0], rows)
	if len(eps) != 1 || eps[0].RelayID == 0 || eps[0].Direct {
		t.Fatalf("relayed endpoints = %+v, want exactly 1 relay endpoint", eps)
	}
}

// 面 ③：纯中转源在四条回执路径上都带源级提示（新建同步/仅改
// RelayOnly 的文件源/不触发同步的改名/公网 http 提示共存），非
// 纯中转源恒无。
func TestR202SourceReceiptsNameRelayOnly(t *testing.T) {
	r, h := r202Setup(t)
	now := time.Now().UnixMilli()

	saveSource := func(payload string) response.R {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscription/source/save", strings.NewReader(payload))
		rec := httptest.NewRecorder()
		h.remoteSourceSave(rec, req)
		var out response.R
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode source save response: %v", err)
		}
		return out
	}

	// (a) 新建文件源＋内容（走同步回执）：relayOnly=true 带提示，
	// 且导入行 relay_only=1（行为不变）。
	out := saveSource(`{"name":"ro-file","kind":"file","relayOnly":true,"content":"` + r202LinkROa + `"}`)
	if out.Code != 0 {
		t.Fatalf("(a) create ro file source: code=%d msg=%q", out.Code, out.Msg)
	}
	if out.Warning != relayOnlySourceWarning {
		t.Fatalf("(a) warning = %q, want the relay-only source notice", out.Warning)
	}
	sources, err := r.ListRemoteSources()
	if err != nil || len(sources) != 1 {
		t.Fatalf("(a) sources = %v err=%v", sources, err)
	}
	roSrc := sources[0]
	if rows := r202RowsOf(t, r, roSrc.ID); len(rows) != 1 || rows[0].RelayOnly != 1 {
		t.Fatalf("(a) imported rows = %+v, want 1 relay-only row", rows)
	}

	// (b) 同形态 relayOnly=false：无提示。
	out = saveSource(`{"name":"ok-file","kind":"file","relayOnly":false,"content":"` + r202LinkOK + `"}`)
	if out.Code != 0 {
		t.Fatalf("(b) create ok file source: code=%d msg=%q", out.Code, out.Msg)
	}
	if out.Warning != "" {
		t.Fatalf("(b) warning must be empty, got %q", out.Warning)
	}

	// (c) 已有文件源只切 RelayOnly（不走同步）：关时无提示、开时
	// 带提示，且新值直接落导入行（A6-001 既有行为不变）。
	out = saveSource(`{"id":` + strconv.FormatInt(roSrc.ID, 10) + `,"name":"ro-file","kind":"file","relayOnly":false}`)
	if out.Code != 0 {
		t.Fatalf("(c) toggle-off save: code=%d msg=%q", out.Code, out.Msg)
	}
	if out.Warning != "" {
		t.Fatalf("(c) toggle-off warning must be empty, got %q", out.Warning)
	}
	if rows := r202RowsOf(t, r, roSrc.ID); len(rows) != 1 || rows[0].RelayOnly != 0 {
		t.Fatalf("(c) after toggle-off rows = %+v, want relay_only=0", rows)
	}
	out = saveSource(`{"id":` + strconv.FormatInt(roSrc.ID, 10) + `,"name":"ro-file","kind":"file","relayOnly":true}`)
	if out.Code != 0 {
		t.Fatalf("(c) toggle-on save: code=%d msg=%q", out.Code, out.Msg)
	}
	if out.Warning != relayOnlySourceWarning {
		t.Fatalf("(c) warning = %q, want the relay-only source notice", out.Warning)
	}
	if rows := r202RowsOf(t, r, roSrc.ID); len(rows) != 1 || rows[0].RelayOnly != 1 {
		t.Fatalf("(c) after toggle-on rows = %+v, want relay_only=1", rows)
	}

	// (d) URL 源仅改名（不触发同步）：relayOnly=1 带提示、=0 无。
	urlRO := &model.RemoteSource{Name: "ro-url", Kind: model.RemoteSourceURL, URL: "https://example.com/sub", RelayOnly: 1, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveRemoteSource(urlRO); err != nil {
		t.Fatalf("(d) seed url source: %v", err)
	}
	out = saveSource(`{"id":` + strconv.FormatInt(urlRO.ID, 10) + `,"name":"ro-url-2","kind":"url","url":"https://example.com/sub","relayOnly":true,"intervalHours":0}`)
	if out.Code != 0 {
		t.Fatalf("(d) rename ro url source: code=%d msg=%q", out.Code, out.Msg)
	}
	if out.Warning != relayOnlySourceWarning {
		t.Fatalf("(d) warning = %q, want the relay-only source notice", out.Warning)
	}
	urlOK := &model.RemoteSource{Name: "ok-url", Kind: model.RemoteSourceURL, URL: "https://example.com/sub2", RelayOnly: 0, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveRemoteSource(urlOK); err != nil {
		t.Fatalf("(d) seed ok url source: %v", err)
	}
	out = saveSource(`{"id":` + strconv.FormatInt(urlOK.ID, 10) + `,"name":"ok-url-2","kind":"url","url":"https://example.com/sub2","relayOnly":false,"intervalHours":0}`)
	if out.Code != 0 {
		t.Fatalf("(d) rename ok url source: code=%d msg=%q", out.Code, out.Msg)
	}
	if out.Warning != "" {
		t.Fatalf("(d) warning must be empty, got %q", out.Warning)
	}

	// (e) 公网 http 提示与纯中转提示共存：Msg 仍是 http 提示，
	// warning 独立带出。
	urlHTTP := &model.RemoteSource{Name: "ro-http", Kind: model.RemoteSourceURL, URL: "http://example.com/sub", RelayOnly: 1, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveRemoteSource(urlHTTP); err != nil {
		t.Fatalf("(e) seed http source: %v", err)
	}
	out = saveSource(`{"id":` + strconv.FormatInt(urlHTTP.ID, 10) + `,"name":"ro-http-2","kind":"url","url":"http://example.com/sub","relayOnly":true,"intervalHours":0}`)
	if out.Code != 0 {
		t.Fatalf("(e) rename http ro source: code=%d msg=%q", out.Code, out.Msg)
	}
	if !strings.Contains(out.Msg, "公网 http") {
		t.Fatalf("(e) msg must keep the public-http notice, got %q", out.Msg)
	}
	if out.Warning != relayOnlySourceWarning {
		t.Fatalf("(e) warning = %q, want the relay-only source notice", out.Warning)
	}
}
