package kernel

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// F2-NFT-01: two agents on one host shared one kernel directory and
// one nft table. Every sync deletes the table and rebuilds it from
// the syncing agent's own ports, so a second agent's sync wiped the
// first agent's counters and whitelists, and its nft.json fingerprint
// overwrote the first agent's. The kernel root now follows the agent
// binary's own directory (ResolveBaseDir) and the table name follows
// the root (nftTableForBaseDir).

func TestNftTableForBaseDirCanonicalIsolated(t *testing.T) {
	stubKernelResolution(t, false, "/anywhere", nil, "/etc/flmx_agent/flmx_agent", nil)
	got := nftTableForBaseDir(defaultBaseDir)
	if got == nftTable {
		t.Fatalf("current canonical root %q must not use the plain table, got %q", defaultBaseDir, got)
	}
	if !strings.HasPrefix(got, nftTable+"_") || len(got) != len(nftTable)+1+8 {
		t.Fatalf("canonical table must be flmx_kernel_<8 hex>: %q", got)
	}
	if again := nftTableForBaseDir(defaultBaseDir); again != got {
		t.Fatalf("table derivation must be stable: %q vs %q", again, got)
	}
	if got == nftTableForBaseDir(legacyBaseDir) {
		t.Fatal("canonical and legacy roots must not share a table")
	}
}

// F-R4-02: the legacy pre-rename root must NOT share the plain table
// with the current root — a legacy agent and a current agent on one
// host would otherwise wipe each other's counters/whitelists on sync
// (F2-NFT-01 mechanism). The legacy root gets its own fixed table.
func TestNftTableForBaseDirLegacyIsolated(t *testing.T) {
	stubKernelResolution(t, false, "/anywhere", nil, "/etc/flux_agent/flux_agent", nil)
	if got := nftTableForBaseDir(legacyBaseDir); got != "flmx_kernel_legacy" {
		t.Fatalf("legacy root %q must get its own table, got %q", legacyBaseDir, got)
	}
	if nftTableForBaseDir(legacyBaseDir) == nftTableForBaseDir(defaultBaseDir) {
		t.Fatal("legacy and current roots must not share a table")
	}
}

// F-R4-02: legacy-vs-current sync isolation — one agent's sync must
// not delete or rewrite the other generation's table. Temp-dir
// counters stand in for the two installs (their real roots are
// /etc paths whose state files the test must not touch); the table
// names are the real derived ones.
func TestLegacyAndCurrentAgentsDoNotShareNftTable(t *testing.T) {
	stubKernelResolution(t, false, "/anywhere", nil, "/etc/flmx_agent/flmx_agent", nil)
	stubFakeNftTables(t)
	ctx := context.Background()

	nCur := newNftCounters(filepath.Join(t.TempDir(), "cur", "kernels"))
	nCur.table = nftTableForBaseDir(defaultBaseDir)
	nLeg := newNftCounters(filepath.Join(t.TempDir(), "leg", "kernels"))
	nLeg.table = nftTableForBaseDir(legacyBaseDir)
	if nCur.tableName() == nLeg.tableName() {
		t.Fatalf("legacy and current agents must get different tables, both %q", nCur.tableName())
	}

	portsCur := []Port{{InboundID: 901, Port: 18001, Network: "tcp,udp"}}
	portsLeg := []Port{{InboundID: 902, Port: 18002, Network: "tcp"}}
	if err := nCur.sync(portsCur, nil, nil); err != nil {
		t.Fatalf("current sync: %v", err)
	}
	fpCur, err := nCur.tableFingerprint(ctx)
	if err != nil {
		t.Fatalf("current table must exist after current sync: %v", err)
	}
	if err := nLeg.sync(portsLeg, nil, nil); err != nil {
		t.Fatalf("legacy sync: %v", err)
	}
	if fp, err := nCur.tableFingerprint(ctx); err != nil || fp != fpCur {
		t.Fatalf("legacy sync changed the current table: fp=%q err=%v (was %q)", fp, err, fpCur)
	}
	if _, err := nLeg.tableFingerprint(ctx); err != nil {
		t.Fatalf("legacy table must exist after legacy sync: %v", err)
	}
}

