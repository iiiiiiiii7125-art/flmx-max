package kernel

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R7 · RB1-01: the Snell egress guard. These tests pin three contracts:
// the drop set covers the product's canonical non-public definition
// (and nothing public), the rendered nftables rules exist exactly
// while a guarded Snell runs, and a Snell apply without its guard
// prerequisites fails instead of running unguarded.

func ipInAny(ipStr string, cidrs []string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err == nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

func TestSnellPrivateSetsCoverNonPublicOnly(t *testing.T) {
	all := append(append([]string(nil), snellPrivateV4...), snellPrivateV6All()...)
	blocked := []string{
		// loopback / RFC1918 / link-local incl. metadata
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "172.31.255.254", "192.168.1.1",
		"169.254.169.254", "169.254.1.1",
		// CGNAT (incl. the Alibaba metadata address inside it)
		"100.64.0.1", "100.100.100.200", "100.127.255.254",
		// special-purpose / reserved / multicast / broadcast
		"0.0.0.0", "192.0.0.1", "198.18.0.1", "198.51.100.7", "203.0.113.9",
		"192.0.2.3", "224.0.0.1", "240.0.0.1", "255.255.255.255",
		// IPv6: loopback, unspecified, ULA (incl. EC2 metadata), link-local, multicast, docs
		"::1", "::", "fd00:ec2::254", "fc00::1", "fe80::1", "ff02::1", "2001:db8::1", "100::1",
		// IPv4-mapped forms must not slip past the v4 set
		"::ffff:10.0.0.5", "::ffff:169.254.169.254", "::ffff:192.168.0.1", "::ffff:127.0.0.1",
	}
	for _, ip := range blocked {
		if !ipInAny(ip, all) {
			t.Errorf("non-public address %s is NOT in the Snell drop set", ip)
		}
	}
	public := []string{
		"1.1.1.1", "8.8.8.8", "93.184.216.34", "203.0.114.1",
		"172.15.0.1", "172.32.0.1", // just outside 172.16/12
		"100.63.255.255", "100.128.0.1", // just outside 100.64/10
		"192.169.0.1", "11.0.0.1",
		"2001:4860:4860::8888", "2606:4700:4700::1111",
		"::ffff:8.8.8.8", "::ffff:1.1.1.1",
	}
	for _, ip := range public {
		if ipInAny(ip, all) {
			t.Errorf("public address %s must NOT be in the Snell drop set (over-blocking)", ip)
		}
	}
	// 198.20.0.1 is just outside the benchmarking range.
	if ipInAny("198.20.0.1", all) {
		t.Error("198.20.0.1 must not be blocked")
	}
}

func TestSnellMappedSetDerivedFromV4(t *testing.T) {
	all := snellPrivateV6All()
	if len(all) != len(snellPrivateV6)+len(snellPrivateV4) {
		t.Fatalf("mapped expansion drifted: got %d entries, want %d native + %d mapped",
			len(all), len(snellPrivateV6), len(snellPrivateV4))
	}
	for _, c := range snellPrivateV4 {
		ip, ipnet, _ := net.ParseCIDR(c)
		ones, _ := ipnet.Mask.Size()
		want := fmt.Sprintf("::ffff:%s/%d", ip.String(), 96+ones)
		found := false
		for _, got := range all {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("v4 range %s has no mapped twin %s", c, want)
		}
	}
}

func TestSnellGuardScriptRendering(t *testing.T) {
	guard := &snellGuard{UID: 997, Resolvers: []string{"1.1.1.1", "127.0.0.53", "fe80::1"}}
	script := nftScriptGuard(nil, nil, nil, nil, guard)
	for _, want := range []string{
		"set snell_priv4", "set snell_priv6",
		"169.254.0.0/16", "fc00::/7",
		"meta skuid 997 ip daddr @snell_priv4 drop",
		"meta skuid 997 ip6 daddr @snell_priv6 drop",
		"meta skuid 997 ip daddr 127.0.0.53 udp dport 53 accept",
		"meta skuid 997 ip daddr 1.1.1.1 tcp dport { 53, 853 } accept",
		"meta skuid 997 ip6 daddr fe80::1 udp dport 53 accept",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("guard script missing %q:\n%s", want, script)
		}
	}
	// Resolver exemptions must precede the drops or they never match.
	dropAt := strings.Index(script, "@snell_priv4 drop")
	for _, r := range []string{"127.0.0.53", "fe80::1"} {
		if at := strings.Index(script, "daddr "+r); at < 0 || at > dropAt {
			t.Errorf("resolver exemption for %s must come before the drop rules", r)
		}
	}
	// A script without a guard is byte-identical to the legacy script:
	// nodes without Snell see zero change.
	plain := nftScript(expandPorts([]Port{{InboundID: 1, Port: 443, Network: "tcp"}}), nil, nil, nil)
	guardNil := nftScriptGuard(expandPorts([]Port{{InboundID: 1, Port: 443, Network: "tcp"}}), nil, nil, nil, nil)
	if plain != guardNil {
		t.Fatal("nil guard must render the legacy script")
	}
	if strings.Contains(plain, "snell_priv") || strings.Contains(plain, "skuid") {
		t.Fatal("unguarded script must not mention the Snell guard")
	}
	// Guard-only content still builds a table (no counters at all).
	if !strings.Contains(script, "chain output") {
		t.Fatal("guard-only script must still create the table")
	}
}

