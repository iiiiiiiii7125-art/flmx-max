package kernel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// nft table-loss tests (R3A-NEW-1 / C NEW-1): the agent's nft table
// lives in the kernel outside this process and can be deleted
// out-of-band. The family, enumerated mechanically:
//
//   - sync() sig-match + empty desired state returned early without
//     checking the table (A2) — a deleted table read as "consistent".
//   - sync() is only called on Apply/Control, so between pushes a
//     deleted table was never noticed (A3) — the verify loop closes it.
//   - Status() swallowed read() errors and reported empty counters
//     with no error field (A1) — the panel could not tell "no
//     traffic" from "table gone".
//
// The stub below models a live table that can be deleted out-of-band:
// "list table" fails while tableGone, "-f -" (re)creates it, and
// "-j list counters" fails the same way, like the real nft CLI.
type tableLossStub struct {
	mu        sync.Mutex
	tableGone bool
	scripts   []string
}

func stubNftTableLoss(t *testing.T) *tableLossStub {
	t.Helper()
	rec := &tableLossStub{}
	oldRunner, oldAvail := nftRunner, nftAvailable
	nftRunner = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		joined := strings.Join(args, " ")
		switch {
		case joined == "-f -":
			rec.scripts = append(rec.scripts, stdin)
			rec.tableGone = false
			return nil, nil
		case strings.HasPrefix(joined, "list table"):
			if rec.tableGone {
				return nil, errors.New("Error: No such file or directory")
			}
			return []byte("table inet flmx_kernel {}"), nil
		case strings.HasPrefix(joined, "-j list counters"):
			if rec.tableGone {
				return nil, errors.New("Error: No such file or directory")
			}
			return []byte(`{"nftables":[]}`), nil
		case strings.HasPrefix(joined, "list ruleset"):
			return []byte("table inet flmx_kernel {}"), nil
		}
		return nil, errors.New("unexpected nft call " + joined)
	}
	nftAvailable = func() error { return nil }
	t.Cleanup(func() { nftRunner, nftAvailable = oldRunner, oldAvail })
	return rec
}

func (s *tableLossStub) scriptsLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.scripts)
}

func (s *tableLossStub) deleteTable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tableGone = true
}

var tableLossPorts = []Port{{InboundID: 7, Port: 8388, Network: "tcp"}}

// TestNftSyncRebuildsMissingTable pins the C NEW-1 self-heal: a
// table deleted out-of-band after a successful apply is rebuilt by
// the next sync (same desired state), not trusted on signature.
func TestNftSyncRebuildsMissingTable(t *testing.T) {
	rec := stubNftTableLoss(t)
	n := newNftCounters(t.TempDir())
	if err := n.sync(tableLossPorts, []int64{2}, nil); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if got := rec.scriptsLen(); got != 1 {
		t.Fatalf("first sync scripts=%d, want 1", got)
	}
	rec.deleteTable()
	if err := n.sync(tableLossPorts, []int64{2}, nil); err != nil {
		t.Fatalf("sync after table loss: %v", err)
	}
	if got := rec.scriptsLen(); got != 2 {
		t.Fatalf("sync after table loss scripts=%d, want 2 (rebuild)", got)
	}
	if _, _, _, err := n.read(); err != nil {
		t.Fatalf("read after rebuild: %v", err)
	}
}

// TestNftSyncRebuildsMissingEmptyTable pins the A2 leg: the old
// sig-match + !hasContent early return read a deleted table as
// "consistent" and never rebuilt it, while read() kept failing.
// An empty desired state still needs its (empty) table to exist.
func TestNftSyncRebuildsMissingEmptyTable(t *testing.T) {
	rec := stubNftTableLoss(t)
	n := newNftCounters(t.TempDir())
	if err := n.sync(nil, nil, nil); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if got := rec.scriptsLen(); got != 1 {
		t.Fatalf("first sync scripts=%d, want 1", got)
	}
	rec.deleteTable()
	if err := n.sync(nil, nil, nil); err != nil {
		t.Fatalf("sync after table loss: %v", err)
	}
	if got := rec.scriptsLen(); got != 2 {
		t.Fatalf("sync after table loss scripts=%d, want 2 (rebuild)", got)
	}
}

// TestNftSyncSkipsWhenTableIntact is the control for the two rebuild
// tests: a present table with matching content is still a no-op, so
// the fix does not turn every sync into a rebuild.
func TestNftSyncSkipsWhenTableIntact(t *testing.T) {
	rec := stubNftTableLoss(t)
	n := newNftCounters(t.TempDir())
	if err := n.sync(tableLossPorts, []int64{2}, nil); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if err := n.sync(tableLossPorts, []int64{2}, nil); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if got := rec.scriptsLen(); got != 1 {
		t.Fatalf("intact table sync scripts=%d, want 1 (no rebuild)", got)
	}
}

// TestStatusCarriesNftReadError pins the R3A-NEW-1 agent leg: when
// the counter read fails (table gone), Status carries the error in
// NftReadError instead of silently reporting empty counters — and
// clears it again once the read succeeds.
func TestStatusCarriesNftReadError(t *testing.T) {
	rec := stubNftTableLoss(t)
	m := NewManager(t.TempDir())
	m.instances["singbox"] = &instance{
		req:  ApplyRequest{Type: "singbox", Enabled: true, Ports: tableLossPorts, UserMarks: []int64{2}},
		logs: &logRing{},
	}
	if err := m.nft.sync(tableLossPorts, []int64{2}, nil); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	st := m.Status("")
	if st.NftReadError != "" {
		t.Fatalf("healthy status carries NftReadError=%q, want empty", st.NftReadError)
	}
	rec.deleteTable()
	st = m.Status("")
	if st.NftReadError == "" {
		t.Fatal("status after table loss carries no NftReadError")
	}
	if !strings.Contains(st.NftReadError, "No such file or directory") {
		t.Fatalf("NftReadError=%q, want the nft error verbatim", st.NftReadError)
	}
	if len(st.Counters) != 0 || len(st.Users) != 0 {
		t.Fatalf("status after table loss counters=%v users=%v, want empty", st.Counters, st.Users)
	}
	if err := m.nft.sync(tableLossPorts, []int64{2}, nil); err != nil {
		t.Fatalf("rebuild sync: %v", err)
	}
	if st := m.Status(""); st.NftReadError != "" {
		t.Fatalf("status after rebuild carries NftReadError=%q, want empty", st.NftReadError)
	}
}

// TestVerifyNftTableRebuildsMissingTable pins the A3 leg: the
// periodic self-check rebuilds a table deleted between applies,
// and stays silent (no nft call at all) when nothing was ever
// applied.
func TestVerifyNftTableRebuildsMissingTable(t *testing.T) {
	rec := stubNftTableLoss(t)
	m := NewManager(t.TempDir())
	m.verifyNftTable()
	if got := rec.scriptsLen(); got != 0 {
		t.Fatalf("verify with no applied state scripts=%d, want 0", got)
	}
	m.instances["singbox"] = &instance{
		req:  ApplyRequest{Type: "singbox", Enabled: true, Ports: tableLossPorts, UserMarks: []int64{2}},
		logs: &logRing{},
	}
	if err := m.nft.sync(tableLossPorts, []int64{2}, nil); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	m.verifyNftTable()
	if got := rec.scriptsLen(); got != 1 {
		t.Fatalf("verify with intact table scripts=%d, want 1 (no rebuild)", got)
	}
	rec.deleteTable()
	m.verifyNftTable()
	if got := rec.scriptsLen(); got != 2 {
		t.Fatalf("verify after table loss scripts=%d, want 2 (rebuild)", got)
	}
}