func TestNftTableForBaseDirContainerIsolated(t *testing.T) {
	stubKernelResolution(t, true, "/data", nil, "/usr/local/bin/flmx_agent", nil)
	a := nftTableForBaseDir("/data/kernels")
	b := nftTableForBaseDir("/other/kernels")
	for _, got := range []string{a, b} {
		if got == nftTable {
			t.Fatalf("container form root must not use the plain table, got %q", got)
		}
		if !strings.HasPrefix(got, nftTable+"_") || len(got) != len(nftTable)+1+8 {
			t.Fatalf("container table must be flmx_kernel_<8 hex>: %q", got)
		}
	}
	if a == b {
		t.Fatalf("different container roots must get different tables: %q", a)
	}
}

func TestNftTableForBaseDirScratchIsolated(t *testing.T) {
	stubKernelResolution(t, false, "/anywhere", nil, "/root/scratch-nodeconnlog/flmx_agent", nil)
	a := nftTableForBaseDir("/root/scratch-nodeconnlog/kernels")
	b := nftTableForBaseDir("/root/scratch-r2b2-f2/kernels")
	if a == nftTable || b == nftTable {
		t.Fatalf("scratch roots must not use the plain table: %q %q", a, b)
	}
	if a == b {
		t.Fatalf("different installs must get different tables: %q", a)
	}
	if !strings.HasPrefix(a, nftTable+"_") || len(a) != len(nftTable)+1+8 {
		t.Fatalf("isolated table must be flmx_kernel_<8 hex>: %q", a)
	}
	if matched, _ := regexp.MatchString(`^[a-z0-9_]+$`, a); !matched {
		t.Fatalf("isolated table must be a valid nft identifier: %q", a)
	}
	// Stable: the same root always derives the same table.
	if again := nftTableForBaseDir("/root/scratch-nodeconnlog/kernels"); again != a {
		t.Fatalf("table derivation must be stable: %q vs %q", again, a)
	}
}

// fakeNftTables is a stateful in-memory nft: -f scripts apply (deletes
// remove tables, the built block becomes the table's content) and
// list-table reads back what is actually there. It proves, without
// root, that one agent's sync cannot delete or rewrite another
// agent's table.
type fakeNftTables struct {
	mu     sync.Mutex
	tables map[string]string
	loads  int
}

var fakeNftTableOpen = regexp.MustCompile(`^table inet (\S+) \{$`)

func (f *fakeNftTables) runner(_ context.Context, stdin string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	joined := strings.Join(args, " ")
	switch {
	case joined == "-f -":
		f.loads++
		current := ""
		for _, line := range strings.Split(stdin, "\n") {
			if strings.HasPrefix(line, "delete table inet ") {
				delete(f.tables, strings.TrimSpace(strings.TrimPrefix(line, "delete table inet ")))
			}
			if m := fakeNftTableOpen.FindStringSubmatch(line); m != nil {
				current = m[1]
			}
		}
		if current != "" {
			f.tables[current] = stdin
		}
		return nil, nil
	case strings.HasPrefix(joined, "list table"):
		name := args[len(args)-1]
		if body, ok := f.tables[name]; ok {
			return []byte(body), nil
		}
		return nil, errors.New("no such table (fake)")
	case strings.HasPrefix(joined, "-j list counters"):
		return []byte(`{"nftables":[]}`), nil
	}
	return nil, errors.New("unexpected nft call (fake): " + joined)
}

func stubFakeNftTables(t *testing.T) *fakeNftTables {
	t.Helper()
	f := &fakeNftTables{tables: make(map[string]string)}
	oldRunner, oldAvail := nftRunner, nftAvailable
	nftRunner = f.runner
	nftAvailable = func() error { return nil }
	t.Cleanup(func() { nftRunner, nftAvailable = oldRunner, oldAvail })
	return f
}

