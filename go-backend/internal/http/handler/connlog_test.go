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
	rows, err := rig.r.ListInboundConnLogs(rig.inboundID, "", 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("attributed rows = (%v, %v), want exactly 1 under the landing inbound", rows, err)
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
	if rows, _ := rig.r.ListInboundConnLogs(rig.inboundID, "", 0, 10); len(rows) != 1 {
		t.Fatalf("after replay rows = %d, want 1", len(rows))
	}

	// 入口节点谎报落地入站的 tag：归属校验不过 → 落未归属，
	// 不挂到别人入站名下（事件本身不丢）。
	events = fmt.Sprintf(`[
		{"type":"open","connId":"c2","entryKind":"inbound","entryId":"in-%d","clientIp":"203.0.113.9","network":"tcp","sessionKind":"tcp_conn","startTime":%d}
	]`, rig.inboundID, base+120_000)
	rec = rig.postBatch(t, rig.entrySec, 2, events)
	if rec.Code != http.StatusOK {
		t.Fatalf("foreign-tag ingest status = %d, body %s", rec.Code, rec.Body.String())
	}
	if rows, _ := rig.r.ListInboundConnLogs(rig.inboundID, "", 0, 10); len(rows) != 1 {
		t.Fatalf("foreign-tag event leaked into landing inbound: rows = %d, want still 1", len(rows))
	}
	unattributed, _ := rig.r.ListInboundConnLogs(0, "", 0, 10)
	if len(unattributed) != 1 || unattributed[0].ConnID != "c2" {
		t.Fatalf("unattributed rows = %v, want the c2 row under display 0", unattributed)
	}

	// 落地节点本人上报自己的入站 tag：平凡归属成立。
	events = fmt.Sprintf(`[
		{"type":"open","connId":"c3","entryKind":"inbound","entryId":"in-%d","clientIp":"203.0.113.10","network":"tcp","sessionKind":"tcp_conn","startTime":%d}
	]`, rig.inboundID, base+180_000)
	rec = rig.postBatch(t, rig.landingSec, 1, events)
	if rec.Code != http.StatusOK {
		t.Fatalf("self inbound ingest status = %d, body %s", rec.Code, rec.Body.String())
	}
	rows, _ = rig.r.ListInboundConnLogs(rig.inboundID, "", 0, 10)
	if len(rows) != 2 || rows[0].ConnID != "c3" {
		t.Fatalf("rows after self report = %v, want [c3 c1] newest first", rows)
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

type connlogQueryPage struct {
	Rows          []connlogRowView `json:"rows"`
	Dropped       int64            `json:"dropped"`
	RetentionDays int              `json:"retentionDays"`
}

func (rig *connlogTestRig) query(t *testing.T, claims *auth.Claims, body string) (int, connlogQueryPage) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inbound/connlog", strings.NewReader(body))
	if claims != nil {
		ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey, *claims)
		req = req.WithContext(ctx)
	}
	rec := httptest.NewRecorder()
	rig.h.inboundConnlog(rec, req)
	var envelope struct {
		Code int              `json:"code"`
		Msg  string           `json:"msg"`
		Data connlogQueryPage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("query response not an envelope: %s", rec.Body.String())
	}
	return envelope.Code, envelope.Data
}

