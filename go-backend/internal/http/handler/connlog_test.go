package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

type connlogTestRig struct {
	h          *Handler
	r          *repo.Repository
	entryID    int64
	landingID  int64
	inboundID  int64
	entrySec   string
	landingSec string
}

func newConnlogTestRig(t *testing.T) *connlogTestRig {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	rig := &connlogTestRig{r: r, entrySec: "entry-secret-1", landingSec: "landing-secret-1"}
	rig.entryID, err = r.CreateNode("entry-node", rig.entrySec, "192.0.2.1", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", 1, nil, "")
	if err != nil {
		t.Fatalf("create entry node: %v", err)
	}
	rig.landingID, err = r.CreateNode("landing-node", rig.landingSec, "192.0.2.2", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", 2, nil, "")
	if err != nil {
		t.Fatalf("create landing node: %v", err)
	}
	kernelRow := &model.NodeKernel{NodeID: rig.landingID, KernelType: "sing-box", Status: "running", CreatedTime: now, UpdatedTime: now}
	if err := r.CreateNodeKernel(kernelRow); err != nil {
		t.Fatalf("create kernel: %v", err)
	}
	inbound := &model.NodeInbound{KernelID: kernelRow.ID, NodeID: rig.landingID, Name: "land-in", Protocol: "vless",
		Listen: "::", Port: 443, CreatedTime: now, UpdatedTime: now}
	if err := r.CreateNodeInbound(inbound); err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	rig.inboundID = inbound.ID
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: inbound.ID, ForwardID: 77, Name: "relay", CreatedTime: now}); err != nil {
		t.Fatalf("create relay: %v", err)
	}
	rig.h = New(r, "unit-test-secret")
	return rig
}

const connlogTestStream = "0123456789abcdef0123456789abcdef"