func TestTwoAgentsSyncWithoutSharingTable(t *testing.T) {
	stubKernelResolution(t, false, "/anywhere", nil, "/root/scratch-a/flmx_agent", nil)
	f := stubFakeNftTables(t)
	ctx := context.Background()

	dirA := filepath.Join(t.TempDir(), "a", "kernels")
	dirB := filepath.Join(t.TempDir(), "b", "kernels")
	nA := newNftCounters(dirA)
	nB := newNftCounters(dirB)
	if nA.tableName() == nB.tableName() {
		t.Fatalf("two installs must get different tables, both %q", nA.tableName())
	}
	if nA.tableName() == nftTable || nB.tableName() == nftTable {
		t.Fatalf("scratch counters must not use the plain table: %q %q", nA.tableName(), nB.tableName())
	}

	portsA := []Port{{InboundID: 901, Port: 18001, Network: "tcp,udp"}}
	portsB := []Port{{InboundID: 902, Port: 18002, Network: "tcp"}}
	if err := nA.sync(portsA, nil, nil); err != nil {
		t.Fatalf("A sync: %v", err)
	}
	fpA, err := nA.tableFingerprint(ctx)
	if err != nil {
		t.Fatalf("A table must exist after A sync: %v", err)
	}
	if _, ok := f.tables[nftTable]; ok {
		t.Fatal("A sync must not create the shared plain table")
	}
	if err := nB.sync(portsB, nil, nil); err != nil {
		t.Fatalf("B sync: %v", err)
	}
	// B's sync must leave A's table exactly as it was.
	if fp, err := nA.tableFingerprint(ctx); err != nil || fp != fpA {
		t.Fatalf("B sync changed A's table: fp=%q err=%v (was %q)", fp, err, fpA)
	}
	if _, err := nB.tableFingerprint(ctx); err != nil {
		t.Fatalf("B table must exist after B sync: %v", err)
	}
	// A re-syncs with a changed ruleset: B must be untouched.
	fpB, err := nB.tableFingerprint(ctx)
	if err != nil {
		t.Fatalf("B fingerprint: %v", err)
	}
	if err := nA.sync(append(portsA, Port{InboundID: 903, Port: 18003, Network: "tcp"}), nil, nil); err != nil {
		t.Fatalf("A re-sync: %v", err)
	}
	if fp, err := nB.tableFingerprint(ctx); err != nil || fp != fpB {
		t.Fatalf("A re-sync changed B's table: fp=%q err=%v (was %q)", fp, err, fpB)
	}
	// Each agent's state file holds its own signature.
	for _, n := range []*nftCounters{nA, nB} {
		if n.state.Signature == "" {
			t.Fatalf("counter %q must persist a signature", n.tableName())
		}
	}
	if nA.state.Signature == nB.state.Signature {
		t.Fatal("two different rulesets must not share a signature")
	}
}

func TestNftScriptIsolatedTableDeletesOnlyItself(t *testing.T) {
	stubKernelResolution(t, false, "/anywhere", nil, "/root/scratch-a/flmx_agent", nil)
	script := nftScript(nftTableForBaseDir("/root/scratch-a/kernels"), nil, nil, nil, nil, 0, nil)
	if !strings.Contains(script, "delete table inet flmx_kernel_") {
		t.Fatalf("isolated script must delete its own table: %s", script)
	}
	for _, bare := range []string{"delete table inet flmx_kernel\n", "delete table inet flvx_kernel\n"} {
		if strings.Contains(script, bare) {
			t.Fatalf("isolated script must not delete the shared/legacy table (%q): %s", bare, script)
		}
	}
}

func TestRemoveTablesOnlyOwnTableForIsolatedAgent(t *testing.T) {
	stubKernelResolution(t, false, "/anywhere", nil, "/root/scratch-a/flmx_agent", nil)
	var scripts []string
	oldRunner := nftRunner
	nftRunner = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		scripts = append(scripts, stdin)
		return nil, nil
	}
	t.Cleanup(func() { nftRunner = oldRunner })

	n := newNftCounters(t.TempDir())
	if err := n.removeTables(); err != nil {
		t.Fatalf("removeTables: %v", err)
	}
	if len(scripts) != 1 {
		t.Fatalf("removeTables must run one script, got %d", len(scripts))
	}
	script := scripts[0]
	if !strings.Contains(script, "delete table inet "+n.tableName()+"\n") {
		t.Fatalf("removeTables must delete its own table %q: %s", n.tableName(), script)
	}
	for _, bare := range []string{"delete table inet flmx_kernel\n", "delete table inet flvx_kernel\n", "delete table inet flmx_kernel_legacy\n"} {
		if strings.Contains(script, bare) {
			t.Fatalf("isolated removeTables must not delete %q: %s", bare, script)
		}
	}
}

