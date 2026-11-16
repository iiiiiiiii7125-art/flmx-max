package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R4B-01: the nft reject for a truncated user must survive a table
// rebuild after the sweeper half of the tombstone is spent (a
// reload/restart refreshes the running config) and after an agent
// restart. The old code kept the blocked set only in memory
// (inst.tombstones) and spent it on config refresh, so the next
// rebuild silently dropped the reject while the panel still showed
// the truncation active (applied=0). The fix splits the tombstone:
// the sweeper half stays transient, the reject half (blockedMarks)
// is durable until the user is re-added or the instance disabled,
// and is persisted to blocked.json beside state.json.

func writeSingboxConfigUsers(t *testing.T, runDir string, users ...string) {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(`{"inbounds":[{"users":[`)
	for i, u := range users {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"name":"` + u + `"}`)
	}
	sb.WriteString(`]}]}`)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir runDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "config.json"), []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

// TestRestorePreservesBlockedMarks is the R4B-01d live repro at unit
// level: it drives the real Restore() with a confirming Apply. A
// confirmed Apply persists, and before the fix that persist —
// running inside restore()'s re-apply loop with the still-empty
// in-memory set — overwrote blocked.json before it was read, so
// the agent came back from a restart with blocked.json={} and no
// reject (live leg R4B-01d).
func TestRestorePreservesBlockedMarks(t *testing.T) {
	rec := stubNft(t)
	srv, downloads := serveKernel(t, zipWith(t, map[string]string{"xray": fakeKernel}))
	dir := t.TempDir()
	m := NewManager(dir)
	defer m.Shutdown()
	req := applyRequest(srv, `{"v":1}`, true)
	req.UserMarks = []int64{1, 2}
	if _, err := m.Apply(req); err != nil {
		t.Fatalf("seed apply: %v", err)
	}
	// Truncation: the durable reject half is set and persisted.
	m.instances["xray"].blockedMarks = map[string]bool{"u2": true}
	m.instances["xray"].tombstones = map[string]bool{"u2": true}
	m.persist()
	// Fresh manager on the same directory: the real restart path.
	m2 := NewManager(dir)
	defer m2.Shutdown()
	m2.Restore()
	if *downloads != 1 {
		t.Fatalf("restore must not re-download: %d", *downloads)
	}
	got := m2.allBlockedUserMarks()
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("allBlockedUserMarks() after restore = %v, want [2]", got)
	}
	// The file itself must still hold the mark: a persist during the
	// restore Apply loop must not have clobbered it.
	b, err := os.ReadFile(filepath.Join(dir, "blocked.json"))
	if err != nil {
		t.Fatalf("blocked.json missing after restore: %v", err)
	}
	if !strings.Contains(string(b), `"u2"`) {
		t.Fatalf("blocked.json after restore = %s, want u2", b)
	}
	// The restored reject must be rendered, not only held in memory.
	last := rec.scripts[len(rec.scripts)-1]
	if !strings.Contains(last, "meta mark 0x46560002 reject") {
		t.Fatalf("restore must render the reject:\n%s", last)
	}
	// The sweeper half is NOT restored.
	if len(m2.instances["xray"].tombstones) != 0 {
		t.Fatalf("sweeper half must not be restored, tombstones=%v", m2.instances["xray"].tombstones)
	}
}

func TestReconcileTombstonesRefreshKeepsBlocked(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	writeSingboxConfigUsers(t, filepath.Join(dir, "singbox", "run"), "u1") // u1 live, u2 removed
	inst := &instance{
		req:          ApplyRequest{Type: "singbox", Enabled: true},
		tombstones:   map[string]bool{"u2": true},
		blockedMarks: map[string]bool{"u2": true},
		logs:         &logRing{},
	}
	// A reload/restart refreshes the running config: the sweeper
	// half is spent, the reject half stays — the truncation is
	// still active panel-side.
	m.reconcileTombstones(inst, inst.req, true)
	if len(inst.tombstones) != 0 {
		t.Fatalf("refresh must spend the sweeper half, tombstones=%v", inst.tombstones)
	}
	if !inst.blockedMarks["u2"] {
		t.Fatalf("refresh must NOT spend the reject half, blockedMarks=%v", inst.blockedMarks)
	}
	// Without a refresh, both halves stay.
	inst.tombstones = map[string]bool{"u2": true}
	m.reconcileTombstones(inst, inst.req, false)
	if !inst.tombstones["u2"] || !inst.blockedMarks["u2"] {
		t.Fatalf("no-refresh reconcile must keep both halves: tombstones=%v blockedMarks=%v", inst.tombstones, inst.blockedMarks)
	}
	// A re-added user leaves both sets: it must not stay rejected.
	writeSingboxConfigUsers(t, filepath.Join(dir, "singbox", "run"), "u1", "u2")
	m.reconcileTombstones(inst, inst.req, false)
	if len(inst.tombstones) != 0 || len(inst.blockedMarks) != 0 {
		t.Fatalf("re-added user must leave both sets: tombstones=%v blockedMarks=%v", inst.tombstones, inst.blockedMarks)
	}
	// A disabled instance clears both.
	inst.tombstones = map[string]bool{"u2": true}
	inst.blockedMarks = map[string]bool{"u2": true}
	m.reconcileTombstones(inst, ApplyRequest{Type: "singbox", Enabled: false}, false)
	if len(inst.tombstones) != 0 || len(inst.blockedMarks) != 0 {
		t.Fatalf("disabled instance must clear both sets: tombstones=%v blockedMarks=%v", inst.tombstones, inst.blockedMarks)
	}
}

func TestPersistRestoreBlockedMarks(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	m.instances["singbox"] = &instance{
		req:          ApplyRequest{Type: "singbox", Enabled: true, Ports: tableLossPorts, UserMarks: []int64{1, 2}},
		tombstones:   map[string]bool{"u2": true},
		blockedMarks: map[string]bool{"u2": true, "u5": true},
		logs:         &logRing{},
	}
	m.persist()
	if _, err := os.Stat(filepath.Join(dir, "blocked.json")); err != nil {
		t.Fatalf("persist must write blocked.json: %v", err)
	}
	// A fresh manager on the same directory restores the durable half.
	m2 := NewManager(dir)
	m2.instances["singbox"] = &instance{
		req:  ApplyRequest{Type: "singbox", Enabled: true, Ports: tableLossPorts, UserMarks: []int64{1, 2}},
		logs: &logRing{},
	}
	if !m2.restoreBlockedMarks() {
		t.Fatal("restoreBlockedMarks must report marks restored")
	}
	got := m2.allBlockedUserMarks()
	want := map[int64]bool{2: true, 5: true}
	if len(got) != len(want) {
		t.Fatalf("allBlockedUserMarks() after restore = %v, want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("allBlockedUserMarks() after restore = %v, want %v", got, want)
		}
	}
	// The transient sweeper half is NOT restored: a restart runs
	// the fresh config, so there is nothing for the sweeper to close.
	if len(m2.instances["singbox"].tombstones) != 0 {
		t.Fatalf("sweeper half must not be restored, tombstones=%v", m2.instances["singbox"].tombstones)
	}
	// Clearing the set and persisting must not leave a stale file
	// that resurrects the block after a restart.
	m.instances["singbox"].blockedMarks = nil
	m.persist()
	m3 := NewManager(dir)
	m3.instances["singbox"] = &instance{
		req:  ApplyRequest{Type: "singbox", Enabled: true, Ports: tableLossPorts, UserMarks: []int64{1, 2}},
		logs: &logRing{},
	}
	if m3.restoreBlockedMarks() {
		t.Fatal("cleared set must not resurrect after a restart")
	}
}

func TestRestoreBlockedMarksPrunesReaddedUser(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	writeSingboxConfigUsers(t, filepath.Join(dir, "singbox", "run"), "u1") // u1 live again
	m.instances["singbox"] = &instance{
		req:  ApplyRequest{Type: "singbox", Enabled: true},
		logs: &logRing{},
	}
	if err := os.WriteFile(filepath.Join(dir, "blocked.json"), []byte(`{"singbox":["u1","u2"]}`), 0o600); err != nil {
		t.Fatalf("seed blocked.json: %v", err)
	}
	if !m.restoreBlockedMarks() {
		t.Fatal("restoreBlockedMarks must report marks restored")
	}
	inst := m.instances["singbox"]
	if inst.blockedMarks["u1"] {
		t.Fatalf("re-added user u1 must be pruned on restore, blockedMarks=%v", inst.blockedMarks)
	}
	if !inst.blockedMarks["u2"] {
		t.Fatalf("still-removed user u2 must stay blocked, blockedMarks=%v", inst.blockedMarks)
	}
}

func TestRestoreBlockedMarksCorruptFile(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	m.instances["singbox"] = &instance{
		req:  ApplyRequest{Type: "singbox", Enabled: true},
		logs: &logRing{},
	}
	if err := os.WriteFile(filepath.Join(dir, "blocked.json"), []byte(`{not json`), 0o600); err != nil {
		t.Fatalf("seed blocked.json: %v", err)
	}
	if m.restoreBlockedMarks() {
		t.Fatal("corrupt blocked.json must restore nothing")
	}
}

// TestVerifyRebuildKeepsReject is the R4B-01 repro at unit level:
// a table deleted out-of-band after the truncation must come back
// WITH the reject, even though the sweeper half was already spent
// by a config refresh in the meantime.
func TestVerifyRebuildKeepsReject(t *testing.T) {
	rec := stubNftTableLoss(t)
	m := NewManager(t.TempDir())
	m.instances["singbox"] = &instance{
		req:          ApplyRequest{Type: "singbox", Enabled: true, Ports: tableLossPorts, UserMarks: []int64{1, 2}},
		blockedMarks: map[string]bool{"u2": true},
		logs:         &logRing{},
	}
	if err := m.nft.sync(tableLossPorts, []int64{1, 2}, m.allBlockedUserMarks()); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	last := rec.scripts[rec.scriptsLen()-1]
	if !strings.Contains(last, "meta mark 0x46560002 reject") {
		t.Fatalf("seed sync must render the reject:\n%s", last)
	}
	rec.deleteTable()
	m.verifyNftTable()
	if got := rec.scriptsLen(); got != 2 {
		t.Fatalf("verify after table loss scripts=%d, want 2 (rebuild)", got)
	}
	last = rec.scripts[rec.scriptsLen()-1]
	if !strings.Contains(last, "meta mark 0x46560002 reject") {
		t.Fatalf("rebuild after table loss must keep the reject (R4B-01):\n%s", last)
	}
	if strings.Contains(last, "meta mark 0x46560001 reject") {
		t.Fatalf("rebuild must not reject the kept user u1:\n%s", last)
	}
}

func TestAllBlockedUserMarksReadsBlockedNotTombstones(t *testing.T) {
	m := &Manager{instances: map[string]*instance{}, nft: &nftCounters{}}
	m.instances["sing-box"] = &instance{
		req:          ApplyRequest{Enabled: true},
		tombstones:   map[string]bool{"u9": true}, // sweeper-only, never rendered
		blockedMarks: map[string]bool{"u2": true, "u5": true, "bogus": true},
	}
	m.instances["xray"] = &instance{
		req:          ApplyRequest{Enabled: false},
		blockedMarks: map[string]bool{"u3": true},
	}
	got := m.allBlockedUserMarks()
	// Enabled instance contributes u2 and u5; the unparseable name
	// is skipped; the sweeper-only tombstone is not rendered; the
	// disabled instance contributes nothing.
	want := map[int64]bool{2: true, 5: true}
	if len(got) != len(want) {
		t.Fatalf("allBlockedUserMarks() = %v, want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("allBlockedUserMarks() = %v, want %v", got, want)
		}
	}
}