func (rig *connlogTestRig) postBatch(t *testing.T, secret string, sequence int64, events string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"stream":%q,"sequence":%d,"events":%s}`, connlogTestStream, sequence, events)
	req := httptest.NewRequest(http.MethodPost, "/flow/connlog/v2?secret="+secret, strings.NewReader(body))
	rec := httptest.NewRecorder()
	rig.h.flowConnlogV2(rec, req)
	return rec
}

func TestConnlogIngestAttributionAndReplay(t *testing.T) {
	rig := newConnlogTestRig(t)
	base := int64(1_700_000_000_000)

	// 入口节点上报 forward 入口的一开一关：经 relay 归属到落地入站。
	events := fmt.Sprintf(`[
		{"type":"open","connId":"c1","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.7","clientPort":51000,"network":"tcp","sessionKind":"tcp_conn","startTime":%d},
		{"type":"close","connId":"c1","entryKind":"forward","entryId":"77_2_3_tcp","endTime":%d}
	]`, base, base+60_000)
	rec := rig.postBatch(t, rig.entrySec, 1, events)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest status = %d, body %s", rec.Code, rec.Body.String())
	}
	var ack struct {
		Stream   string `json:"stream"`
		Sequence int64  `json:"sequence"`
		Cursor   int64  `json:"cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ack); err != nil || ack.Cursor != 1 {
		t.Fatalf("ack = %s (err %v), want cursor 1", rec.Body.String(), err)
	}
	rows, err := rig.r.ListNodeConnLogs(rig.entryID, "", 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("attributed rows = (%v, %v), want exactly 1 under the entry node", rows, err)
	}
	row := rows[0]
	if row.EntryNodeID != rig.entryID || row.ClientIP != "203.0.113.7" || row.Status != repo.ConnlogStatusClosed || row.DurationMs != 60_000 {
		t.Fatalf("row = %+v, want entry-node sourced, closed, 60s", row)
	}

	// 整批重放：确认回执照发，行数不变。
	rec = rig.postBatch(t, rig.entrySec, 1, events)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, body %s", rec.Code, rec.Body.String())
	}
	if rows, _ := rig.r.ListNodeConnLogs(rig.entryID, "", 0, 10); len(rows) != 1 {
		t.Fatalf("after replay rows = %d, want 1", len(rows))
	}

	// 入口节点谎报落地入站的 tag：归属校验不过 → 落未归属，
	// 不挂到别人入站名下（事件本身不丢）。入口节点视图仍见此
	// 行（EntryNodeID 是上报节点），但其 DisplayInboundID 为 0。
	events = fmt.Sprintf(`[
		{"type":"open","connId":"c2","entryKind":"inbound","entryId":"in-%d","clientIp":"203.0.113.9","network":"tcp","sessionKind":"tcp_conn","startTime":%d}
	]`, rig.inboundID, base+120_000)
	rec = rig.postBatch(t, rig.entrySec, 2, events)
	if rec.Code != http.StatusOK {
		t.Fatalf("foreign-tag ingest status = %d, body %s", rec.Code, rec.Body.String())
	}
	rows, _ = rig.r.ListNodeConnLogs(rig.entryID, "", 0, 10)
	if len(rows) != 2 {
		t.Fatalf("entry node rows after foreign tag = %d, want 2 (c1 attributed + c2 unattributed)", len(rows))
	}
	unattributed := 0
	for _, r := range rows {
		if r.DisplayInboundID == 0 {
			unattributed++
			if r.ConnID != "c2" {
				t.Fatalf("unattributed row = %v, want c2", r)
			}
		}
	}
	if unattributed != 1 {
		t.Fatalf("unattributed rows = %d, want exactly 1 (c2)", unattributed)
	}

	// 落地节点本人上报自己的入站 tag：平凡归属成立。
	events = fmt.Sprintf(`[
		{"type":"open","connId":"c3","entryKind":"inbound","entryId":"in-%d","clientIp":"203.0.113.10","network":"tcp","sessionKind":"tcp_conn","startTime":%d}
	]`, rig.inboundID, base+180_000)
	rec = rig.postBatch(t, rig.landingSec, 1, events)
	if rec.Code != http.StatusOK {
		t.Fatalf("self inbound ingest status = %d, body %s", rec.Code, rec.Body.String())
	}
	rows, _ = rig.r.ListNodeConnLogs(rig.landingID, "", 0, 10)
	if len(rows) != 1 || rows[0].ConnID != "c3" {
		t.Fatalf("landing node rows after self report = %v, want [c3]", rows)
	}
	rows, _ = rig.r.ListNodeConnLogs(rig.entryID, "", 0, 10)
	if len(rows) != 2 || rows[0].ConnID != "c2" {
		t.Fatalf("entry node rows after self report = %v, want [c2 c1] newest first", rows)
	}
}

func TestConnlogIngestAuthAndValidation(t *testing.T) {
	rig := newConnlogTestRig(t)
	events := `[{"type":"open","connId":"c1","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.7","network":"tcp","sessionKind":"tcp_conn","startTime":1700000000000}]`

	if rec := rig.postBatch(t, "no-such-secret", 1, events); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown secret status = %d, want 401", rec.Code)
	}

	bad := `[{"type":"open","connId":"c1","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.7","network":"tcp","sessionKind":"bogus","startTime":1700000000000}]`
	rec := rig.postBatch(t, rig.entrySec, 1, bad)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_batch") {
		t.Fatalf("invalid event = (%d, %s), want 400 invalid_batch", rec.Code, rec.Body.String())
	}

	badIP := `[{"type":"open","connId":"c1","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"not-an-ip","network":"tcp","sessionKind":"tcp_conn","startTime":1700000000000}]`
	if rec := rig.postBatch(t, rig.entrySec, 1, badIP); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad clientIp status = %d, want 400", rec.Code)
	}

	// 序号缺口（首批就是 5）→ 409 reset_stream，agent 换 stream 自愈。
	rec = rig.postBatch(t, rig.entrySec, 5, events)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "reset_stream") {
		t.Fatalf("gap batch = (%d, %s), want 409 reset_stream", rec.Code, rec.Body.String())
	}
}

type connlogNodeQueryPage struct {
	Rows          []connlogRowView `json:"rows"`
	Total         int64            `json:"total"`
	Enabled       bool             `json:"enabled"`
	Dropped       int64            `json:"dropped"`
	RetentionDays int              `json:"retentionDays"`
}