func TestRemoveTablesCanonicalDeletesLegacyToo(t *testing.T) {
	stubKernelResolution(t, false, "/anywhere", nil, "/etc/flmx_agent/flmx_agent", nil)
	var scripts []string
	oldRunner := nftRunner
	nftRunner = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		scripts = append(scripts, stdin)
		return nil, nil
	}
	t.Cleanup(func() { nftRunner = oldRunner })

	n := newNftCounters(defaultBaseDir)
	n.state = nftState{}
	if n.tableName() == nftTable {
		t.Fatalf("canonical counter must not use the plain table, got %q", n.tableName())
	}
	if err := n.removeTables(); err != nil {
		t.Fatalf("removeTables: %v", err)
	}
	script := scripts[0]
	for _, want := range []string{"delete table inet " + n.tableName() + "\n", "delete table inet flvx_kernel\n", "delete table inet flmx_kernel_legacy\n", "delete table inet flmx_hop_probe\n"} {
		if !strings.Contains(script, want) {
			t.Fatalf("canonical removeTables must delete %q: %s", want, script)
		}
	}
	if strings.Contains(script, "delete table inet flmx_kernel\n") {
		t.Fatalf("fresh canonical removeTables must not delete the plain table: %s", script)
	}
}

// P1-1: a canonical install upgraded from the pre-fix build still
// owns its old plain table (a persisted pre-fix signature proves
// it), so uninstall deletes it; a fresh install never owned one.
func TestRemoveTablesCanonicalWithOldStateDeletesPlainTable(t *testing.T) {
	stubKernelResolution(t, false, "/anywhere", nil, "/etc/flmx_agent/flmx_agent", nil)
	var scripts []string
	oldRunner := nftRunner
	nftRunner = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		scripts = append(scripts, stdin)
		return nil, nil
	}
	t.Cleanup(func() { nftRunner = oldRunner })

	n := newNftCounters(defaultBaseDir)
	n.state = nftState{Signature: "pre-fix", Epoch: "old"}
	if err := n.removeTables(); err != nil {
		t.Fatalf("removeTables: %v", err)
	}
	if !strings.Contains(scripts[0], "delete table inet flmx_kernel\n") {
		t.Fatalf("upgraded canonical removeTables must delete its old plain table: %s", scripts[0])
	}
}

func TestParseCountersIgnoresOtherTables(t *testing.T) {
	stubKernelResolution(t, false, "/anywhere", nil, "/root/scratch-a/flmx_agent", nil)
	other := nftTableForBaseDir("/root/scratch-b/kernels")
	raw := `{"nftables":[{"counter":{"name":"in_7_tcp","table":"` + nftTable + `","bytes":10}},` +
		`{"counter":{"name":"in_7_tcp","table":"` + other + `","bytes":20}}]}`
	got, _, err := parseCounters([]byte(raw), other)
	if err != nil {
		t.Fatalf("parseCounters: %v", err)
	}
	if len(got) != 1 || got[0].Bytes != 20 {
		t.Fatalf("parseCounters must only return rows from the agent's own table, got %+v", got)
	}
}

