package kcp

import "testing"

// FIX-KCPMEM（P1-04）：agent 缺省面须与面板 small 档帽逐值对齐——
// 面板未下发（或旧面板）时缺省高于小档帽即是旁路。
func TestDefaultConfigAlignsWithPanelSmallTier(t *testing.T) {
	c := DefaultConfig
	if c.SndWnd != 512 || c.RcvWnd != 512 {
		t.Fatalf("default wnd must be 512/512, got %d/%d", c.SndWnd, c.RcvWnd)
	}
	if c.SockBuf != 1048576 || c.SmuxBuf != 1048576 || c.StreamBuf != 524288 {
		t.Fatalf("default bufs must be 1048576/1048576/524288, got %d/%d/%d",
			c.SockBuf, c.SmuxBuf, c.StreamBuf)
	}
	// FEC 缺省保持 10/3（面板缺省档同值）。
	if c.DataShard != 10 || c.ParityShard != 3 {
		t.Fatalf("default FEC must stay 10/3, got %d/%d", c.DataShard, c.ParityShard)
	}
}
