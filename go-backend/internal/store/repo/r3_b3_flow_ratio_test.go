package repo

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// R3X-003 (R3-F-01): GetFlowUploadForwardMetas 的 TrafficRatio 归一化
// 只能在隧道行缺失（TunnelMissing）时兜底为 1。行存在时：0 必须保留
// 0（BC-R2-03「零计费」）、正倍率原样、负倍率钳为 0 并 warn，不得再
// 把有意配置的 0 改回 1 倍计费。
func TestR3B3FlowUploadMetasZeroRatioPreserved(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "r3b3-ratio.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	now := time.Now().UnixMilli()
	seed := func(sql string, args ...interface{}) {
		t.Helper()
		if err := r.DB().Exec(sql, args...).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	seed(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(2, 'u2', 'pwd', 1, 2727251700000, 99999, 0, 0, 1, 99999, ?, ?, 1)`, now, now)
	// ratio=0（有意免费）/ ratio=2（对照）/ ratio=-1.5（异常负值）。
	seed(`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(1, 'free', 0, 1, 'tls', 2, ?, ?, 1, NULL, 0)`, now, now)
	seed(`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(2, 'paid', 2.0, 1, 'tls', 2, ?, ?, 1, NULL, 0)`, now, now)
	seed(`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(3, 'neg', -1.5, 1, 'tls', 2, ?, ?, 1, NULL, 0)`, now, now)
	seed(`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(20, 2, 'u2', 'f20', 1, '1.1.1.1:80', 'fifo', 0, 0, ?, ?, 1, 0)`, now, now)
	seed(`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(21, 2, 'u2', 'f21', 2, '1.1.1.1:80', 'fifo', 0, 0, ?, ?, 1, 0)`, now, now)
	seed(`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(22, 2, 'u2', 'f22', 3, '1.1.1.1:80', 'fifo', 0, 0, ?, ?, 1, 0)`, now, now)
	// 隧道行真缺失的 forward（兜底对照）。
	seed(`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(25, 2, 'u2', 'f25', 99, '1.1.1.1:80', 'fifo', 0, 0, ?, ?, 1, 0)`, now, now)

	var logBuf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldOut)

	metas, err := r.GetFlowUploadForwardMetas([]int64{20, 21, 22, 25})
	if err != nil {
		t.Fatalf("get metas: %v", err)
	}
	log.SetOutput(oldOut)

	if got := metas[20].TrafficRatio; got != 0 {
		t.Fatalf("R3X-003: existing tunnel ratio=0 must be preserved as 0, got %v", got)
	}
	if got := metas[21].TrafficRatio; got != 2 {
		t.Fatalf("ratio=2 must be unchanged, got %v", got)
	}
	if got := metas[22].TrafficRatio; got != 0 {
		t.Fatalf("negative ratio on existing tunnel must be clamped to 0, got %v", got)
	}
	if !strings.Contains(logBuf.String(), "traffic_ratio_negative") {
		t.Fatalf("negative ratio clamp must warn, log=%q", logBuf.String())
	}
	// 隧道缺失仍兜底 1 且 F-S01 warn 在位。
	if got := metas[25].TrafficRatio; got != 1 || metas[25].TunnelFlow != 1 {
		t.Fatalf("missing tunnel must fall back to ratio=1 flow=1, got %#v", metas[25])
	}
	if !strings.Contains(logBuf.String(), "tunnel_row_missing") {
		t.Fatalf("missing tunnel warn must be present, log=%q", logBuf.String())
	}
}