// TestLiveTwoAgentsDoNotShareNftTable is the test-machine proof for
// F2-NFT-01. It runs ONLY with FLMX_LIVE_NFT=1, as root, on a machine
// with nftables (the Lightsail test machine): the compiled test
// binary is copied to a scratch directory and run from there, so its
// own executable path is a scratch install — ResolveBaseDir must root
// under that directory, and two counters rooted at different
// installs must sync into two different kernel tables while the live
// agent's table stays byte-identical (fingerprint with
// counter values masked). Cleanup removes both test tables.
func TestLiveTwoAgentsDoNotShareNftTable(t *testing.T) {
	if os.Getenv("FLMX_LIVE_NFT") != "1" {
		t.Skip("live nft test: set FLMX_LIVE_NFT=1 on a test machine with nft + root")
	}
	if _, err := exec.LookPath("nft"); err != nil {
		t.Fatalf("live nft test needs the nft binary: %v", err)
	}
	ctx := context.Background()

	// The test binary IS the scratch agent stand-in: its own
	// directory is the install.
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	baseA := ResolveBaseDir()
	if want := filepath.Join(filepath.Dir(exe), "kernels"); baseA != want {
		t.Fatalf("ResolveBaseDir must root under the binary's own directory: got %q want %q", baseA, want)
	}
	if baseA == defaultBaseDir {
		t.Fatalf("scratch binary must not resolve to the live root %q", defaultBaseDir)
	}
	t.Cleanup(func() { _ = os.RemoveAll(baseA) })

	// The live agent's table, read-only: fingerprint before/after.
	live := newNftCounters(defaultBaseDir)
	fpLive0, err := live.tableFingerprint(ctx)
	if err != nil {
		t.Fatalf("live agent table must exist before the test: %v", err)
	}

	dirB := filepath.Join(t.TempDir(), "kernels")
	// A real agent's root exists by sync time (the binary install
	// creates it); make the roots exist so state persistence is real.
	for _, d := range []string{baseA, dirB} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	nA := newNftCounters(baseA)
	nB := newNftCounters(dirB)
	if nA.tableName() == nftTable || nB.tableName() == nftTable {
		t.Fatalf("scratch counters must not use the plain table: %q %q", nA.tableName(), nB.tableName())
	}
	if nA.tableName() == nB.tableName() {
		t.Fatalf("two installs must get different tables, both %q", nA.tableName())
	}
	t.Cleanup(func() {
		_ = nA.removeTables()
		_ = nB.removeTables()
	})

	portsA := []Port{{InboundID: 901, Port: 18001, Network: "tcp,udp"}}
	portsB := []Port{{InboundID: 902, Port: 18002, Network: "tcp"}}
	if err := nA.sync(portsA, nil, nil); err != nil {
		t.Fatalf("A sync: %v", err)
	}
	fpA, err := nA.tableFingerprint(ctx)
	if err != nil {
		t.Fatalf("A table must exist after A sync: %v", err)
	}
	if fp, err := live.tableFingerprint(ctx); err != nil || fp != fpLive0 {
		t.Fatalf("A sync changed the live table: fp=%q err=%v (was %q)", fp, err, fpLive0)
	}
	if err := nB.sync(portsB, nil, nil); err != nil {
		t.Fatalf("B sync: %v", err)
	}
	if fp, err := nA.tableFingerprint(ctx); err != nil || fp != fpA {
		t.Fatalf("B sync changed A's table: fp=%q err=%v (was %q)", fp, err, fpA)
	}
	fpB, err := nB.tableFingerprint(ctx)
	if err != nil {
		t.Fatalf("B table must exist after B sync: %v", err)
	}
	if err := nA.sync(append(portsA, Port{InboundID: 903, Port: 18003, Network: "tcp"}), nil, nil); err != nil {
		t.Fatalf("A re-sync: %v", err)
	}
	if fp, err := nB.tableFingerprint(ctx); err != nil || fp != fpB {
		t.Fatalf("A re-sync changed B's table: fp=%q err=%v (was %q)", fp, err, fpB)
	}
	if fp, err := live.tableFingerprint(ctx); err != nil || fp != fpLive0 {
		t.Fatalf("syncs changed the live table: fp=%q err=%v (was %q)", fp, err, fpLive0)
	}
}

// P1-1 upgrade migration: the first post-upgrade sync of an install
// that previously applied with the plain table removes its own old
// plain tables (flmx_kernel, flvx_kernel) in the same atomic script
// that builds its new hashed table, records the migration in
// nft.json, and never touches a plain table again.

func TestSyncMigratesPlainTablesOnce(t *testing.T) {
	stubKernelResolution(t, false, "/anywhere", nil, "/etc/flmx_agent/flmx_agent", nil)
	f := stubFakeNftTables(t)
	ctx := context.Background()

	// The pre-fix plain tables, as the old build left them.
	f.tables[nftTable] = "table inet flmx_kernel {\n}\n"
	f.tables[legacyNFTTable] = "table inet flvx_kernel {\n}\n"
	n := &nftCounters{
		statePath: filepath.Join(t.TempDir(), "nft.json"),
		baseDir:   defaultBaseDir,
		table:     nftTableForBaseDir(defaultBaseDir),
		state:     nftState{Signature: "pre-fix", Epoch: "old"},
	}
	ports := []Port{{InboundID: 901, Port: 18001, Network: "tcp,udp"}}
	if err := n.sync(ports, nil, nil); err != nil {
		t.Fatalf("migrating sync: %v", err)
	}
	for _, plain := range []string{nftTable, legacyNFTTable} {
		if _, ok := f.tables[plain]; ok {
			t.Fatalf("migrating sync must delete the pre-fix plain table %q", plain)
		}
	}
	if _, err := n.tableFingerprint(ctx); err != nil {
		t.Fatalf("hashed table must exist after migrating sync: %v", err)
	}
	if !n.state.Migrated {
		t.Fatal("state must record the migration after the migrating sync")
	}

	// A plain table that shows up later (another install's live
	// table, or a re-created one) must survive later syncs: the
	// migration runs once. Force a real re-apply with a changed
	// ruleset so the early return cannot mask a second delete.
	f.tables[nftTable] = "table inet flmx_kernel {\n}\n"
	if err := n.sync(append(ports, Port{InboundID: 902, Port: 18002, Network: "tcp"}), nil, nil); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if _, ok := f.tables[nftTable]; !ok {
		t.Fatal("second sync must not delete a plain table again")
	}
	if _, err := n.tableFingerprint(ctx); err != nil {
		t.Fatalf("hashed table must still exist after second sync: %v", err)
	}
}

