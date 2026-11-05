package handler

import (
	"fmt"
	"testing"
)

// B6-01：对端资源下发面此前只过引用/类型两道校验，TLS/mitm 证书文件
// 路径（listener/handler/connector/dialer 的 tls{certFile,keyFile,
// caFile} 与 handler metadata 的 mitm.*File）无人拦截——提供方 agent
// 以 root 按路径 LoadX509KeyPair，等于把节点本机密钥交给持 share
// token 的对端使用并附带文件存在性探针。本文件以真实命令面钉修复：
// 携带非空路径字段的资源必须被 fail-closed 拒绝且不落库；正常资源
// 与空字符串占位（gost 侧不构成加载）不受影响。

func b601Service(name string, port int, mutate func(map[string]interface{})) map[string]interface{} {
	svc := map[string]interface{}{
		"name":     name,
		"addr":     fmt.Sprintf(":%d", port),
		"handler":  map[string]interface{}{"type": "tcp"},
		"listener": map[string]interface{}{"type": "tcp"},
	}
	if mutate != nil {
		mutate(svc)
	}
	return svc
}

func TestB601PeerResourceCertFilePathsRejected(t *testing.T) {
	a := newCleanupAgent(t)
	share := resourceTestShare(t, a.h, "b601-share")

	cases := []struct {
		name  string
		cmd   string
		data  interface{}
		check string // 期望被拒后仍不存在的资源名
		kind  string
	}{
		{
			name: "listener tls certFile/keyFile",
			cmd:  "AddService",
			data: []interface{}{b601Service("tlsfile", 31011, func(s map[string]interface{}) {
				s["listener"] = map[string]interface{}{"type": "tls", "tls": map[string]interface{}{"certFile": "/etc/flmx_agent/cert.pem", "keyFile": "/etc/flmx_agent/key.pem"}}
			})},
			check: "tlsfile", kind: "service",
		},
		{
			name: "handler metadata mitm.certFile",
			cmd:  "AddService",
			data: []interface{}{b601Service("mitmfile", 31012, func(s map[string]interface{}) {
				s["handler"] = map[string]interface{}{"type": "relay", "metadata": map[string]interface{}{"mitm.certFile": "/root/cert.pem", "mitm.keyFile": "/root/key.pem"}}
			})},
			check: "mitmfile", kind: "service",
		},
		{
			name: "case variant CertFile",
			cmd:  "AddService",
			data: []interface{}{b601Service("casefile", 31013, func(s map[string]interface{}) {
				s["listener"] = map[string]interface{}{"type": "tls", "tls": map[string]interface{}{"CertFile": "/tmp/c.pem", "KeyFile": "/tmp/k.pem"}}
			})},
			check: "casefile", kind: "service",
		},
		{
			name: "chain dialer tls caFile",
			cmd:  "AddChains",
			data: map[string]interface{}{"name": "chainfile", "hops": []interface{}{map[string]interface{}{"name": "h1", "nodes": []interface{}{map[string]interface{}{"name": "n1", "addr": "127.0.0.1:8080", "connector": map[string]interface{}{"type": "relay"}, "dialer": map[string]interface{}{"type": "tls", "tls": map[string]interface{}{"caFile": "/etc/ssl/certs/ca.pem"}}}}}}},
			check: "chainfile", kind: "chain",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := resourceTestCommand(t, a.h, share, tc.cmd, tc.data)
			if result.Code == 0 {
				t.Fatalf("command carrying node-local cert file paths was accepted: %+v", result)
			}
			stored, err := a.h.repo.GetPeerShareResource(share.ID, tc.kind, tc.check)
			if err != nil {
				t.Fatal(err)
			}
			if stored != nil {
				t.Fatalf("rejected resource was persisted anyway: %+v", stored)
			}
		})
	}
}

func TestB601PeerResourcePlainAndEmptyPathAccepted(t *testing.T) {
	a := newCleanupAgent(t)
	share := resourceTestShare(t, a.h, "b601-ok-share")

	plain := resourceTestCommand(t, a.h, share, "AddService", []interface{}{b601Service("plain", 31021, nil)})
	if plain.Code != 0 {
		t.Fatalf("plain service rejected: %s", plain.Msg)
	}
	// 空字符串路径在 gost 侧不构成文件加载（与 hooks/引用校验的
	// 「空值放行」口径一致），不得误杀。
	empty := resourceTestCommand(t, a.h, share, "AddService", []interface{}{b601Service("emptytls", 31022, func(s map[string]interface{}) {
		s["listener"] = map[string]interface{}{"type": "tls", "tls": map[string]interface{}{"certFile": "", "keyFile": ""}}
	})})
	if empty.Code != 0 {
		t.Fatalf("empty cert path placeholder rejected: %s", empty.Msg)
	}
}
