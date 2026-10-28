package handler

import "testing"

// R3-H-01（P2）下发侧守卫：修复只动恢复侧，buildForwardServiceConfigs
// 的“端口胜出”分支一字不动。本测试把 H 域探针 TestR3H_R3H01DeploySide
// 的三格钉为回归：bindIP 含端口时嵌入端口胜出、裸 bindIP 时 fp.Port
// 胜出、非法主机名直通（下发即失败——防线在恢复侧，不在此处改）。

func TestR3H01DeploySideUnchanged(t *testing.T) {
	forward := &forwardRecord{RemoteAddr: "1.2.3.4:80", Strategy: "fifo", TunnelID: 7}
	node := &nodeRecord{TCPListenAddr: "0.0.0.0", UDPListenAddr: "[::]"}
	cells := []struct {
		bindIP   string
		wantAddr string
	}{
		{bindIP: "10.0.0.1:9999", wantAddr: "10.0.0.1:9999"},
		{bindIP: "10.0.0.1", wantAddr: "10.0.0.1:8080"},
		{bindIP: "a..b", wantAddr: "a..b:8080"},
	}
	for _, c := range cells {
		services := buildForwardServiceConfigs("1_2_0", forward, nil, node, 8080, c.bindIP, forwardRuntimeLimiters{})
		if len(services) != 2 {
			t.Fatalf("bindIP %q: expected 2 services, got %d", c.bindIP, len(services))
		}
		for _, svc := range services {
			addr, _ := svc["addr"].(string)
			if addr != c.wantAddr {
				t.Errorf("bindIP %q: service addr = %q, want %q", c.bindIP, addr, c.wantAddr)
			}
		}
	}
}