func (rig *connlogTestRig) queryNode(t *testing.T, claims *auth.Claims, rawQuery string) (int, connlogNodeQueryPage) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/node/connlog?"+rawQuery, nil)
	if claims != nil {
		ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey, *claims)
		req = req.WithContext(ctx)
	}
	rec := httptest.NewRecorder()
	rig.h.nodeConnlog(rec, req)
	var envelope struct {
		Code int                  `json:"code"`
		Msg  string               `json:"msg"`
		Data connlogNodeQueryPage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("query response not an envelope: %s", rec.Body.String())
	}
	return envelope.Code, envelope.Data
}

func TestConnlogNodeQueryPermissionsPaginationAndEnrichment(t *testing.T) {
	rig := newConnlogTestRig(t)
	base := int64(1_700_000_000_000)
	// 入口节点上报 forward 入口的三行（q1 tcp、q2 udp、q3 event_only）。
	events := fmt.Sprintf(`[
		{"type":"open","connId":"q1","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.7","network":"tcp","sessionKind":"tcp_conn","startTime":%d},
		{"type":"open","connId":"q2","entryKind":"forward","entryId":"77_2_3_udp","clientIp":"203.0.113.8","network":"udp","sessionKind":"udp_session","startTime":%d},
		{"type":"open","connId":"q3","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.7","network":"tcp","sessionKind":"event_only","startTime":%d}
	]`, base, base+1000, base+2000)
	if rec := rig.postBatch(t, rig.entrySec, 1, events); rec.Code != http.StatusOK {
		t.Fatalf("seed ingest status = %d, body %s", rec.Code, rec.Body.String())
	}
	// 落地节点上报自己的直连入站一行：只开节点开关即被采集的
	// 直连路径（逐入站开关不存在；payload 侧见
	// connlog_u5_test.go）。
	direct := fmt.Sprintf(`[{"type":"open","connId":"q4","entryKind":"inbound","entryId":"in-%d","clientIp":"203.0.113.11","network":"tcp","sessionKind":"tcp_conn","startTime":%d}]`, rig.inboundID, base+3000)
	if rec := rig.postBatch(t, rig.landingSec, 1, direct); rec.Code != http.StatusOK {
		t.Fatalf("direct ingest status = %d, body %s", rec.Code, rec.Body.String())
	}
	// 无法解析的入口 tag（直插行，绕过摄入校验）：enrichment 回落格。
	if err := rig.r.DB().Create(&model.InboundConnLog{EntryNodeID: rig.entryID, ConnID: "q5", EntryKind: repo.ConnlogEntryInbound, EntryID: "bogus", ClientIP: "203.0.113.99", Network: "tcp", SessionKind: "tcp_conn", StartTime: base + 500, Status: repo.ConnlogStatusOpen, CreatedTime: base}).Error; err != nil {
		t.Fatalf("seed unparseable row: %v", err)
	}

	admin := &auth.Claims{Sub: "1", RoleID: 0}
	user := &auth.Claims{Sub: "2", RoleID: 1}

	entryQ := fmt.Sprintf("nodeId=%d&offset=0&limit=2", rig.entryID)
	code, page := rig.queryNode(t, admin, entryQ)
	if code != 0 {
		t.Fatalf("admin page1 code = %d, want 0", code)
	}
	if page.Total != 4 {
		t.Fatalf("total = %d, want 4", page.Total)
	}
	if page.Enabled {
		t.Fatal("enabled = true, want false (node switch defaults off)")
	}
	page1 := page.Rows
	if len(page1) != 2 || page1[0].StartTime != base+2000 || page1[1].StartTime != base+1000 {
		t.Fatalf("page1 order = %v, want newest first [q3 q2]", page1)
	}
	// 连接中行：endTime/durationMs 必须为 null，不冒充时长。
	if page1[0].EndTime != nil || page1[0].DurationMs != nil || page1[0].Status != repo.ConnlogStatusOpen {
		t.Fatalf("open row view = %+v, want null end/duration", page1[0])
	}
	// 转发入口无转发记录：端口 0、入口名回落原始 EntryID，不编造。
	if page1[0].LocalPort != 0 || page1[0].EntryName != "77_2_3_tcp" {
		t.Fatalf("forward row enrichment = (%d, %q), want (0, %q)", page1[0].LocalPort, page1[0].EntryName, "77_2_3_tcp")
	}

	_, page2p := rig.queryNode(t, admin, fmt.Sprintf("nodeId=%d&offset=2&limit=2", rig.entryID))
	page2 := page2p.Rows
	if len(page2) != 2 || page2[0].StartTime != base+500 || page2[1].StartTime != base {
		t.Fatalf("page2 = %v, want [q5 q1]", page2)
	}
	if page2[0].LocalPort != 0 || page2[0].EntryName != "bogus" {
		t.Fatalf("unparseable row enrichment = (%d, %q), want (0, %q)", page2[0].LocalPort, page2[0].EntryName, "bogus")
	}

	_, byIPPage := rig.queryNode(t, admin, fmt.Sprintf("nodeId=%d&offset=0&limit=10&ip=203.0.113.7", rig.entryID))
	if byIPPage.Total != 2 || len(byIPPage.Rows) != 2 {
		t.Fatalf("ip filter rows = %v, want q1+q3 only", byIPPage.Rows)
	}

	// 页脚字段：无丢弃时 dropped 回 0、保留期回默认 7 天（A-P2-1）。
	if page.Dropped != 0 || page.RetentionDays != 7 {
		t.Fatalf("footer = (dropped=%d retentionDays=%d), want (0, 7)", page.Dropped, page.RetentionDays)
	}

	// 直连行：端口＋入站名如实解析。
	_, landingPage := rig.queryNode(t, admin, fmt.Sprintf("nodeId=%d&offset=0&limit=10", rig.landingID))
	if landingPage.Total != 1 || len(landingPage.Rows) != 1 {
		t.Fatalf("landing rows = %v, want [q4]", landingPage.Rows)
	}
	if landingPage.Rows[0].LocalPort != 443 || landingPage.Rows[0].EntryName != "land-in" {
		t.Fatalf("direct row enrichment = (%d, %q), want (443, %q)", landingPage.Rows[0].LocalPort, landingPage.Rows[0].EntryName, "land-in")
	}

	// 开关只控采集，不删历史行：开后同查仍见全部行，enabled 翻 true。
	if err := rig.r.UpdateNodeConnLogEnabled(rig.entryID, 1); err != nil {
		t.Fatalf("enable node connlog: %v", err)
	}
	_, enabledPage := rig.queryNode(t, admin, entryQ)
	if !enabledPage.Enabled || enabledPage.Total != 4 {
		t.Fatalf("after enable page = (enabled=%v total=%d), want (true, 4)", enabledPage.Enabled, enabledPage.Total)
	}

	if code, _ := rig.queryNode(t, user, entryQ); code != 403 {
		t.Fatalf("non-admin query code = %d, want 403", code)
	}
	if code, _ := rig.queryNode(t, nil, entryQ); code != 401 {
		t.Fatalf("anonymous query code = %d, want 401", code)
	}
	if code, _ := rig.queryNode(t, admin, "offset=0&limit=10"); code == 0 {
		t.Fatal("missing nodeId must fail")
	}
	if code, _ := rig.queryNode(t, admin, "nodeId=999999&offset=0&limit=10"); code == 0 {
		t.Fatal("unknown node must fail")
	}
}

