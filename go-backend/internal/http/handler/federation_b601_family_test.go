package handler

import (
	"encoding/json"
	"fmt"
	"testing"

	"go-backend/internal/store/model"
)

// B6-01 同族补全：B6-01 的路径面普查只收了证书路径（tls 三字段与
// mitm.*file*），同机制（对端经资源配置/metadata 携带提供方本机文件
// 路径、agent 以 root 读取）还有五族漏网——kcp 配置文件
// （kcp.configFile/configFile/c，kcp 在对端合法类型集内、为活洞）、
// SSH 私钥文件 privateKeyFile 与授权文件 authorizedKeys、file
// handler 的服务目录 file.dir/dir、http 诱饵 probeResist 的 file:
// 值（文件内容直接回给未认证探测者）。R2-07-01 补登第六族 netns/
// netns.out：对端经 service/hop metadata 或 node 字段携带命名空间
// 引用，agent parsing 层以 GetFromPath 打开任意路径并 Set 后在该
// 命名空间内建监听/出站（与文件族同机制：对端配置命名提供方主机
// 资源、root agent 消费；面板自身从不下发 netns，后端全仓零命中）。
// FIX-R2-7（§14 按来源收口）续收第七族 interface：peer 载荷裸键名
// 非空即拒；面板以节点记录 InterfaceName 经 federation 生命周期
// 物化下发的合法流与本面按来源分离（校验器只见 peer 来源的
// PeerShareResource.Config），生命周期回归同组钉住不断。同批续
// 收第八族 so_mark/sockopts.mark（数值型策略值：对端替提供方设
// 出站 SO_MARK 路由标记；面板自有流零命中，按有效值直接拒）。
// 本文件钉补全后的全集：可达族
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
		{"service metadata netns path", map[string]interface{}{"metadata": map[string]interface{}{"netns": "/proc/1/ns/net"}}},
		{"netns name form", map[string]interface{}{"netns": "blue"}},
		{"netns.out dotted", map[string]interface{}{"metadata": map[string]interface{}{"netns.out": "/x"}}},
		{"NetNS case variant", map[string]interface{}{"NetNS": "/x"}},
		{"node field netns", map[string]interface{}{"nodes": []interface{}{map[string]interface{}{"netns": "/x"}}}},
		{"service metadata interface", map[string]interface{}{"metadata": map[string]interface{}{"interface": "eth0"}}},
		{"Interface case variant", map[string]interface{}{"Interface": "eth0"}},
		{"node field interface", map[string]interface{}{"nodes": []interface{}{map[string]interface{}{"interface": "eth0"}}}},
		{"metadata so_mark number", map[string]interface{}{"metadata": map[string]interface{}{"so_mark": float64(42)}}},
		{"metadata so_mark string", map[string]interface{}{"metadata": map[string]interface{}{"so_mark": "42"}}},
		{"So_Mark case variant", map[string]interface{}{"metadata": map[string]interface{}{"So_Mark": 1}}},
		{"metadata so_mark bool", map[string]interface{}{"metadata": map[string]interface{}{"so_mark": true}}},
		{"sockopts mark", map[string]interface{}{"sockopts": map[string]interface{}{"mark": 3}}},
		{"node sockopts Mark case variant", map[string]interface{}{"hops": []interface{}{map[string]interface{}{"nodes": []interface{}{map[string]interface{}{"sockopts": map[string]interface{}{"Mark": 3}}}}}}},
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
		{"empty netns placeholder", map[string]interface{}{"metadata": map[string]interface{}{"netns": ""}}},
		{"empty netns.out placeholder", map[string]interface{}{"metadata": map[string]interface{}{"netns.out": ""}}},
		{"empty interface placeholder", map[string]interface{}{"metadata": map[string]interface{}{"interface": ""}}},
		{"so_mark zero placeholder", map[string]interface{}{"metadata": map[string]interface{}{"so_mark": 0}}},
		{"so_mark zero string placeholder", map[string]interface{}{"metadata": map[string]interface{}{"so_mark": "0"}}},
		{"so_mark negative inert", map[string]interface{}{"metadata": map[string]interface{}{"so_mark": float64(-1)}}},
		{"sockopts mark zero placeholder", map[string]interface{}{"sockopts": map[string]interface{}{"mark": 0}}},
		{"mark outside sockopts", map[string]interface{}{"mark": 9}},
		{"metadata bare mark inert", map[string]interface{}{"metadata": map[string]interface{}{"mark": 9}}},
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

