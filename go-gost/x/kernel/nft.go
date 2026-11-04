package kernel

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const nftTable = "flmx_kernel"

// legacyNFTTable 是改名前的计数表名；nftScript 每次构建时顺带清理，
// 避免旧表残留在节点内核中（仅计数残留，无转发影响，但保持干净）。
const legacyNFTTable = "flvx_kernel"

// nftRunner executes `nft` with a script on stdin (or arguments). It is a
// variable so tests can capture scripts without root.
var nftRunner = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "nft", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("nft: %s", msg)
	}
	return out, nil
}

var nftAvailable = func() error {
	_, err := exec.LookPath("nft")
	return err
}

type counterRule struct {
	InboundID int64
	Port      int
	Proto     string
}

func (c counterRule) name(dir string) string {
	return fmt.Sprintf("%s_%d_%s", dir, c.InboundID, c.Proto)
}

// expandPorts turns inbound ports into one rule per L4 protocol, sorted so
// the generated ruleset (and its signature) is stable.
func expandPorts(ports []Port) []counterRule {
	seen := make(map[counterRule]struct{})
	var rules []counterRule
	for _, p := range ports {
		for _, proto := range strings.Split(p.Network, ",") {
			proto = strings.TrimSpace(proto)
			if proto != "tcp" && proto != "udp" {
				continue
			}
			r := counterRule{InboundID: p.InboundID, Port: p.Port, Proto: proto}
			if _, ok := seen[r]; ok {
				continue
			}
			seen[r] = struct{}{}
			rules = append(rules, r)
		}
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].InboundID != rules[j].InboundID {
			return rules[i].InboundID < rules[j].InboundID
		}
		return rules[i].Proto < rules[j].Proto
	})
	return rules
}

// Users' connections carry the socket mark userMarkBase|id (set by the
// panel-rendered kernel config). Packets out with it are the user's upload;
// the mark is copied to the connection so packets coming back count as the
// download.
const (
	userMarkBase = 0x46560000
	userMarkMax  = 0xffff
)

func userCounterName(dir string, id int64) string { return fmt.Sprintf("u%s_%d", dir, id) }

