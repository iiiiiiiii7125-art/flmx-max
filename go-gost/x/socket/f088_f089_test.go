package socket

import (
	"testing"
	"time"
)

// F-088 回归：TcpPing/UdpPing 的 count/timeout 必须钳制——count 无上限
// 时单个只读诊断命令可按 count×(timeout+100ms) 长期占用命令槽。
func TestClampPingParams(t *testing.T) {
	cases := []struct {
		count, timeout         int
		wantCount, wantTimeout int
	}{
		{0, 0, 4, 5000},            // 缺省回退默认值
		{-1, -5, 4, 5000},          // 负数同样回退
		{3, 2000, 3, 2000},         // 正常值不动
		{20, 10000, 20, 10000},     // 恰在上限
		{100000, 60000, 20, 10000}, // 超限钳制
		{21, 10001, 20, 10000},     // 刚过上限
	}
	for _, c := range cases {
		gotCount, gotTimeout := clampPingParams(c.count, c.timeout)
		if gotCount != c.wantCount || gotTimeout != c.wantTimeout {
			t.Errorf("clampPingParams(%d, %d) = (%d, %d), want (%d, %d)",
				c.count, c.timeout, gotCount, gotTimeout, c.wantCount, c.wantTimeout)
		}
	}
}

func TestHandleTcpPingClampsHugeCount(t *testing.T) {
	w := &WebSocketReporter{}
	start := time.Now()
	resp, err := w.handleTcpPing(map[string]interface{}{
		"ip": "127.0.0.1", "port": 1, "count": 100000, "timeout": 60000,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("handleTcpPing: %v", err)
	}
	if resp.Success {
		t.Fatal("连接被拒的目标不应报 success")
	}
	// 钳制后 20 次尝试（每次拒连近乎瞬时 + 19×100ms 间隔）应在数秒内
	// 结束；未钳制时 100000 次尝试需数小时。
	if elapsed > 30*time.Second {
		t.Fatalf("count 未被钳制: 耗时 %v", elapsed)
	}
}

// F-089 回归（socket 侧）：加密器缺失时 encryptPayload 必须报错
// （fail-closed），不得回退明文。
func TestEncryptPayloadFailsClosedWithoutCrypto(t *testing.T) {
	w := &WebSocketReporter{}
	if _, err := w.encryptPayload([]byte(`{"type":"metric"}`)); err == nil {
		t.Fatal("aesCrypto 为 nil 时 encryptPayload 必须报错，不得回退明文")
	}
}