func TestParseResolvConf(t *testing.T) {
	got := parseResolvConf("# comment\nnameserver 127.0.0.53\nnameserver 1.1.1.1\nnameserver 127.0.0.53\nsearch lan\nnameserver fe80::1\nnameserver not-an-ip\n")
	want := []string{"1.1.1.1", "127.0.0.53", "fe80::1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("parseResolvConf = %v, want %v", got, want)
	}
}

// stubSnellGuardIdentity pins the guard user to the test process's
// own identity, for tests that apply a Snell kernel for reasons other
// than the guard itself: the apply must not create real system users
// or chown files to a foreign uid (impossible in CI sandboxes anyway).
// The guard's own behavior is covered with a foreign uid (65534) by
// the tests in this file.
func stubSnellGuardIdentity(t *testing.T) {
	t.Helper()
	oldLookup, oldCreate := lookupSnellGuardUser, createSnellGuardUser
	lookupSnellGuardUser = func() (uint32, uint32, error) {
		return uint32(os.Getuid()), uint32(os.Getgid()), nil
	}
	createSnellGuardUser = func(context.Context) error { return nil }
	oldResolvers := systemResolvers
	systemResolvers = func() []string { return nil }
	t.Cleanup(func() {
		lookupSnellGuardUser, createSnellGuardUser = oldLookup, oldCreate
		systemResolvers = oldResolvers
	})
}

// snellTestEnv stubs the guard's external seams (nft binary, user
// database) and hands back a manager over a temp base dir plus the
// scripts nft was asked to load, in order.
func snellTestEnv(t *testing.T) (*Manager, *[]string) {
	t.Helper()
	var scripts []string
	oldRunner, oldAvail := nftRunner, nftAvailable
	nftRunner = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "-f" {
			scripts = append(scripts, stdin)
		}
		return []byte("table inet flmx_kernel {}"), nil
	}
	nftAvailable = func() error { return nil }
	oldLookup, oldCreate := lookupSnellGuardUser, createSnellGuardUser
	lookupSnellGuardUser = func() (uint32, uint32, error) { return 65534, 65534, nil }
	createSnellGuardUser = func(context.Context) error { return nil }
	oldResolvers := systemResolvers
	systemResolvers = func() []string { return []string{"127.0.0.53"} }
	t.Cleanup(func() {
		nftRunner, nftAvailable = oldRunner, oldAvail
		lookupSnellGuardUser, createSnellGuardUser = oldLookup, oldCreate
		systemResolvers = oldResolvers
	})
	base := t.TempDir()
	// The guard user (65534) must be able to traverse into the tree,
	// as it can on a real node where /etc is world-traversable.
	for _, d := range []string{base, filepath.Dir(base)} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return NewManager(base), &scripts
}

func snellTestRequest(t *testing.T, m *Manager) ApplyRequest {
	t.Helper()
	binDir := m.binDir("snell", "4.1.1")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "snell-server")
	script := "#!/bin/sh\nif [ \"$1\" = \"-v\" ]; then echo 'snell-server 4.1.1'; exit 0; fi\nexec sleep 3600\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// WriteFile's mode is umask-filtered; the production installer
	// chmods explicitly, so do the same here.
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	return ApplyRequest{
		Type: "snell", Version: "4.1.1", Enabled: true,
		Download:  &Download{URL: "https://example.invalid/snell.zip", BinName: "snell-server", Archive: "raw"},
		Files:    []File{{Name: "snell-7.conf", Content: "listen = 0.0.0.0:25021\npsk = test-psk\n"}},
		Processes: []ProcessSpec{
			{Name: "snell-7", Bin: "", Args: []string{"-c", "{RUN_DIR}/snell-7.conf"}},
		},
		Ports: []Port{{InboundID: 7, Port: 25021, Network: "tcp"}},
	}
}