func sortedUsers(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 && id <= userMarkMax && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// hopRule redirects a UDP port range to an inbound port (port hopping).
type hopRule struct {
	From, To, Port int
}

// expr is the single nft statement this rule expands to. It is identical in
// the prerouting and output hop chains, and nftScript renders it through
// here so the per-rule probe below can never drift from production output.
func (h hopRule) expr() string {
	return fmt.Sprintf("fib daddr type local udp dport %d-%d redirect to :%d", h.From, h.To, h.Port)
}

// name identifies the rule in logs and errors: its position in the sorted
// rule list plus its range and target port.
func (h hopRule) name(idx int) string {
	return fmt.Sprintf("hop[%d] udp %d-%d -> :%d", idx, h.From, h.To, h.Port)
}

// hopSkip records one hop rule the running kernel rejected, with the reason.
type hopSkip struct {
	index int
	rule  hopRule
	err   error
}

func (s hopSkip) String() string {
	return fmt.Sprintf("%s (%s): %s", s.rule.name(s.index), s.rule.expr(), truncateNftErr(s.err.Error()))
}

// truncateNftErr caps nft's stderr for logs and error messages so one bad
// rule cannot flood them.
func truncateNftErr(s string) string {
	const max = 200
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

const nftHopProbeTable = "flmx_hop_probe"

// nftHopCheck validates a single hop rule against the running kernel by
// asking nft to check (-c) a minimal script containing only that rule.
// It is a variable so tests can inject a fake validator without nft.
// F-091 补强：hop 规则的 fib 表达式依赖内核 nft_fib，老内核整表下发会
// 因一条不支持的规则全盘失败，故下发前逐条预检、坏条单独摘除。
var nftHopCheck = func(ctx context.Context, h hopRule) error {
	_, err := nftRunner(ctx, hopProbeScript(h), "-c", "-f", "-")
	return err
}

func hopProbeScript(h hopRule) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {\n", nftHopProbeTable)
	for _, hook := range []string{"prerouting", "output"} {
		fmt.Fprintf(&b, "\tchain hop_%s {\n\t\ttype nat hook %s priority -100; policy accept;\n\t\t%s\n\t}\n", hook, hook, h.expr())
	}
	b.WriteString("}\n")
	return b.String()
}

// filterHopRules partitions hops into those the kernel accepts and those it
// rejects, preserving order and each rejected rule's original index.
func filterHopRules(ctx context.Context, hops []hopRule) (kept []hopRule, skipped []hopSkip) {
	kept = make([]hopRule, 0, len(hops))
	for i, h := range hops {
		if err := nftHopCheck(ctx, h); err != nil {
			skipped = append(skipped, hopSkip{index: i, rule: h, err: err})
			continue
		}
		kept = append(kept, h)
	}
	return kept, skipped
}

func expandHops(ports []Port) []hopRule {
	var hops []hopRule
	for _, p := range ports {
		if p.HopFrom > 0 && p.HopTo > p.HopFrom {
			hops = append(hops, hopRule{From: p.HopFrom, To: p.HopTo, Port: p.Port})
		}
	}
	sort.Slice(hops, func(i, j int) bool { return hops[i].From < hops[j].From })
	return hops
}

// portsHaveWhitelist reports whether any port is restricted to a
// source whitelist. A whitelisted port is only protected while its
// rules are actually installed in the nft table, so unlike traffic
// counting a whitelist makes the sync security-critical.
func portsHaveWhitelist(ports []Port) bool {
	for _, p := range ports {
		if len(p.Allow) > 0 {
			return true
		}
	}
	return false
}

// reqHasWhitelist reports whether an enabled request carries at least
// one whitelisted port.
func reqHasWhitelist(req ApplyRequest) bool {
	return req.Enabled && portsHaveWhitelist(req.Ports)
}

// allowGuard is the source whitelist of one inbound, split by family.
type allowGuard struct {
	v4, v6 []string
}

// expandGuards collects the whitelists keyed by inbound. Entries were
// validated as CIDRs; they are re-rendered from the parsed form so nothing
// but an address can reach the nft script. The skip keys on the same
// predicate as portsHaveWhitelist (len > 0): an empty allow list is the
// same as an absent one — no restriction, no guard — so a payload
// carrying "allow": [] cannot render a drop-all ruleset for a port the
// guard classification does not treat as protected (C3-01).
func expandGuards(ports []Port) map[int64]allowGuard {
	guards := make(map[int64]allowGuard)
	for _, p := range ports {
		if len(p.Allow) == 0 {
			continue
		}
		g := guards[p.InboundID]
		for _, c := range p.Allow {
			_, ipnet, err := net.ParseCIDR(c)
			if err != nil {
				continue
			}
			if ipnet.IP.To4() != nil {
				g.v4 = append(g.v4, ipnet.String())
			} else {
				g.v6 = append(g.v6, ipnet.String())
			}
		}
		sort.Strings(g.v4)
		sort.Strings(g.v6)
		guards[p.InboundID] = g
	}
	return guards
}

func writeSet(b *strings.Builder, name, typ string, elems []string) {
	fmt.Fprintf(b, "\tset %s {\n\t\ttype %s\n\t\tflags interval\n\t\tauto-merge\n", name, typ)
	if len(elems) > 0 {
		fmt.Fprintf(b, "\t\telements = { %s }\n", strings.Join(elems, ", "))
	}
	b.WriteString("\t}\n")
}

// nftScript builds the atomic ruleset. Chains have policy accept and only
// count, except that a port with a whitelist drops packets from sources not
// on it (only that port; nothing else on the host is affected).
//
// Hop ranges are redirected before filtering, so counters and whitelists see
// the inbound port. Only traffic addressed to this host is redirected; the
// output hook covers clients running on the node itself.
func nftScript(rules []counterRule, guards map[int64]allowGuard, hops []hopRule, users []int64) string {
	return nftScriptGuard(rules, guards, hops, users, nil)
}

func nftScriptGuard(rules []counterRule, guards map[int64]allowGuard, hops []hopRule, users []int64, guard *snellGuard) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {}\n", nftTable)
	fmt.Fprintf(&b, "delete table inet %s\n", nftTable)
	fmt.Fprintf(&b, "table inet %s {}\n", legacyNFTTable)
	fmt.Fprintf(&b, "delete table inet %s\n", legacyNFTTable)
	if len(rules) == 0 && len(users) == 0 && guard == nil {
		return b.String()
	}
	fmt.Fprintf(&b, "table inet %s {\n", nftTable)
	if guard != nil {
		writeSet(&b, "snell_priv4", "ipv4_addr", snellPrivateV4)
		writeSet(&b, "snell_priv6", "ipv6_addr", snellPrivateV6All())
	}
	for _, r := range rules {
		fmt.Fprintf(&b, "\tcounter %s {}\n", r.name("in"))
		fmt.Fprintf(&b, "\tcounter %s {}\n", r.name("out"))
	}
	for _, id := range users {
		fmt.Fprintf(&b, "\tcounter %s {}\n", userCounterName("up", id))
		fmt.Fprintf(&b, "\tcounter %s {}\n", userCounterName("down", id))
	}
	guarded := make([]int64, 0, len(guards))
	for id := range guards {
		guarded = append(guarded, id)
	}
	sort.Slice(guarded, func(i, j int) bool { return guarded[i] < guarded[j] })
	for _, id := range guarded {
		writeSet(&b, fmt.Sprintf("allow4_%d", id), "ipv4_addr", guards[id].v4)
		writeSet(&b, fmt.Sprintf("allow6_%d", id), "ipv6_addr", guards[id].v6)
	}
	b.WriteString("\tchain input {\n\t\ttype filter hook input priority -150; policy accept;\n")
	for _, r := range rules {
		if _, ok := guards[r.InboundID]; !ok {
			continue
		}
		fmt.Fprintf(&b, "\t\tmeta nfproto ipv4 %s dport %d ip saddr != @allow4_%d drop\n", r.Proto, r.Port, r.InboundID)
		fmt.Fprintf(&b, "\t\tmeta nfproto ipv6 %s dport %d ip6 saddr != @allow6_%d drop\n", r.Proto, r.Port, r.InboundID)
	}
	for _, r := range rules {
		fmt.Fprintf(&b, "\t\t%s dport %d counter name %s\n", r.Proto, r.Port, r.name("in"))
	}
	for _, id := range users {
		fmt.Fprintf(&b, "\t\tct mark 0x%08x counter name %s\n", userMarkBase|id, userCounterName("down", id))
	}
	b.WriteString("\t}\n\tchain output {\n\t\ttype filter hook output priority -150; policy accept;\n")
	if guard != nil {
		// Snell egress guard (see snell_guard.go): the system resolvers
		// stay reachable on DNS ports so domain targets keep resolving;
		// packets of an already established flow pass (they are replies
		// to sessions a client opened TO the Snell inbound — that client
		// may itself sit on a private address, and its source address
		// must not make the session's replies look like a private
		// target); every other packet from the guard uid to a
		// non-public range is dropped by destination IP, whatever name
		// it came from. The established exemption cannot open a private
		// target: a flow reaches the established state only after a
		// reply packet was seen, and the guard uid's opening packet
		// towards a non-public range is dropped below before it can
		// leave the host, so no such flow ever gets there.
		for _, r := range guard.Resolvers {
			fam := "ip"
			if strings.Contains(r, ":") {
				fam = "ip6"
			}
			fmt.Fprintf(&b, "\t\tmeta skuid %d %s daddr %s udp dport 53 accept\n", guard.UID, fam, r)
			fmt.Fprintf(&b, "\t\tmeta skuid %d %s daddr %s tcp dport { 53, 853 } accept\n", guard.UID, fam, r)
		}
		fmt.Fprintf(&b, "\t\tmeta skuid %d ct state established,related accept\n", guard.UID)
		fmt.Fprintf(&b, "\t\tmeta skuid %d ip daddr @snell_priv4 drop\n", guard.UID)
		fmt.Fprintf(&b, "\t\tmeta skuid %d ip6 daddr @snell_priv6 drop\n", guard.UID)
	}
	for _, r := range rules {
		fmt.Fprintf(&b, "\t\t%s sport %d counter name %s\n", r.Proto, r.Port, r.name("out"))
	}
	for _, id := range users {
		fmt.Fprintf(&b, "\t\tmeta mark 0x%08x ct mark set meta mark counter name %s\n", userMarkBase|id, userCounterName("up", id))
	}
	b.WriteString("\t}\n")
	if len(hops) > 0 {
		for _, hook := range []string{"prerouting", "output"} {
			fmt.Fprintf(&b, "\tchain hop_%s {\n\t\ttype nat hook %s priority -100; policy accept;\n", hook, hook)
			for _, h := range hops {
				fmt.Fprintf(&b, "\t\t%s\n", h.expr())
			}
			b.WriteString("\t}\n")
		}
	}
	b.WriteString("}\n")
	return b.String()
}

type nftState struct {
	Signature string `json:"signature"`
	Epoch     string `json:"epoch"`
	// Content is the fingerprint of the live table's rendered content
	// (counter values masked) captured right after the last successful
	// apply. The signature alone only proves what this process last
	// TRIED to install; the fingerprint proves what the kernel table
	// actually holds, so a table flushed or rewritten out-of-band is
	// detected instead of trusted on existence alone. Empty for state
	// files written before the fingerprint existed; the next apply
	// re-arms it.
	Content string `json:"content,omitempty"`
}

// nftCounters owns the flmx_kernel table (flvx_kernel is only ever
// recognised and deleted, never counted). The epoch changes whenever
// the table is rebuilt so the panel knows the counters restarted
// from zero.
type nftCounters struct {
	mu        sync.Mutex
	statePath string
	state     nftState
	lastErr   string
}

func newNftCounters(baseDir string) *nftCounters {
	n := &nftCounters{statePath: filepath.Join(baseDir, "nft.json")}
	if b, err := os.ReadFile(n.statePath); err == nil {
		_ = json.Unmarshal(b, &n.state)
	}
	return n
}

// counterValuesPattern matches the one volatile part of a rendered
// table: the packet/byte counts of the named counters, which change
// with traffic. Everything else in the rendering — chains, rules,
// sets and their elements, counter names — is content.
var counterValuesPattern = regexp.MustCompile(`packets [0-9]+ bytes [0-9]+`)

// tableFingerprint hashes the live table's rendered content with the
// counter values masked out. nft renders a given ruleset
// deterministically, so the fingerprint changes exactly when the
// table's rules, sets or structure change — no matter who changed
// them — and stays stable while only traffic flows.
func (n *nftCounters) tableFingerprint(ctx context.Context) (string, error) {
	out, err := nftRunner(ctx, "", "list", "table", "inet", nftTable)
	if err != nil {
		return "", err
	}
	return hashString(counterValuesPattern.ReplaceAllString(string(out), "packets # bytes #")), nil
}

// removeTables 删除 agent 建的全部 nftables 表（计数/白名单主表、
// 改名前的旧表、端口跳跃探针表），供 agent 卸载时调用：表驻留在
// 内核里，不随进程退出消失，必须在进程还活着时显式删掉。脚本沿用
// nftScriptGuard 的「先建空表再删」惯例，表不存在时也不报错。
func (n *nftCounters) removeTables() error {
	var b strings.Builder
	for _, table := range []string{nftTable, legacyNFTTable, nftHopProbeTable} {
		fmt.Fprintf(&b, "table inet %s {}\n", table)
		fmt.Fprintf(&b, "delete table inet %s\n", table)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := nftRunner(ctx, b.String(), "-f", "-")
	return err
}

func (n *nftCounters) sync(ports []Port, users []int64) error {
	return n.syncGuard(ports, users, nil)
}

// hopRulesError reports that every port-hop rule was rejected by the
// kernel (typically an old kernel without nft_fib) while the main
// ruleset — traffic counters, inbound whitelists, the Snell egress
// guard — was applied successfully. syncGuard returns it so the
// failure stays named and per-rule, but it is not a ruleset failure:
// port hopping is a reachability nicety outside the security posture,
// so gating callers must not block a kernel start on it or report it
// as a whitelist/guard sync failure (R2-F2-E02). Manager call sites
// split it off with isolateHopSync.
type hopRulesError struct{ msg string }

func (e *hopRulesError) Error() string { return e.msg }

// syncGuard is sync with the Snell egress guard folded into the same
// atomic table rebuild. Unlike traffic counting (best-effort: a node
// without nftables just loses its counters), the guard is a security
// control — when a guard is requested, a missing nft binary or a failed
// ruleset load is a hard error so the caller can refuse to run Snell
// unguarded.
func (n *nftCounters) syncGuard(ports []Port, users []int64, guard *snellGuard) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := nftAvailable(); err != nil {
		if guard != nil {
			n.lastErr = "Snell 私网拦截需要 nftables: " + err.Error()
			return fmt.Errorf("Snell 私网拦截需要 nftables: %v", err)
		}
		if portsHaveWhitelist(ports) {
			n.lastErr = "入站白名单需要 nftables: " + err.Error()
			return fmt.Errorf("入站白名单需要 nftables: %v", err)
		}
		n.lastErr = "未安装 nftables，无法统计流量"
		return nil
	}
	rules := expandPorts(ports)
	users = sortedUsers(users)
	hops := expandHops(ports)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// F-091 补强：逐条预检 hop 规则。单条被内核拒绝（典型：老内核无
	// nft_fib）时只摘除该条并告警，其余 hop 与计数/白名单照常下发，不再
	// 整表陪葬；全部被拒时计数仍下发，但返回逐条点名的错误让失败可见。
	var hopErr error
	if len(hops) > 0 {
		var skipped []hopSkip
		hops, skipped = filterHopRules(ctx, hops)
		for _, s := range skipped {
			log.Printf("WARN: hop rule skipped: %s", s)
		}
		if len(hops) == 0 {
			parts := make([]string, len(skipped))
			for i, s := range skipped {
				parts[i] = s.String()
			}
			hopErr = &hopRulesError{msg: fmt.Sprintf("全部 %d 条 hop 规则被内核拒绝，端口跳跃未生效（计数与白名单规则已照常下发）: %s",
				len(skipped), strings.Join(parts, "; "))}
		}
	}

	script := nftScriptGuard(rules, expandGuards(ports), hops, users, guard)
	sig := hashString(script)
	hasContent := len(rules) > 0 || len(users) > 0 || guard != nil
	if sig == n.state.Signature {
		if !hasContent {
			n.lastErr = ""
			return nil
		}
		// The signature only proves what this process last tried to
		// install. The table lives in the kernel, outside the agent:
		// it can be flushed or rewritten by other tools while guarded
		// kernels keep running, and an existence check would never
		// notice (A-03). Skip the re-apply only when the live content
		// still matches the fingerprint captured at the last apply;
		// anything else — missing table, unreadable table, tampered
		// content, or a state file predating fingerprints — falls
		// through to a full re-apply below.
		if n.state.Content != "" {
			if fp, err := n.tableFingerprint(ctx); err == nil && fp == n.state.Content {
				n.lastErr = ""
				return nil
			}
		}
	}
	if _, err := nftRunner(ctx, script, "-f", "-"); err != nil {
		n.lastErr = err.Error()
		return err
	}
	// Capture what the kernel actually holds now, so later syncs can
	// verify content instead of existence. A failed capture leaves
	// Content empty; the next sync then re-applies once and retries
	// the capture — degraded, never wrongly trusted.
	content, _ := n.tableFingerprint(ctx)
	n.state = nftState{Signature: sig, Epoch: newEpoch(), Content: content}
	if b, err := json.Marshal(n.state); err == nil {
		_ = writeFileAtomic(n.statePath, b, 0o600)
	}
	if hopErr != nil {
		n.lastErr = hopErr.Error()
		return hopErr
	}
	n.lastErr = ""
	return nil
}

type nftListOutput struct {
	Nftables []struct {
		Counter *struct {
			Name  string `json:"name"`
			Table string `json:"table"`
			Bytes int64  `json:"bytes"`
		} `json:"counter"`
	} `json:"nftables"`
}

// read returns the current counters together with the epoch they belong to.
func (n *nftCounters) read() ([]Counter, []UserCounter, string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := nftAvailable(); err != nil {
		return nil, nil, "", fmt.Errorf("未安装 nftables，无法统计流量")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := nftRunner(ctx, "", "-j", "list", "counters", "table", "inet", nftTable)
	if err != nil {
		return nil, nil, "", err
	}
	counters, users, err := parseCounters(out)
	return counters, users, n.state.Epoch, err
}

func parseCounters(raw []byte) ([]Counter, []UserCounter, error) {
	var parsed nftListOutput
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, nil, fmt.Errorf("解析 nft 计数失败: %v", err)
	}
	var counters []Counter
	var users []UserCounter
	for _, item := range parsed.Nftables {
		c := item.Counter
		if c == nil || c.Table != nftTable {
			continue
		}
		parts := strings.Split(c.Name, "_")
		if len(parts) == 2 && (parts[0] == "uup" || parts[0] == "udown") {
			if id, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				users = append(users, UserCounter{ID: id, Dir: parts[0][1:], Bytes: c.Bytes})
			}
			continue
		}
		if len(parts) != 3 || (parts[0] != "in" && parts[0] != "out") {
			continue
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		counters = append(counters, Counter{InboundID: id, Proto: parts[2], Dir: parts[0], Bytes: c.Bytes})
	}
	return counters, users, nil
}

func newEpoch() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