// A fresh install has no persisted signature, so a plain table on
// the host cannot be its own: the sync must leave it alone.
func TestSyncSkipsMigrationForFreshInstall(t *testing.T) {
	stubKernelResolution(t, false, "/anywhere", nil, "/etc/flmx_agent/flmx_agent", nil)
	f := stubFakeNftTables(t)

	f.tables[nftTable] = "table inet flmx_kernel {\n}\n"
	f.tables[legacyNFTTable] = "table inet flvx_kernel {\n}\n"
	n := &nftCounters{
		statePath: filepath.Join(t.TempDir(), "nft.json"),
		baseDir:   defaultBaseDir,
		table:     nftTableForBaseDir(defaultBaseDir),
	}
	if err := n.sync([]Port{{InboundID: 901, Port: 18001, Network: "tcp,udp"}}, nil, nil); err != nil {
		t.Fatalf("fresh sync: %v", err)
	}
	for _, plain := range []string{nftTable, legacyNFTTable} {
		if _, ok := f.tables[plain]; !ok {
			t.Fatalf("fresh sync must not delete the plain table %q", plain)
		}
	}
	if !n.state.Migrated {
		t.Fatal("fresh install must record that there is nothing to migrate")
	}
}

// The legacy root never owned a plain table (its table was always
// flmx_kernel_legacy), so its syncs must not touch the plain
// tables even with a pre-fix signature on disk.
func TestSyncSkipsMigrationForLegacyRoot(t *testing.T) {
	stubKernelResolution(t, false, "/anywhere", nil, "/etc/flux_agent/flux_agent", nil)
	f := stubFakeNftTables(t)

	f.tables[nftTable] = "table inet flmx_kernel {\n}\n"
	n := &nftCounters{
		statePath: filepath.Join(t.TempDir(), "nft.json"),
		baseDir:   legacyBaseDir,
		table:     nftTableForBaseDir(legacyBaseDir),
		state:     nftState{Signature: "pre-fix", Epoch: "old"},
	}
	if err := n.sync([]Port{{InboundID: 901, Port: 18001, Network: "tcp"}}, nil, nil); err != nil {
		t.Fatalf("legacy sync: %v", err)
	}
	if _, ok := f.tables[nftTable]; !ok {
		t.Fatal("legacy sync must not delete the plain table")
	}
	if _, err := n.tableFingerprint(context.Background()); err != nil {
		t.Fatalf("legacy table must exist after legacy sync: %v", err)
	}
}

// The container form used the plain table pre-fix too, so a
// container install with a pre-fix signature migrates as well.
func TestSyncMigratesPlainTablesInContainer(t *testing.T) {
	stubKernelResolution(t, true, "/data", nil, "/usr/local/bin/flmx_agent", nil)
	f := stubFakeNftTables(t)

	f.tables[nftTable] = "table inet flmx_kernel {\n}\n"
	n := &nftCounters{
		statePath: filepath.Join(t.TempDir(), "nft.json"),
		baseDir:   "/data/kernels",
		table:     nftTableForBaseDir("/data/kernels"),
		state:     nftState{Signature: "pre-fix", Epoch: "old"},
	}
	if err := n.sync([]Port{{InboundID: 901, Port: 18001, Network: "tcp"}}, nil, nil); err != nil {
		t.Fatalf("container migrating sync: %v", err)
	}
	if _, ok := f.tables[nftTable]; ok {
		t.Fatal("container migrating sync must delete the pre-fix plain table")
	}
	if !n.state.Migrated {
		t.Fatal("state must record the migration after the container migrating sync")
	}
}