// R2-07-01：netns 族四入形（service metadata 的 netns 与 netns.out、
// hop metadata 的 netns、node 字段 netns）在命令面全数被拒且不落库。
// 值形覆盖路径形（/proc/1/ns/net）与命名形（GetFromName 拼
// /var/run/netns/<名>）——两形都命名提供方主机的网络命名空间。
func TestB601FamilyNetnsRejected(t *testing.T) {
	a := newCleanupAgent(t)
	share := resourceTestShare(t, a.h, "b601fam-netns-share")

	cases := []struct {
		name  string
		cmd   string
		data  interface{}
		check string
		kind  string
	}{
		{
			name: "service metadata netns path",
			cmd:  "AddService",
			data: []interface{}{b601FamilyService("netnssvc", 31061, func(s map[string]interface{}) {
				s["metadata"] = map[string]interface{}{"netns": "/proc/1/ns/net"}
			})},
			check: "netnssvc", kind: "service",
		},
		{
			name: "service metadata netns.out name",
			cmd:  "AddService",
			data: []interface{}{b601FamilyService("netnsout", 31062, func(s map[string]interface{}) {
				s["metadata"] = map[string]interface{}{"netns.out": "blue"}
			})},
			check: "netnsout", kind: "service",
		},
		{
			name:  "chain hop metadata netns",
			cmd:   "AddChains",
			data:  map[string]interface{}{"name": "netnshop", "hops": []interface{}{map[string]interface{}{"name": "h1", "metadata": map[string]interface{}{"netns": "/var/run/netns/tenant"}, "nodes": []interface{}{map[string]interface{}{"name": "n1", "addr": "127.0.0.1:8080", "connector": map[string]interface{}{"type": "relay"}, "dialer": map[string]interface{}{"type": "tcp"}}}}}},
			check: "netnshop", kind: "chain",
		},
		{
			name:  "chain node field netns",
			cmd:   "AddChains",
			data:  map[string]interface{}{"name": "netnsnode", "hops": []interface{}{map[string]interface{}{"name": "h1", "nodes": []interface{}{map[string]interface{}{"name": "n1", "addr": "127.0.0.1:8080", "netns": "/proc/1/ns/net", "connector": map[string]interface{}{"type": "relay"}, "dialer": map[string]interface{}{"type": "tcp"}}}}}},
			check: "netnsnode", kind: "chain",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := resourceTestCommand(t, a.h, share, tc.cmd, tc.data)
			if result.Code == 0 {
				t.Fatalf("command carrying a node-local netns reference was accepted: %+v", result)
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

// 存量面：植入 netns 的存量服务经启动复检必须被置 paused（同一校验
// 全集的本族验证，与 kcp 存量例同形）。
func TestB601FamilyStoredNetnsPaused(t *testing.T) {
	a := newCleanupAgent(t)
	share := resourceTestShare(t, a.h, "b601fam-netns-stored-share")

	b606SeedResource(t, a.h, share, "service", "storednetns", "active", map[string]interface{}{
		"handler":  map[string]interface{}{"type": "tcp"},
		"listener": map[string]interface{}{"type": "tcp"},
		"metadata": map[string]interface{}{"netns": "/proc/1/ns/net"},
	})

	restarted := &Handler{repo: a.h.repo}
	restarted.revalidateStoredPeerShareResources()

	if got := b606DesiredState(t, a.h, share, "service", "storednetns"); got != "paused" {
		t.Fatalf("stored service with netns desired_state = %q, want paused", got)
	}
}

// FIX-R2-7（§14 按来源收口）：interface 族四入形（service metadata、
// service 结构体字段、hop metadata、node 结构体字段）在命令面全数
// 被拒且不落库。对端任意携带 interface 即以 InterfaceByName 做存
// 在性预言机并选择绑定位置；面板自有合法流（节点记录
// InterfaceName 经生命周期物化）不经本面，见生命周期回归例。
func TestB601FamilyInterfaceRejected(t *testing.T) {
	a := newCleanupAgent(t)
	share := resourceTestShare(t, a.h, "b601fam-iface-share")

	cases := []struct {
		name  string
		cmd   string
		data  interface{}
		check string
		kind  string
	}{
		{
			name: "service metadata interface",
			cmd:  "AddService",
			data: []interface{}{b601FamilyService("ifacesvc", 31071, func(s map[string]interface{}) {
				s["metadata"] = map[string]interface{}{"interface": "eth0"}
			})},
			check: "ifacesvc", kind: "service",
		},
		{
			name: "service field interface",
			cmd:  "AddService",
			data: []interface{}{b601FamilyService("ifacefield", 31072, func(s map[string]interface{}) {
				s["interface"] = "eth0"
			})},
			check: "ifacefield", kind: "service",
		},
		{
			name:  "chain hop metadata interface",
			cmd:   "AddChains",
			data:  map[string]interface{}{"name": "ifacehop", "hops": []interface{}{map[string]interface{}{"name": "h1", "metadata": map[string]interface{}{"interface": "eth0"}, "nodes": []interface{}{map[string]interface{}{"name": "n1", "addr": "127.0.0.1:8080", "connector": map[string]interface{}{"type": "relay"}, "dialer": map[string]interface{}{"type": "tcp"}}}}}},
			check: "ifacehop", kind: "chain",
		},
		{
			name:  "chain node field interface",
			cmd:   "AddChains",
			data:  map[string]interface{}{"name": "ifacenode", "hops": []interface{}{map[string]interface{}{"name": "h1", "nodes": []interface{}{map[string]interface{}{"name": "n1", "addr": "127.0.0.1:8080", "interface": "eth0", "connector": map[string]interface{}{"type": "relay"}, "dialer": map[string]interface{}{"type": "tcp"}}}}}},
			check: "ifacenode", kind: "chain",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := resourceTestCommand(t, a.h, share, tc.cmd, tc.data)
			if result.Code == 0 {
				t.Fatalf("command carrying a node-local interface binding was accepted: %+v", result)
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

// 存量面：植入 interface 的存量服务经启动复检必须被置 paused
// （同一校验全集的本族验证，与 netns 存量例同形）。
func TestB601FamilyStoredInterfacePaused(t *testing.T) {
	a := newCleanupAgent(t)
	share := resourceTestShare(t, a.h, "b601fam-iface-stored-share")

	b606SeedResource(t, a.h, share, "service", "storediface", "active", map[string]interface{}{
		"handler":  map[string]interface{}{"type": "tcp"},
		"listener": map[string]interface{}{"type": "tcp"},
		"metadata": map[string]interface{}{"interface": "eth0"},
	})

	restarted := &Handler{repo: a.h.repo}
	restarted.revalidateStoredPeerShareResources()

	if got := b606DesiredState(t, a.h, share, "service", "storediface"); got != "paused" {
		t.Fatalf("stored service with interface desired_state = %q, want paused", got)
	}
}

// §14 来源分离的回归锚：面板以提供方节点记录的 InterfaceName 经
// 联邦生命周期物化下发 interface（exit 服务 metadata、middle 链
// hop 字段）不经 peer 资源校验器——本面收口后合法流必须照常。
// 经真实 applyPeerShareRoleRuntime 下发并断言捕获到的命令载荷。
func TestB601FamilyInterfaceLifecycleIntact(t *testing.T) {
	for _, role := range []string{"exit", "middle"} {
		t.Run(role, func(t *testing.T) {
			agent := newCleanupAgent(t)
			if err := agent.h.repo.DB().Model(&model.Node{}).Where("id = ?", 1).Update("interface_name", "ens224").Error; err != nil {
				t.Fatal(err)
			}
			runtime := roleRuntimeFixture(t, agent, role)
			if err := agent.h.applyPeerShareRoleRuntime(runtime); err != nil {
				t.Fatalf("applyPeerShareRoleRuntime: %v", err)
			}
			if role == "exit" {
				cmds := agent.commandsOfType("UpdateService")
				if len(cmds) == 0 {
					t.Fatal("no UpdateService command captured")
				}
				var services []map[string]interface{}
				if err := json.Unmarshal(cmds[len(cmds)-1].Data, &services); err != nil || len(services) == 0 {
					t.Fatalf("decode UpdateService data %s: %v", string(cmds[len(cmds)-1].Data), err)
				}
				md, _ := services[0]["metadata"].(map[string]interface{})
				if got, _ := md["interface"].(string); got != "ens224" {
					t.Fatalf("exit service metadata interface = %q, want provider node InterfaceName %q", got, "ens224")
				}
				return
			}
			cmds := agent.commandsOfType("UpdateChains")
			if len(cmds) == 0 {
				t.Fatal("no UpdateChains command captured")
			}
			var payload map[string]interface{}
			if err := json.Unmarshal(cmds[len(cmds)-1].Data, &payload); err != nil {
				t.Fatalf("decode UpdateChains data %s: %v", string(cmds[len(cmds)-1].Data), err)
			}
			data, _ := payload["data"].(map[string]interface{})
			hops, _ := data["hops"].([]interface{})
			if len(hops) == 0 {
				t.Fatalf("chain payload has no hops: %s", string(cmds[len(cmds)-1].Data))
			}
			hop0, _ := hops[0].(map[string]interface{})
			if got, _ := hop0["interface"].(string); got != "ens224" {
				t.Fatalf("middle chain hop interface = %q, want provider node InterfaceName %q", got, "ens224")
			}
		})
	}
}

// FIX-R2-7：so_mark / sockopts.mark 两入形（service metadata 的
// so_mark 数值与数字字符串形、结构体 sockopts 的 mark）在命令面
// 全数被拒且不落库——对端借此替提供方节点出站连接打 SO_MARK
// 策略路由标记，面板自有流从不设 Mark。0 值在消费面惰性
// （parsing 门 v>0），作对照必须照常受理落库。
func TestB601FamilySoMarkRejected(t *testing.T) {
	a := newCleanupAgent(t)
	share := resourceTestShare(t, a.h, "b601fam-mark-share")

	cases := []struct {
		name  string
		cmd   string
		data  interface{}
		check string
		kind  string
	}{
		{
			name: "service metadata so_mark number",
			cmd:  "AddService",
			data: []interface{}{b601FamilyService("marksoc", 31081, func(s map[string]interface{}) {
				s["metadata"] = map[string]interface{}{"so_mark": 123}
			})},
			check: "marksoc", kind: "service",
		},
		{
			name: "service metadata so_mark string",
			cmd:  "AddService",
			data: []interface{}{b601FamilyService("markstr", 31082, func(s map[string]interface{}) {
				s["metadata"] = map[string]interface{}{"so_mark": "123"}
			})},
			check: "markstr", kind: "service",
		},
		{
			name: "service sockopts mark",
			cmd:  "AddService",
			data: []interface{}{b601FamilyService("markfield", 31083, func(s map[string]interface{}) {
				s["sockopts"] = map[string]interface{}{"mark": 7}
			})},
			check: "markfield", kind: "service",
		},
		{
			name:  "chain node sockopts mark",
			cmd:   "AddChains",
			data:  map[string]interface{}{"name": "marknode", "hops": []interface{}{map[string]interface{}{"name": "h1", "nodes": []interface{}{map[string]interface{}{"name": "n1", "addr": "127.0.0.1:8080", "sockopts": map[string]interface{}{"mark": 7}, "connector": map[string]interface{}{"type": "relay"}, "dialer": map[string]interface{}{"type": "tcp"}}}}}},
			check: "marknode", kind: "chain",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := resourceTestCommand(t, a.h, share, tc.cmd, tc.data)
			if result.Code == 0 {
				t.Fatalf("command carrying a socket mark for the provider node was accepted: %+v", result)
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

	zeroMeta := resourceTestCommand(t, a.h, share, "AddService", []interface{}{b601FamilyService("markzero", 31084, func(s map[string]interface{}) {
		s["metadata"] = map[string]interface{}{"so_mark": 0}
	})})
	if zeroMeta.Code != 0 {
		t.Fatalf("zero so_mark placeholder rejected: %s", zeroMeta.Msg)
	}
	zeroField := resourceTestCommand(t, a.h, share, "AddService", []interface{}{b601FamilyService("markzerofield", 31085, func(s map[string]interface{}) {
		s["sockopts"] = map[string]interface{}{"mark": 0}
	})})
	if zeroField.Code != 0 {
		t.Fatalf("zero sockopts mark placeholder rejected: %s", zeroField.Msg)
	}
}

// 存量面：植入 so_mark 的存量服务经启动复检必须被置 paused
// （同一校验全集的本族验证，与 interface 存量例同形）。
func TestB601FamilyStoredSoMarkPaused(t *testing.T) {
	a := newCleanupAgent(t)
	share := resourceTestShare(t, a.h, "b601fam-mark-stored-share")

	b606SeedResource(t, a.h, share, "service", "storedmark", "active", map[string]interface{}{
		"handler":  map[string]interface{}{"type": "tcp"},
		"listener": map[string]interface{}{"type": "tcp"},
		"metadata": map[string]interface{}{"so_mark": 99},
	})

	restarted := &Handler{repo: a.h.repo}
	restarted.revalidateStoredPeerShareResources()

	if got := b606DesiredState(t, a.h, share, "service", "storedmark"); got != "paused" {
		t.Fatalf("stored service with so_mark desired_state = %q, want paused", got)
	}
}
