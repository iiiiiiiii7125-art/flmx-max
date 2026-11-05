package handler

import (
	"fmt"
	"testing"
)

// B6-01 同族补全：B6-01 的路径面普查只收了证书路径（tls 三字段与
// mitm.*file*），同机制（对端经资源配置/metadata 携带提供方本机文件
// 路径、agent 以 root 读取）还有五族漏网——kcp 配置文件
// （kcp.configFile/configFile/c，kcp 在对端合法类型集内、为活洞）、
// SSH 私钥文件 privateKeyFile 与授权文件 authorizedKeys、file
// handler 的服务目录 file.dir/dir、http 诱饵 probeResist 的 file:
// 值（文件内容直接回给未认证探测者）。本文件钉补全后的全集：可达族
// 走真实命令面红→绿，类型面暂挡住的族以合法信封嵌入证路径面本身
// 在命令面生效、再以校验器直测钉键形变体；合法 KCP 配置（内联
// kcp.config）与空值占位不得误伤。

func b601FamilyService(name string, port int, mutate func(map[string]interface{})) map[string]interface{} {
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

func TestB601FamilyKcpConfigFileRejected(t *testing.T) {
	a := newCleanupAgent(t)
	share := resourceTestShare(t, a.h, "b601fam-kcp-share")

	cases := []struct {
		name  string
		cmd   string
		data  interface{}
		check string
		kind  string
	}{
		{
			name: "listener kcp.configFile",
			cmd:  "AddService",
			data: []interface{}{b601FamilyService("kcpdot", 31031, func(s map[string]interface{}) {
				s["listener"] = map[string]interface{}{"type": "kcp", "metadata": map[string]interface{}{"kcp.configFile": "/etc/passwd"}}
			})},
			check: "kcpdot", kind: "service",
		},
		{
			name: "listener metadata alias c",
			cmd:  "AddService",
			data: []interface{}{b601FamilyService("kcpalias", 31032, func(s map[string]interface{}) {
				s["listener"] = map[string]interface{}{"type": "kcp", "metadata": map[string]interface{}{"c": "/etc/passwd"}}
			})},
			check: "kcpalias", kind: "service",
		},
		{
			name:  "chain dialer kcp configFile",
			cmd:   "AddChains",
			data:  map[string]interface{}{"name": "kcpchain", "hops": []interface{}{map[string]interface{}{"name": "h1", "nodes": []interface{}{map[string]interface{}{"name": "n1", "addr": "127.0.0.1:8080", "connector": map[string]interface{}{"type": "relay"}, "dialer": map[string]interface{}{"type": "kcp", "metadata": map[string]interface{}{"configFile": "/etc/passwd"}}}}}}},
			check: "kcpchain", kind: "chain",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := resourceTestCommand(t, a.h, share, tc.cmd, tc.data)
			if result.Code == 0 {
				t.Fatalf("command carrying a node-local kcp config file path was accepted: %+v", result)
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

// 类型面暂挡住的族（ssh/file/http handler 类型不在对端合法集内）以
// 合法信封（relay handler 的 metadata）嵌入：路径面是与类型面独立的
// 单点，键一出现即应在命令面被拒且不落库——白名单日后放宽时路径面
// 不得随之敞开。
func TestB601FamilyKeyMaterialAndDecoyRejected(t *testing.T) {
	a := newCleanupAgent(t)
	share := resourceTestShare(t, a.h, "b601fam-key-share")

	cases := []struct {
		name string
		svc  string
		port int
		meta map[string]interface{}
	}{
		{name: "ssh privateKeyFile", svc: "sshkey", port: 31041, meta: map[string]interface{}{"privateKeyFile": "/root/.ssh/id_rsa"}},
		{name: "ssh authorizedKeys", svc: "sshauth", port: 31042, meta: map[string]interface{}{"authorizedKeys": "/root/.ssh/authorized_keys"}},
		{name: "probeResist file decoy", svc: "decoyfile", port: 31043, meta: map[string]interface{}{"probeResist": "file:/etc/passwd"}},
		{name: "file handler dir", svc: "filedir", port: 31044, meta: map[string]interface{}{"dir": "/etc"}},
		{name: "file handler file.dir", svc: "filedotdir", port: 31045, meta: map[string]interface{}{"file.dir": "/var/www"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := []interface{}{b601FamilyService(tc.svc, tc.port, func(s map[string]interface{}) {
				s["handler"] = map[string]interface{}{"type": "relay", "metadata": tc.meta}
			})}
			result := resourceTestCommand(t, a.h, share, "AddService", data)
			if result.Code == 0 {
				t.Fatalf("command carrying a node-local file path (%s) was accepted: %+v", tc.name, result)
			}
			stored, err := a.h.repo.GetPeerShareResource(share.ID, "service", tc.svc)
			if err != nil {
				t.Fatal(err)
			}
			if stored != nil {
				t.Fatalf("rejected resource was persisted anyway: %+v", stored)
			}
		})
	}
}

// 校验器直测：钉键形变体（嵌套/大小写/点分）与上下文边界——单字母
// 别名 c 与裸 dir 只在 metadata 上下文内是路径键，metadata 之外同名
// 键不归本面管；probe 的非 file 类型值（web/host）是网络目标、不在
// 本机文件面；空值占位恒放行（与 B6-01 同律）。
func TestB601FamilyValidatorKeyShapes(t *testing.T) {
	reject := []struct {
		name  string
		value interface{}
	}{
		{"nested kcp map", map[string]interface{}{"kcp": map[string]interface{}{"configFile": "/x"}}},
		{"dotted case variant", map[string]interface{}{"KCP.ConfigFile": "/x"}},
		{"alias C under metadata", map[string]interface{}{"metadata": map[string]interface{}{"C": "/x"}}},
		{"nested dir under metadata", map[string]interface{}{"metadata": map[string]interface{}{"file": map[string]interface{}{"dir": "/x"}}}},
		{"probe_resist file", map[string]interface{}{"probe_resist": "file:/x"}},
		{"probeResistance FILE case", map[string]interface{}{"probeResistance": "FILE:/x"}},
		{"privateKeyFile", map[string]interface{}{"privateKeyFile": "/x"}},
		{"authorizedKeys", map[string]interface{}{"authorizedKeys": "/x"}},
	}
	for _, tc := range reject {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			if err := validatePeerResourceLocalPaths(tc.value); err == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
	accept := []struct {
		name  string
		value interface{}
	}{
		{"c outside metadata", map[string]interface{}{"c": "/x"}},
		{"dir outside metadata", map[string]interface{}{"dir": "/x"}},
		{"probeResist web value", map[string]interface{}{"probeResist": "web:https://example.com/"}},
		{"probeResist host value", map[string]interface{}{"probeResist": "host:example.com:443"}},
		{"empty configFile placeholder", map[string]interface{}{"metadata": map[string]interface{}{"kcp.configFile": ""}}},
		{"inline kcp config", map[string]interface{}{"metadata": map[string]interface{}{"config": map[string]interface{}{"mode": "fast"}}}},
	}
	for _, tc := range accept {
		t.Run("accept/"+tc.name, func(t *testing.T) {
			if err := validatePeerResourceLocalPaths(tc.value); err != nil {
				t.Fatalf("unexpected rejection for %s: %v", tc.name, err)
			}
		})
	}
}

// 绿证：合法 KCP 资源（内联参数、无 configFile）与空值占位必须照常
// 受理落库——补全不得误伤对端合法 kcp 隧道面。
func TestB601FamilyLegitKcpAccepted(t *testing.T) {
	a := newCleanupAgent(t)
	share := resourceTestShare(t, a.h, "b601fam-ok-share")

	inline := resourceTestCommand(t, a.h, share, "AddService", []interface{}{b601FamilyService("kcpok", 31051, func(s map[string]interface{}) {
		s["listener"] = map[string]interface{}{"type": "kcp", "metadata": map[string]interface{}{"kcp.mode": "fast", "kcp.crypt": "aes", "kcp.config": map[string]interface{}{"mtu": 1350}}}
	})})
	if inline.Code != 0 {
		t.Fatalf("legit kcp service with inline config rejected: %s", inline.Msg)
	}
	empty := resourceTestCommand(t, a.h, share, "AddService", []interface{}{b601FamilyService("kcpempty", 31052, func(s map[string]interface{}) {
		s["listener"] = map[string]interface{}{"type": "kcp", "metadata": map[string]interface{}{"kcp.configFile": ""}}
	})})
	if empty.Code != 0 {
		t.Fatalf("empty kcp.configFile placeholder rejected: %s", empty.Msg)
	}
	kcpNode := map[string]interface{}{
		"name": "n1", "addr": "127.0.0.1:8080",
		"connector": map[string]interface{}{"type": "relay"},
		"dialer":    map[string]interface{}{"type": "kcp", "metadata": map[string]interface{}{"config": map[string]interface{}{"mode": "fast2"}}},
	}
	kcpChain := map[string]interface{}{
		"name": "kcpchainok",
		"hops": []interface{}{map[string]interface{}{"name": "h1", "nodes": []interface{}{kcpNode}}},
	}
	chain := resourceTestCommand(t, a.h, share, "AddChains", kcpChain)
	if chain.Code != 0 {
		t.Fatalf("legit kcp chain dialer rejected: %s", chain.Msg)
	}
}

// 存量面：植入的 kcp.configFile 存量服务经启动复检必须被置 paused
// （与写入面同一校验全集，B6-06 三面口径的本族验证）。
func TestB601FamilyStoredKcpConfigFilePaused(t *testing.T) {
	a := newCleanupAgent(t)
	share := resourceTestShare(t, a.h, "b601fam-stored-share")

	b606SeedResource(t, a.h, share, "service", "storedkcp", "active", map[string]interface{}{
		"handler":  map[string]interface{}{"type": "tcp"},
		"listener": map[string]interface{}{"type": "kcp", "metadata": map[string]interface{}{"kcp.configFile": "/etc/passwd"}},
	})

	restarted := &Handler{repo: a.h.repo}
	restarted.revalidateStoredPeerShareResources()

	if got := b606DesiredState(t, a.h, share, "service", "storedkcp"); got != "paused" {
		t.Fatalf("stored service with kcp.configFile desired_state = %q, want paused", got)
	}
}