func (rig *connlogTestRig) postBatchDropped(t *testing.T, secret string, sequence int64, dropped int64, events string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"stream":%q,"sequence":%d,"dropped":%d,"events":%s}`, connlogTestStream, sequence, dropped, events)
	req := httptest.NewRequest(http.MethodPost, "/flow/connlog/v2?secret="+secret, strings.NewReader(body))
	rec := httptest.NewRecorder()
	rig.h.flowConnlogV2(rec, req)
	return rec
}

// 集成遗留定形：dropped 落库＋仓库级汇总。批次被真正应用时才按
// 上报节点入账一次；同批重放（agent 的 dropped 是未确认快照、
// 会随重放再到）不得重复入账；节点端点返回该入口节点的行。
func TestConnlogDroppedAccountingEndToEnd(t *testing.T) {
	rig := newConnlogTestRig(t)
	base := int64(1_700_000_100_000)
	events := fmt.Sprintf(`[
		{"type":"open","connId":"d1","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.9","network":"tcp","sessionKind":"tcp_conn","startTime":%d}
	]`, base)

	if rec := rig.postBatchDropped(t, rig.entrySec, 1, 7, events); rec.Code != http.StatusOK {
		t.Fatalf("ingest status = %d, body %s", rec.Code, rec.Body.String())
	}
	admin := &auth.Claims{Sub: "1", RoleID: 0}
	entryQ := fmt.Sprintf("nodeId=%d&offset=0&limit=10", rig.entryID)
	if code, page := rig.queryNode(t, admin, entryQ); code != 0 || page.Total != 1 || len(page.Rows) != 1 || page.Rows[0].StartTime != base {
		t.Fatalf("node endpoint after ingest = (code=%d total=%d rows=%v), want [d1]", code, page.Total, page.Rows)
	}
	// 页脚字段：端点回的 dropped 与仓储汇总一致、保留期回默认 7 天（A-P2-1）。
	if code, page := rig.queryNode(t, admin, entryQ); code != 0 || page.Dropped != 7 || page.RetentionDays != 7 {
		t.Fatalf("node endpoint footer after ingest = (code=%d dropped=%d retentionDays=%d), want (0, 7, 7)", code, page.Dropped, page.RetentionDays)
	}
	if total, err := rig.r.SumConnlogDroppedByNodes([]int64{rig.entryID}); err != nil || total != 7 {
		t.Fatalf("entry node dropped sum = (%d, %v), want 7 after the applied batch", total, err)
	}

	// 同批逐字节重放：ack 为 Replay，dropped 不得二次入账。
	if rec := rig.postBatchDropped(t, rig.entrySec, 1, 7, events); rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, body %s", rec.Code, rec.Body.String())
	}
	if total, err := rig.r.SumConnlogDroppedByNodes([]int64{rig.entryID}); err != nil || total != 7 {
		t.Fatalf("dropped sum after replay = (%d, %v), want still 7 (no double booking)", total, err)
	}

	// 下一批带新的未确认丢弃增量：应用后累计推进。
	events2 := fmt.Sprintf(`[
		{"type":"open","connId":"d2","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.10","network":"tcp","sessionKind":"tcp_conn","startTime":%d}
	]`, base+1000)
	if rec := rig.postBatchDropped(t, rig.entrySec, 2, 3, events2); rec.Code != http.StatusOK {
		t.Fatalf("second ingest status = %d, body %s", rec.Code, rec.Body.String())
	}
	if total, err := rig.r.SumConnlogDroppedByNodes([]int64{rig.entryID}); err != nil || total != 10 {
		t.Fatalf("dropped sum after second batch = (%d, %v), want 10", total, err)
	}
	if _, page := rig.queryNode(t, admin, entryQ); page.Total != 2 {
		t.Fatalf("node endpoint total after second batch = %d, want 2", page.Total)
	}
	if _, page := rig.queryNode(t, admin, entryQ); page.Dropped != 10 {
		t.Fatalf("node endpoint dropped after second batch = %d, want 10", page.Dropped)
	}

	// 落地节点自身从未供数：只含供数节点。
	ids, err := rig.r.ConnlogEntryNodeIDs(rig.inboundID)
	if err != nil || len(ids) != 1 || ids[0] != rig.entryID {
		t.Fatalf("entry node ids = (%v, %v), want [entry node]", ids, err)
	}
	if total, err := rig.r.SumConnlogDroppedByNodes([]int64{rig.landingID}); err != nil || total != 0 {
		t.Fatalf("landing node dropped sum = (%d, %v), want 0", total, err)
	}
}
