package handler

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// U7：托管中转入口的 gost 服务带 connlog 标记（kind=forward、
// id=服务名，即 §5.1 的 entry_id）；链式隧道（Type=2）下标记与
// chain 设置共存；普通转发（entry=false）零标记，服务形状与
// 标记功能上线前一致。
func TestBuildForwardServiceConfigs_ConnlogEntryMark(t *testing.T) {
	forward := &forwardRecord{RemoteAddr: "1.2.3.4:80", Strategy: "fifo", TunnelID: 7}
	node := &nodeRecord{TCPListenAddr: "[::]", UDPListenAddr: "[::]"}

	services := buildForwardServiceConfigs("1_2_0", forward, nil, node, 22000, "", forwardRuntimeLimiters{}, true)
	if len(services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(services))
	}
	for _, svc := range services {
		md, _ := svc["metadata"].(map[string]interface{})
		if md[connlogEntryKindMetadataKey] != "forward" {
			t.Fatalf("service %v: %s = %v, want forward", svc["name"], connlogEntryKindMetadataKey, md[connlogEntryKindMetadataKey])
		}
		if md[connlogEntryIDMetadataKey] != svc["name"] {
			t.Fatalf("service %v: %s = %v, want the service name", svc["name"], connlogEntryIDMetadataKey, md[connlogEntryIDMetadataKey])
		}
	}

	chained := buildForwardServiceConfigs("1_2_0", forward, &tunnelRecord{Type: 2}, node, 22000, "", forwardRuntimeLimiters{}, true)
	for _, svc := range chained {
		if svc["handler"].(map[string]interface{})["chain"] == nil {
			t.Fatalf("service %v: chain setting lost", svc["name"])
		}
		md, _ := svc["metadata"].(map[string]interface{})
		if md[connlogEntryKindMetadataKey] != "forward" || md[connlogEntryIDMetadataKey] != svc["name"] {
			t.Fatalf("service %v: connlog mark lost on chained tunnel: %v", svc["name"], md)
		}
	}

	plain := buildForwardServiceConfigs("1_2_0", forward, nil, node, 22000, "", forwardRuntimeLimiters{}, false)
	for _, svc := range plain {
		if md, ok := svc["metadata"].(map[string]interface{}); ok {
			if _, present := md[connlogEntryKindMetadataKey]; present {
				t.Fatalf("service %v: ordinary forward carries an entry mark", svc["name"])
			}
			if _, present := md[connlogEntryIDMetadataKey]; present {
				t.Fatalf("service %v: ordinary forward carries an entry id", svc["name"])
			}
		}
	}
}

