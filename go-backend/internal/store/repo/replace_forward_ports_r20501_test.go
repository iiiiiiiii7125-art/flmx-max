package repo

import (
	"testing"
	"time"
)

// R2-05-01 回归：存量重复 chain_type=1 行（备份恢复/遗留可达）使
// TunnelEntryNodeIDs 返回重复入口节点时，三处 ReplaceForwardPorts
// 消费点按未去重列表逐行建条目，replaceForwardPortsTx 又排除转发
// 自身做占用复检，于是同一转发被写出重复 forward_port 行（无唯
// 一索引兜底）。修复须在 replaceForwardPortsTx 入口按节点去重保
// 首现序：同一节点只落一行、首现条目的端口/监听 IP 胜出。

type r20501Entry = struct {
	NodeID int64
	Port   int
	InIP   string
}

func r20501SeedForward(t *testing.T, r *Repository, name string, port int) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	id, err := r.CreateForwardTx(1, "test", name, 0, "", "fifo", now, 0, []int64{1}, port, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("seed forward %s: %v", name, err)
	}
	return id
}

func r20501AssertRows(t *testing.T, r *Repository, forwardID int64, want []r20501Entry) {
	t.Helper()
	rows, err := r.ListForwardPorts(forwardID)
	if err != nil {
		t.Fatalf("list forward ports: %v", err)
	}
	if len(rows) != len(want) {
		t.Fatalf("forward_port rows = %v, want exactly %v (no duplicate node rows)", rows, want)
	}
	for i, w := range want {
		if rows[i].NodeID != w.NodeID || rows[i].Port != w.Port || rows[i].InIP != w.InIP {
			t.Fatalf("row %d = %+v, want %+v (first occurrence must win, order preserved)", i, rows[i], w)
		}
	}
}

func TestReplaceForwardPortsDedupesDuplicateEntryNodes(t *testing.T) {
	r := setupB4TestDB(t)
	forwardID := r20501SeedForward(t, r, "fwd-r20501", 32400)

	entries := []r20501Entry{
		{NodeID: 7, Port: 32410, InIP: ""},
		{NodeID: 7, Port: 32411, InIP: "10.0.0.9"},
		{NodeID: 8, Port: 32412, InIP: ""},
		{NodeID: 8, Port: 32413, InIP: ""},
	}
	if err := r.ReplaceForwardPorts(forwardID, entries); err != nil {
		t.Fatalf("replace forward ports with duplicated entry nodes: %v", err)
	}
	r20501AssertRows(t, r, forwardID, []r20501Entry{
		{NodeID: 7, Port: 32410, InIP: ""},
		{NodeID: 8, Port: 32412, InIP: ""},
	})
}

func TestChangeForwardTunnelAndPortsDedupesDuplicateEntryNodes(t *testing.T) {
	r := setupB4TestDB(t)
	forwardID := r20501SeedForward(t, r, "fwd-r20501-bct", 32500)
	now := time.Now().UnixMilli()

	entries := []r20501Entry{
		{NodeID: 9, Port: 32510, InIP: ""},
		{NodeID: 9, Port: 32511, InIP: ""},
		{NodeID: 10, Port: 32512, InIP: ""},
	}
	if err := r.ChangeForwardTunnelAndPorts(forwardID, 0, "", entries, now); err != nil {
		t.Fatalf("change forward tunnel and ports with duplicated entry nodes: %v", err)
	}
	r20501AssertRows(t, r, forwardID, []r20501Entry{
		{NodeID: 9, Port: 32510, InIP: ""},
		{NodeID: 10, Port: 32512, InIP: ""},
	})
}