func TestConnlogQueryPermissionsAndPagination(t *testing.T) {
	rig := newConnlogTestRig(t)
	base := int64(1_700_000_000_000)
	events := fmt.Sprintf(`[
		{"type":"open","connId":"q1","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.7","network":"tcp","sessionKind":"tcp_conn","startTime":%d},
		{"type":"open","connId":"q2","entryKind":"forward","entryId":"77_2_3_udp","clientIp":"203.0.113.8","network":"udp","sessionKind":"udp_session","startTime":%d},
		{"type":"open","connId":"q3","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.7","network":"tcp","sessionKind":"event_only","startTime":%d}
	]`, base, base+1000, base+2000)
	if rec := rig.postBatch(t, rig.entrySec, 1, events); rec.Code != http.StatusOK {
		t.Fatalf("seed ingest status = %d, body %s", rec.Code, rec.Body.String())
	}

	admin := &auth.Claims{Sub: "1", RoleID: 0}
	user := &auth.Claims{Sub: "2", RoleID: 1}

	body := fmt.Sprintf(`{"inboundId":%d,"current":1,"size":2}`, rig.inboundID)
	code, page := rig.query(t, admin, body)
	page1 := page.Rows
	if code != 0 {
		t.Fatalf("admin page1 code = %d, want 0", code)
	}
	if len(page1) != 2 || page1[0].StartTime != base+2000 || page1[1].StartTime != base+1000 {
		t.Fatalf("page1 order = %v, want newest first [q3 q2]", page1)
	}
	// 连接中行：endTime/durationMs 必须为 null，不冒充时长。
	if page1[0].EndTime != nil || page1[0].DurationMs != nil || page1[0].Status != repo.ConnlogStatusOpen {
		t.Fatalf("open row view = %+v, want null end/duration", page1[0])
	}

	body = fmt.Sprintf(`{"inboundId":%d,"current":2,"size":2}`, rig.inboundID)
	_, page2p := rig.query(t, admin, body)
	page2 := page2p.Rows
	if len(page2) != 1 || page2[0].StartTime != base {
		t.Fatalf("page2 = %v, want [q1]", page2)
	}

	body = fmt.Sprintf(`{"inboundId":%d,"current":1,"size":10,"ip":"203.0.113.7"}`, rig.inboundID)
	_, byIPPage := rig.query(t, admin, body)
	byIP := byIPPage.Rows
	if len(byIP) != 2 {
		t.Fatalf("ip filter rows = %v, want q1+q3 only", byIP)
	}

	if code, _ := rig.query(t, user, body); code != 403 {
		t.Fatalf("non-admin query code = %d, want 403", code)
	}
	if code, _ := rig.query(t, nil, body); code != 401 {
		t.Fatalf("anonymous query code = %d, want 401", code)
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

// 集成遗留定形：dropped 落库＋页脚汇总。批次被真正应用时才按
// 上报节点入账一次；同批重放（agent 的 dropped 是未确认快照、
// 会随重放再到）不得重复入账；查询响应是页对象，dropped 汇总
// 「为本入站供数的入口节点」的累计、retentionDays 回当前口径。
func TestConnlogDroppedFooterEndToEnd(t *testing.T) {
	rig := newConnlogTestRig(t)
	base := int64(1_700_000_100_000)
	events := fmt.Sprintf(`[
		{"type":"open","connId":"d1","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.9","network":"tcp","sessionKind":"tcp_conn","startTime":%d}
	]`, base)

	if rec := rig.postBatchDropped(t, rig.entrySec, 1, 7, events); rec.Code != http.StatusOK {
		t.Fatalf("ingest status = %d, body %s", rec.Code, rec.Body.String())
	}
	admin := &auth.Claims{Sub: "1", RoleID: 0}
	body := fmt.Sprintf(`{"inboundId":%d,"current":1,"size":10}`, rig.inboundID)
	_, page := rig.query(t, admin, body)
	if page.Dropped != 7 {
		t.Fatalf("footer dropped = %d, want 7 after the applied batch", page.Dropped)
	}
	if page.RetentionDays != 7 {
		t.Fatalf("footer retentionDays = %d, want the default 7", page.RetentionDays)
	}

	// 同批逐字节重放：ack 为 Replay，dropped 不得二次入账。
	if rec := rig.postBatchDropped(t, rig.entrySec, 1, 7, events); rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, body %s", rec.Code, rec.Body.String())
	}
	_, page = rig.query(t, admin, body)
	if page.Dropped != 7 {
		t.Fatalf("footer dropped after replay = %d, want still 7 (no double booking)", page.Dropped)
	}

	// 下一批带新的未确认丢弃增量：应用后累计推进。
	events2 := fmt.Sprintf(`[
		{"type":"open","connId":"d2","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.10","network":"tcp","sessionKind":"tcp_conn","startTime":%d}
	]`, base+1000)
	if rec := rig.postBatchDropped(t, rig.entrySec, 2, 3, events2); rec.Code != http.StatusOK {
		t.Fatalf("second ingest status = %d, body %s", rec.Code, rec.Body.String())
	}
	_, page = rig.query(t, admin, body)
	if page.Dropped != 10 {
		t.Fatalf("footer dropped after second batch = %d, want 10", page.Dropped)
	}

	// 落地节点自身从未供数：其名下入站（此处借未归属查询不可
	// 见，改查另一构造）——直接验 repo 汇总口径：只含供数节点。
	ids, err := rig.r.ConnlogEntryNodeIDs(rig.inboundID)
	if err != nil || len(ids) != 1 || ids[0] != rig.entryID {
		t.Fatalf("entry node ids = (%v, %v), want [entry node]", ids, err)
	}
	if total, err := rig.r.SumConnlogDroppedByNodes([]int64{rig.landingID}); err != nil || total != 0 {
		t.Fatalf("landing node dropped sum = (%d, %v), want 0", total, err)
	}
}