// D-01：分流目标（SplitRule action=SplitProxy）是链式目标同形——
// 其腿连接的对端是源入站所在机器，必须进拓扑排除集；只被
// OutboundID 指向的入站与从未被指向的入站作对照。
func TestConnlogChainTargetsIncludeSplitProxy(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/connlog-split.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	node := seedRelayTargetNode(t, r, "cl-split", "198.51.100.5")
	// 新门控：入口标记只下发给采集开关开着的节点。
	if err := r.UpdateNodeConnLogEnabled(node, 1); err != nil {
		t.Fatal(err)
	}
	kern := seedChainKernel(t, r, node, kernel.TypeSingbox)
	vless := func(port int) kernel.Inbound {
		return kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: port,
			Security: kernel.Security{Type: kernel.SecurityNone}}
	}
	splitTarget := seedChainInbound(t, r, kern, "cl-split-target", vless(8443), 0)
	direct := seedChainInbound(t, r, kern, "cl-split-direct", vless(8444), 0)
	src := seedChainInbound(t, r, kern, "cl-split-source", vless(8445), 0)
	rules, err := json.Marshal([]kernel.SplitRule{{Action: kernel.SplitProxy, OutboundID: splitTarget.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`UPDATE node_inbound SET split_rules = ? WHERE id = ?`, string(rules), src.ID).Error; err != nil {
		t.Fatal(err)
	}

	ids, err := h.connlogChainTargetInboundIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !ids[splitTarget.ID] {
		t.Fatalf("split target %d missing from chain-target set: %v", splitTarget.ID, ids)
	}
	if ids[direct.ID] {
		t.Fatalf("direct inbound %d wrongly in chain-target set: %v", direct.ID, ids)
	}

	// 端到端：分流目标在自己载荷的 connlogEntries 里不是入口。
	payload, _, err := h.buildKernelApplyPayload(kern)
	if err != nil {
		t.Fatal(err)
	}
	if _, bad := connlogEntryTags(payload)[splitTarget.ID]; bad {
		t.Fatalf("split target marked as entry: %v", connlogEntryTags(payload))
	}
	if got := connlogEntryTags(payload)[direct.ID]; got == "" {
		t.Fatalf("direct inbound lost its entry mark: %v", connlogEntryTags(payload))
	}
}

// D-05 后端：connLogInbounds（agent 内核日志解析器的 opt-in
// 名单）不得收永无直连记录的入站形态——被中转落地／链式目标／
// 分流目标／导入入站的开关即使开着（节点开关），其行也被
// connlogEntries 排除，交集恒空；只会白开日志级。直连与链式源／
// 分流源是对照。
func TestConnlogOptInListExcludesNeverDirect(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/connlog-optin.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	node := seedRelayTargetNode(t, r, "cl-optin", "198.51.100.6")
	// 新门控：名单只在节点开关开时生成（逐入站开关已撤）。
	if err := r.UpdateNodeConnLogEnabled(node, 1); err != nil {
		t.Fatal(err)
	}
	kern := seedChainKernel(t, r, node, kernel.TypeSingbox)
	vless := func(port int) kernel.Inbound {
		return kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: port,
			Security: kernel.Security{Type: kernel.SecurityNone}}
	}
	direct := seedChainInbound(t, r, kern, "cl-o-direct", vless(8443), 0)
	chainTarget := seedChainInbound(t, r, kern, "cl-o-chain-target", vless(8444), 0)
	chainSource := seedChainInbound(t, r, kern, "cl-o-chain-source", vless(8445), chainTarget.ID)
	splitTarget := seedChainInbound(t, r, kern, "cl-o-split-target", vless(8446), 0)
	splitSource := seedChainInbound(t, r, kern, "cl-o-split-source", vless(8447), 0)
	relayTarget := seedChainInbound(t, r, kern, "cl-o-relay-target", vless(8448), 0)
	imported := seedChainInbound(t, r, kern, "cl-o-imported", vless(8449), 0)
	rules, err := json.Marshal([]kernel.SplitRule{{Action: kernel.SplitProxy, OutboundID: splitTarget.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`UPDATE node_inbound SET split_rules = ? WHERE id = ?`, string(rules), splitSource.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: relayTarget.ID, ForwardID: 777, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`UPDATE node_inbound SET source_id = 7 WHERE id = ?`, imported.ID).Error; err != nil {
		t.Fatal(err)
	}

	payload, _, err := h.buildKernelApplyPayload(kern)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := payload["connLogInbounds"].([]int64)
	want := map[int64]bool{direct.ID: true, chainSource.ID: true, splitSource.ID: true}
	if len(got) != len(want) {
		t.Fatalf("connLogInbounds = %v, want exactly %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("connLogInbounds = %v, want exactly %v", got, want)
		}
	}
}

// U7：入口集合含全部 forward（D-P3-1）：托管中转行（不论 Managed）
// 与全部现存 forward（有入口端口者）的并集——用户自建 forward 无
// 托管中转行，仅凭“全部 forward”面才进采集；只收 Managed==1 会
// 漏掉它们，与“连过这台服务器”的口径有落差。集合在每次调用时按
// 当前 DB 现算——relay 行或端口行删除后同一次查询立即收敛，这
// 是「拓扑变更随重推自动更新、不留陈旧标记」的生成侧保证。
// relay-target 集（落地入站）仍只认 Managed==1（未动）。
func TestConnlogEntrySets_AllForwardsAndConverge(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/connlog.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: 101, ForwardID: 201, Name: "managed", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}
	// Managed 带 GORM default:1，零值经 Create 会被默认值覆盖，
	// 非托管行只能以原生 SQL 造（对应存量/直写数据的形态）。
	if err := r.DB().Exec(`INSERT INTO node_inbound_relay(inbound_id, forward_id, name, share_host, managed, created_time) VALUES(102, 202, 'unmanaged', '', 0, ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	// 用户自建 forward：有入口端口行、无托管中转行（D-P3-1）。
	if err := r.DB().Create(&model.ForwardPort{ForwardID: 203, NodeID: 1, Port: 24444}).Error; err != nil {
		t.Fatal(err)
	}

	forwards, err := h.connlogEntryForwardIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !forwards[201] || !forwards[202] || !forwards[203] {
		t.Fatalf("entry forwards = %v, want 201, 202 and 203", forwards)
	}
	targets, err := h.connlogRelayTargetInboundIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !targets[101] || targets[102] {
		t.Fatalf("relay target inbounds = %v, want only 101", targets)
	}

	if err := r.DB().Exec(`DELETE FROM node_inbound_relay WHERE forward_id = 201`).Error; err != nil {
		t.Fatal(err)
	}
	forwards, err = h.connlogEntryForwardIDs()
	if err != nil {
		t.Fatal(err)
	}
	if forwards[201] {
		t.Fatal("stale entry mark: forward 201 still an entry after its relay was deleted")
	}
	if !forwards[202] || !forwards[203] {
		t.Fatalf("entry forwards = %v, want 202 and 203 to survive relay 201 deletion", forwards)
	}
	// 自建 forward 的端口行删除后亦收敛。
	if err := r.DB().Exec(`DELETE FROM forward_port WHERE forward_id = 203`).Error; err != nil {
		t.Fatal(err)
	}
	forwards, err = h.connlogEntryForwardIDs()
	if err != nil {
		t.Fatal(err)
	}
	if forwards[203] {
		t.Fatal("stale entry mark: forward 203 still an entry after its port was deleted")
	}
	targets, err = h.connlogRelayTargetInboundIDs()
	if err != nil {
		t.Fatal(err)
	}
	if targets[101] {
		t.Fatal("stale relay target: inbound 101 still a landing after its relay was deleted")
	}
}

func connlogEntryTags(payload map[string]interface{}) map[int64]string {
	raw, _ := payload["connlogEntries"].([]map[string]interface{})
	out := make(map[int64]string, len(raw))
	for _, e := range raw {
		id, _ := e["inboundId"].(int64)
		tag, _ := e["tag"].(string)
		out[id] = tag
	}
	return out
}

// U7：KernelApply 载荷的 connlogEntries 按形态生成——直连与链
// 式源入站是入口；被中转落地、链式目标、计量伴生不是；中转删
// 除后重新生成载荷，落地入站收敛为直连入口。
func TestConnlogKernelPayloadEntryMarks(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/connlog-kernel.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	front := seedRelayTargetNode(t, r, "cl-front", "198.51.100.1")
	landing := seedRelayTargetNode(t, r, "cl-landing", "203.0.113.10")
	// 新门控：两节点都开采集，否则标记断言在新门控下恒空。
	if err := r.UpdateNodeConnLogEnabled(front, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateNodeConnLogEnabled(landing, 1); err != nil {
		t.Fatal(err)
	}
	frontXray := seedChainKernel(t, r, front, kernel.TypeXray)
	landingBox := seedChainKernel(t, r, landing, kernel.TypeSingbox)

	// 链式目标在落地机上：它是落地不是入口（对 front 载荷而言
	// 不在册是当然的；它在自己载荷里的身份由 chainTargets 全局
	// 集合决定，下面一并断言）。
	chainTarget := seedChainInbound(t, r, landingBox, "cl-chain-target", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	direct := seedChainInbound(t, r, frontXray, "cl-direct", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, 0)
	chainSource := seedChainInbound(t, r, frontXray, "cl-chain-source", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8445,
		Security: kernel.Security{Type: kernel.SecurityNone}}, chainTarget.ID)
	relayed := seedChainInbound(t, r, frontXray, "cl-relayed", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8444,
		Security: kernel.Security{Type: kernel.SecurityNone}}, 0)
	origin := seedChainInbound(t, r, frontXray, "cl-origin", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8446,
		Security: kernel.Security{Type: kernel.SecurityNone}}, 0)
	companion := *origin
	companion.ID = 0
	companion.Name = "cl-companion"
	companion.Port = 8447
	companion.CompanionOf = origin.ID
	if err := r.CreateNodeInbound(&companion); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: relayed.ID, ForwardID: 777, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}

	payload, _, err := h.buildKernelApplyPayload(frontXray)
	if err != nil {
		t.Fatal(err)
	}
	entries := connlogEntryTags(payload)
	wantTag := func(row *model.NodeInbound) string { return fmt.Sprintf("in-%d", row.ID) }
	if entries[direct.ID] != wantTag(direct) {
		t.Fatalf("direct inbound missing/wrong entry mark: %v", entries)
	}
	if entries[chainSource.ID] != wantTag(chainSource) {
		t.Fatalf("chain source inbound missing/wrong entry mark: %v", entries)
	}
	if entries[origin.ID] != wantTag(origin) {
		t.Fatalf("origin inbound missing/wrong entry mark: %v", entries)
	}
	if _, bad := entries[relayed.ID]; bad {
		t.Fatalf("relayed landing inbound marked as entry: %v", entries)
	}
	if _, bad := entries[companion.ID]; bad {
		t.Fatalf("companion inbound marked as entry: %v", entries)
	}

	// 落地机载荷：链式目标被全局 chainTargets 集合排除——它名
	// 下的连接对端是源机器，不是客户端。
	landingPayload, _, err := h.buildKernelApplyPayload(landingBox)
	if err != nil {
		t.Fatal(err)
	}
	if _, bad := connlogEntryTags(landingPayload)[chainTarget.ID]; bad {
		t.Fatal("chain target inbound marked as entry in its own payload")
	}

	// 拓扑收敛：删掉中转行后重新生成，relayed 变回直连入口。
	if err := r.DB().Exec(`DELETE FROM node_inbound_relay WHERE inbound_id = ?`, relayed.ID).Error; err != nil {
		t.Fatal(err)
	}
	payload, _, err = h.buildKernelApplyPayload(frontXray)
	if err != nil {
		t.Fatal(err)
	}
	if got := connlogEntryTags(payload)[relayed.ID]; got != wantTag(relayed) {
		t.Fatalf("after relay removal, inbound did not converge to direct entry: %v", connlogEntryTags(payload))
	}
}

// 集成对账③：总开关 connlog_enabled 是三面下发的总门——关时
// gost 入口集为空、KernelApply 的 connlogEntries 为空集、
// connLogInbounds 不写键；恢复后三面回到拓扑/节点开关现算结果。
// 不许只停一半。
func TestConnlogMasterSwitchGatesAllMarkFaces(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/connlog-master.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: 101, ForwardID: 201, Name: "managed", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}
	node := seedRelayTargetNode(t, r, "cl-master", "198.51.100.77")
	kern := seedChainKernel(t, r, node, kernel.TypeXray)
	direct := seedChainInbound(t, r, kern, "cl-master-direct", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, 0)
	// 新门控：节点开关开（逐入站开关已撤），三面才随总开关翻转。
	if err := r.UpdateNodeConnLogEnabled(node, 1); err != nil {
		t.Fatal(err)
	}

	assertFaces := func(wantOn bool, stage string) {
		t.Helper()
		forwards, err := h.connlogEntryForwardIDs()
		if err != nil {
			t.Fatal(err)
		}
		if got := forwards[201]; got != wantOn {
			t.Fatalf("%s: gost entry set[201] = %v, want %v", stage, got, wantOn)
		}
		payload, _, err := h.buildKernelApplyPayload(kern)
		if err != nil {
			t.Fatal(err)
		}
		// D-04：渲染面与标记面同门——总开关关时下发的 Xray 配置
		// 不开 access 日志（节点开关开着也不升），开时才开。
		files, _ := payload["files"].([]kernel.File)
		if len(files) == 0 {
			t.Fatalf("%s: payload has no rendered files", stage)
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(files[0].Content), &parsed); err != nil {
			t.Fatal(err)
		}
		logCfg, _ := parsed["log"].(map[string]interface{})
		_, accessPresent := logCfg["access"]
		if accessPresent != wantOn {
			t.Fatalf("%s: rendered log.access present = %v, want %v", stage, accessPresent, wantOn)
		}
		entries := connlogEntryTags(payload)
		if wantOn {
			if entries[direct.ID] == "" {
				t.Fatalf("%s: connlogEntries lost the direct inbound: %v", stage, entries)
			}
			ids, _ := payload["connLogInbounds"].([]int64)
			found := false
			for _, id := range ids {
				if id == direct.ID {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: connLogInbounds = %v, want it to contain %d", stage, payload["connLogInbounds"], direct.ID)
			}
		} else {
			if len(entries) != 0 {
				t.Fatalf("%s: connlogEntries = %v, want empty with the master switch off", stage, entries)
			}
			if _, present := payload["connLogInbounds"]; present {
				t.Fatalf("%s: connLogInbounds key present with the master switch off: %v", stage, payload["connLogInbounds"])
			}
		}
	}

	assertFaces(true, "default (key absent)")
	if err := r.UpsertConfig(repo.ConnLogEnabledConfigKey, "false", now); err != nil {
		t.Fatal(err)
	}
	assertFaces(false, "master off")
	if err := r.UpsertConfig(repo.ConnLogEnabledConfigKey, "true", now); err != nil {
		t.Fatal(err)
	}
	assertFaces(true, "master back on")
}
