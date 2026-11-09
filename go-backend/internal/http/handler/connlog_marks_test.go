package handler

import (
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

// U7：入口集合只认 Managed=1 的 relay 行（托管口径）；集合在每
// 次调用时按当前 DB 现算——relay 行删除后同一次查询立即收敛，
// 这是「拓扑变更随重推自动更新、不留陈旧标记」的生成侧保证。
func TestConnlogEntrySets_ManagedOnlyAndConverge(t *testing.T) {
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

	forwards, err := h.connlogEntryForwardIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !forwards[201] || forwards[202] {
		t.Fatalf("entry forwards = %v, want only 201", forwards)
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
