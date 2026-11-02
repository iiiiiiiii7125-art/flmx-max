package handler

import "testing"

func TestBuildForwardPortEntriesWithPreservedInIP(t *testing.T) {
	entryNodeIDs := []int64{10, 20, 30}
	oldPorts := []forwardPortRecord{
		{NodeID: 10, Port: 10001, InIP: ""},
		{NodeID: 10, Port: 10002, InIP: "10.0.0.10"},
		{NodeID: 20, Port: 10003, InIP: "10.0.0.20"},
	}

	entries := buildForwardPortEntriesWithPreservedInIP(entryNodeIDs, oldPorts, 18080)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// C-F3-01 起端口逐节点沿用现口（同节点多行后行覆盖，与
	// syncTunnelForwardsEntryPorts 的 oldPortByNode 同口径），只有
	// 无存量行的节点 30 用传入的规范口 18080；InIP 保留口径不变。
	if entries[0].NodeID != 10 || entries[0].Port != 10002 || entries[0].InIP != "10.0.0.10" {
		t.Fatalf("unexpected first entry: %+v", entries[0])
	}
	if entries[1].NodeID != 20 || entries[1].Port != 10003 || entries[1].InIP != "10.0.0.20" {
		t.Fatalf("unexpected second entry: %+v", entries[1])
	}
	if entries[2].NodeID != 30 || entries[2].Port != 18080 || entries[2].InIP != "" {
		t.Fatalf("unexpected third entry: %+v", entries[2])
	}
}

func TestBuildForwardPortEntriesWithPreservedInIP_EmptyOldPorts(t *testing.T) {
	entryNodeIDs := []int64{99}
	entries := buildForwardPortEntriesWithPreservedInIP(entryNodeIDs, nil, 17000)

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].NodeID != 99 || entries[0].Port != 17000 || entries[0].InIP != "" {
		t.Fatalf("unexpected entry: %+v", entries[0])
	}
}