func TestApplySnellRunsGuarded(t *testing.T) {
	m, scripts := snellTestEnv(t)
	req := snellTestRequest(t, m)
	st, err := m.Apply(req)
	if err != nil {
		t.Fatalf("apply snell: %v", err)
	}
	if !st.Running {
		t.Fatalf("snell should be running: %+v", st)
	}
	m.mu.Lock()
	inst := m.instances["snell"]
	credUID := inst.procs[0].credUID
	m.mu.Unlock()
	if credUID != 65534 {
		t.Fatalf("snell-server must run as the guard user, credUID=%d", credUID)
	}
	if len(*scripts) == 0 {
		t.Fatal("no nft script was loaded")
	}
	last := (*scripts)[len(*scripts)-1]
	for _, want := range []string{"meta skuid 65534 ip daddr @snell_priv4 drop", "meta skuid 65534 ip daddr 127.0.0.53 udp dport 53 accept"} {
		if !strings.Contains(last, want) {
			t.Errorf("applied ruleset missing %q:\n%s", want, last)
		}
	}
	// The config must be readable by the guard user it now belongs to.
	fi, err := os.Stat(filepath.Join(m.runDir("snell"), "snell-7.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("snell config mode = %v, want 0600", fi.Mode().Perm())
	}

	// Lifecycle: stop must strip the guard rules from the table.
	if _, err := m.Control("snell", "stop"); err != nil {
		t.Fatalf("control stop: %v", err)
	}
	last = (*scripts)[len(*scripts)-1]
	if strings.Contains(last, "snell_priv") || strings.Contains(last, "skuid") {
		t.Fatalf("guard rules must not survive a stop:\n%s", last)
	}
	// Start restores them; remove strips them again with the instance.
	if _, err := m.Control("snell", "start"); err != nil {
		t.Fatalf("control start: %v", err)
	}
	last = (*scripts)[len(*scripts)-1]
	if !strings.Contains(last, "meta skuid 65534 ip daddr @snell_priv4 drop") {
		t.Fatalf("guard rules must be restored on start:\n%s", last)
	}
	if _, err := m.Control("snell", "remove"); err != nil {
		t.Fatalf("control remove: %v", err)
	}
	last = (*scripts)[len(*scripts)-1]
	if strings.Contains(last, "snell_priv") || strings.Contains(last, "chain output") {
		t.Fatalf("guard rules must not survive remove:\n%s", last)
	}
}

func TestApplySnellFailsWithoutNft(t *testing.T) {
	m, _ := snellTestEnv(t)
	nftAvailable = func() error { return fmt.Errorf("nft 不存在") }
	req := snellTestRequest(t, m)
	st, err := m.Apply(req)
	if err == nil || !strings.Contains(err.Error(), "nftables") {
		t.Fatalf("snell apply without nftables must fail loudly, got state=%+v err=%v", st, err)
	}
	if st.Running {
		t.Fatal("snell must not run unguarded")
	}
	m.mu.Lock()
	n := len(m.instances["snell"].procs)
	m.mu.Unlock()
	if n != 0 {
		t.Fatal("no snell process may be started when the guard cannot be installed")
	}
}

func TestApplySnellFailsWithoutGuardUser(t *testing.T) {
	m, _ := snellTestEnv(t)
	lookupSnellGuardUser = func() (uint32, uint32, error) { return 0, 0, fmt.Errorf("user unknown") }
	createSnellGuardUser = func(context.Context) error { return fmt.Errorf("useradd 不可用") }
	req := snellTestRequest(t, m)
	st, err := m.Apply(req)
	if err == nil || !strings.Contains(err.Error(), snellGuardUserName) {
		t.Fatalf("snell apply without guard user must fail loudly, got state=%+v err=%v", st, err)
	}
	if st.Running {
		t.Fatal("snell must not run unguarded")
	}
}

func TestSnellGuardDerivation(t *testing.T) {
	m, _ := snellTestEnv(t)
	req := snellTestRequest(t, m)
	if g := m.snellGuardFor(65534); g != nil {
		t.Fatal("no instance yet: guard must be off")
	}
	m.mu.Lock()
	m.instances["snell"] = &instance{logs: &logRing{}, req: req}
	m.mu.Unlock()
	g := m.snellGuardFor(65534)
	if g == nil || g.UID != 65534 {
		t.Fatalf("enabled snell with a server spec must derive a guard, got %+v", g)
	}
	if g := m.snellGuardFor(0); g != nil {
		t.Fatal("uid 0 (unresolved) must never derive a guard")
	}
	disabled := req
	disabled.Enabled = false
	m.mu.Lock()
	m.instances["snell"].req = disabled
	m.mu.Unlock()
	if g := m.snellGuardFor(65534); g != nil {
		t.Fatal("disabled snell must not derive a guard")
	}
	// An instance whose only process is a shadow-tls front has no
	// snell-server to guard (renderSnell never emits that shape, but
	// the derivation must key on the server spec, not the type alone).
	frontOnly := req
	frontOnly.Enabled = true
	frontOnly.Processes = []ProcessSpec{{Name: "stls-7", Bin: "shadow-tls", Args: []string{}}}
	m.mu.Lock()
	m.instances["snell"].req = frontOnly
	m.mu.Unlock()
	if g := m.snellGuardFor(65534); g != nil {
		t.Fatal("front-only instance must not derive a guard")
	}
}
