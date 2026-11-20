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

// legacyAgentNFTTable 是 legacy 代（/etc/flux_agent）agent 的计数
// 表名（F-R4-02）：legacy 根与 current 根不得共享 plain 表，否则
// 一方 sync 即清另一方计数器/白名单（F2-NFT-01 机制）。该表只由
// legacy agent 自身同步；current 代的常规 sync 不得碰它（legacy
// agent 活着时那是它的 live 表），卸载时也只在确认无 legacy
// agent 存活后才清理（R18-C3-06/M-4：先判存活再删表）。
const legacyAgentNFTTable = "flmx_kernel_legacy"

// legacyAgentDir 是 legacy 代 agent 的安装根（/etc/flux_agent，
// legacyBaseDir 的父目录）：在其下执行二进制的进程即存活的
// legacy 代 agent。
const legacyAgentDir = "/etc/flux_agent"

// legacyAgentProcRoot 是存活探测扫描的 /proc 根；测试可指向伪造
// 的进程树。生产上恒为 /proc。
var legacyAgentProcRoot = "/proc"

// legacyAgentAlive 报告是否有 legacy 代 agent 进程正在运行；
// removeTables 在删 flmx_kernel_legacy 前先问它。它是变量，
// 测试可注入确定性的存活/死亡，不依赖本机进程状态。
var legacyAgentAlive = func() bool { return legacyAgentRunning() }

// legacyAgentRunning 扫描 /proc：任一进程的 exe 指向
// legacyAgentDir 下、或其命令行含该路径，即 legacy 代 agent 存
// 活。读不到 /proc（或权限不足无法判断）时按存活处理
// fail-closed：删一张可能仍是 live 的白名单表，远比卸载后留一
// 张残留表严重（残留表可由运维扫除，白名单窗口却是暴露期）。
// 非 Linux 上无 /proc，读失败同样按存活处理——nft 本就只在
// Linux 上有意义，卸载路径上宁可留表、不删他方 live 表。
func legacyAgentRunning() bool {
	entries, err := os.ReadDir(legacyAgentProcRoot)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if pid, err := strconv.Atoi(e.Name()); err != nil || pid <= 0 {
			continue
		}
		pdir := filepath.Join(legacyAgentProcRoot, e.Name())
		if exe, err := os.Readlink(filepath.Join(pdir, "exe")); err == nil {
			if exe == legacyAgentDir || strings.HasPrefix(exe, legacyAgentDir+"/") {
				return true
			}
		}
		if cmd, err := os.ReadFile(filepath.Join(pdir, "cmdline")); err == nil {
			if strings.Contains(string(cmd), legacyAgentDir+"/") {
				return true
			}
		}
	}
	return false
}

// nftTableForBaseDir derives this agent's nft table name from its
// kernel root (F2-NFT-01, P1-1): two agents on one host must never
// share a table, because every sync deletes the table and rebuilds
// it from the syncing agent's own ports — a second agent's sync
// used to wipe the first agent's counters and whitelists, and its
// nft.json fingerprint overwrote the first agent's.
//
// Every root gets flmx_kernel_<first 8 hex of sha256(root)>: stable
// across restarts (same root, same table), distinct per install,
// and a valid nft identifier (20 chars, well under the name limit).
// The canonical root used to keep the plain shared name — that was
// the hole: any same-lineage install at the same canonical root
// (a second panel's agent, an un-upgraded foreign fork) shared the
// table with us, so one side's sync wiped the other's counters and
// whitelists. The container form gets a hash too: its network
// namespace is private in the normal case, but a host-network
// container shares the host namespace, and uniformity leaves no
// plain-named table behind for old logic to delete and rebuild.
// The legacy pre-rename root keeps its own fixed table
// (F-R4-02): it is already install-scoped (no other root maps to
// it), and it is the live table of a possibly still-running
// legacy-generation agent — renaming it would strand that agent's
// counters and force a choice between orphaning its table and
// deleting a live table, so the name stays.
func nftTableForBaseDir(baseDir string) string {
	if baseDir == legacyBaseDir {
		return legacyAgentNFTTable
	}
	return nftTable + "_" + hashString(baseDir)[:8]
}

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
	// Bind is the inbound's listen address (FIX-031); empty or
	// wildcard means the rule stays unconditioned (see nftBindMatch).
	Bind string
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
			r := counterRule{InboundID: p.InboundID, Port: p.Port, Proto: proto, Bind: p.Bind}
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

// Users' connections carry the socket mark markBase|id (set by the
// panel-rendered kernel config, markBase = the per-agent base the
// panel sent in the KernelApply payload, falling back to the legacy
// userMarkBase when the payload omits it). Packets out with it are
// the user's upload; the mark is copied to the connection so packets
// coming back count as the download. F2-DBL-01 / E-01: the base is
// per-agent — the agent byte in bits 16-23 is the first byte of
// SHA-256 of the node's secret, so co-located agents count only
// their own kernels' traffic instead of the same packets N×, even
// when two panels give their nodes the same id.
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

// tombstoneUserID maps a tombstoned auth_user name ("u<N>", see
// User.Name in the panel) back to its user id, for the nft reject
// rule (V-D). Names that do not match the shape are not blocked at
// the nft layer — the sweeper still closes them by name — so a bad
// shape must never break the render.
func tombstoneUserID(name string) (int64, bool) {
	if !strings.HasPrefix(name, "u") {
		return 0, false
	}
	id, err := strconv.ParseInt(name[1:], 10, 64)
	if err != nil || id <= 0 || id > userMarkMax {
		return 0, false
	}
	return id, true
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

// nftBindMatch renders the address match for one bound counter rule
// (FIX-031, AUDIT-HIST): packets to the same port on any other local
// address were counted toward the bound inbound. dir "in" matches the
// packet's destination (input chain), dir "out" its source (output
// chain — replies leave from the bind address). A wildcard listen
// ("", "0.0.0.0", "::") or an unparsable value matches every local
// address, so it renders no condition and the rule is byte-identical
// to the old port-only form. A specific IPv4 bind renders
// "ip daddr/saddr X"; a specific IPv6 bind renders "ip6 daddr/saddr X".
func nftBindMatch(bind, dir string) string {
	ip := net.ParseIP(strings.TrimSpace(bind))
	if ip == nil || ip.IsUnspecified() {
		return ""
	}
	verb := "daddr"
	if dir == "out" {
		verb = "saddr"
	}
	if ip4 := ip.To4(); ip4 != nil {
		return fmt.Sprintf("ip %s %s", verb, ip4.String())
	}
	return fmt.Sprintf("ip6 %s %s", verb, ip.String())
}

// nftScript builds the atomic ruleset for the given table. Chains
// have policy accept and only count, except that a port with a
// whitelist drops packets from sources not on it (only that port;
// nothing else on the host is affected).
//
// Hop ranges are redirected before filtering, so counters and whitelists see
// the inbound port. Only traffic addressed to this host is redirected; the
// output hook covers clients running on the node itself.
//
// markBase is the socket-mark base user marks are minted with
// (F2-DBL-01, per-agent); 0 reads as the legacy userMarkBase.
// nftScript renders the whole table (delete + recreate) from the
// desired state. blocked are tombstoned user ids (V-D): their marks
// are rejected first in the output chain, so a new outbound dial
// admitted by a still-running old config dies at the handshake
// instead of draining through the sweeper's graceful close. blocked
// is expected sorted and deduplicated (sync does that via
// sortedUsers), like users.
func nftScript(table string, rules []counterRule, guards map[int64]allowGuard, hops []hopRule, users []int64, markBase int64, blocked []int64) string {
	if markBase == 0 {
		markBase = userMarkBase
	}
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {}\n", table)
	fmt.Fprintf(&b, "delete table inet %s\n", table)
	if len(rules) == 0 && len(users) == 0 && len(blocked) == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, "table inet %s {\n", table)
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
		// FIX-031: a bound inbound's counter only matches traffic
		// addressed to/from its BindIP; unbound rules are unchanged.
		if m := nftBindMatch(r.Bind, "in"); m != "" {
			fmt.Fprintf(&b, "\t\t%s %s dport %d counter name %s\n", m, r.Proto, r.Port, r.name("in"))
		} else {
			fmt.Fprintf(&b, "\t\t%s dport %d counter name %s\n", r.Proto, r.Port, r.name("in"))
		}
	}
	for _, id := range users {
		fmt.Fprintf(&b, "\t\tct mark 0x%08x counter name %s\n", markBase|id, userCounterName("down", id))
	}
	b.WriteString("\t}\n\tchain output {\n\t\ttype filter hook output priority -150; policy accept;\n")
	// V-D: a tombstoned user's new outbound dial dies here, at the
	// handshake — the running config still admits them (a
	// removal-only change skips the reload), so without this the
	// sweeper's graceful close is the only enforcement and a fast
	// transfer finishes before it. Bare `reject` is protocol-aware
	// (TCP RST for TCP, ICMP for UDP); the rules sit first so
	// blocked traffic is never counted and its conntrack mark is
	// never set. An already-established connection of a tombstoned
	// user dies here too, as a backstop to the Clash API close.
	for _, id := range blocked {
		fmt.Fprintf(&b, "\t\tmeta mark 0x%08x reject\n", markBase|id)
	}
	for _, r := range rules {
		if m := nftBindMatch(r.Bind, "out"); m != "" {
			fmt.Fprintf(&b, "\t\t%s %s sport %d counter name %s\n", m, r.Proto, r.Port, r.name("out"))
		} else {
			fmt.Fprintf(&b, "\t\t%s sport %d counter name %s\n", r.Proto, r.Port, r.name("out"))
		}
	}
	for _, id := range users {
		fmt.Fprintf(&b, "\t\tmeta mark 0x%08x ct mark set meta mark counter name %s\n", markBase|id, userCounterName("up", id))
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
	// Migrated records that the pre-fix plain tables (flmx_kernel,
	// flvx_kernel) have been dealt with: true after any successful
	// apply by this build — either the one-shot migration ran, or
	// this install never owned a plain table (fresh install, or a
	// root whose table never changed). A pre-fix state file has no
	// field, so it reads false and the first post-upgrade sync
	// migrates (see needsTableMigration).
	Migrated bool `json:"migrated,omitempty"`
}

// nftCounters owns this agent's nft table (see nftTableForBaseDir:
// flmx_kernel_<hash> per install root, flmx_kernel_legacy for the
// legacy root). The epoch changes whenever the table is rebuilt so
// the panel knows the counters restarted from zero.
type nftCounters struct {
	mu sync.Mutex
	// installMu serializes concurrent auto-installs only. It is
	// never held across state access, so a slow install (up to
	// nftInstallTimeout) cannot starve read()/setMarkBase() the
	// way holding mu across the install did (R18-D3-02).
	installMu sync.Mutex
	statePath string
	// baseDir is this agent's kernel root. It decides whether this
	// install ever owned a shared plain table (hadPlainTable) and
	// hence whether the upgrade migration and the uninstall legacy
	// cleanup apply; an empty baseDir (zero value) owns none.
	baseDir string
	// table is this agent's own nft table. Empty (zero value) reads
	// as the plain name so a bare &nftCounters{} stays usable.
	table string
	// markBase is the socket-mark base user marks are minted with
	// (F2-DBL-01, per-agent; E-01: derived by the panel from the
	// node secret, adopted verbatim here). 0 reads as the legacy
	// userMarkBase (old panel that does not send a base).
	markBase int64
	state    nftState
	lastErr  string
	// autoInstalled 表示本进程曾为白名单自动装上 nftables（供状态
	// 上报区分“已安装”与“已自动安装”；进程重启后复位为 false，
	// 此后 nft 在场即报“已安装”）。
	autoInstalled bool
	// installErr 是最近一次自动安装失败的原因（"" 表示无失败或
	// 已恢复）；只由安装路径写，面板据此展示“安装失败＋原因”。
	installErr string
}

func newNftCounters(baseDir string) *nftCounters {
	n := &nftCounters{statePath: filepath.Join(baseDir, "nft.json"), baseDir: baseDir, table: nftTableForBaseDir(baseDir)}
	if b, err := os.ReadFile(n.statePath); err == nil {
		_ = json.Unmarshal(b, &n.state)
	}
	return n
}

// hadPlainTable reports whether this install's pre-fix table was a
// shared plain name (flmx_kernel): the canonical root, or any root
// in the container form (the pre-fix container branch returned the
// plain name for every root). Only such an install can own the
// residual plain tables the upgrade migration removes; a scratch,
// relocated, or legacy-root install never had one, so its syncs
// must never touch them — they may be another agent's live table
// (F2-NFT-01).
func (n *nftCounters) hadPlainTable() bool {
	return n.baseDir == defaultBaseDir || detectKernelContainer()
}

// needsTableMigration reports whether this sync must run the
// one-shot upgrade migration (delete the pre-fix plain tables
// flmx_kernel and flvx_kernel): only an install that previously
// applied with the old code (a persisted signature exists — a
// fresh install has none, so a plain table on the host cannot be
// ours) and previously owned a plain table. After any successful
// apply the state records Migrated, so the migration runs at most
// once; a failed apply leaves the flag clear and the next sync
// retries.
func (n *nftCounters) needsTableMigration() bool {
	return !n.state.Migrated && n.state.Signature != "" && n.hadPlainTable()
}

// legacyPlainTableCleanupScript deletes the pre-fix plain tables
// (this install's own old tables) with the usual create-empty-
// then-delete idiom, so missing tables are a no-op. It is prepended
// to the sync script by sync(), never rendered on its own.
func legacyPlainTableCleanupScript() string {
	var b strings.Builder
	for _, table := range []string{nftTable, legacyNFTTable} {
		fmt.Fprintf(&b, "table inet %s {}\ndelete table inet %s\n", table, table)
	}
	return b.String()
}

// setMarkBase adopts the socket-mark base the panel sent in the
// KernelApply payload. A zero base (old panel) is ignored — the
// legacy base stays in force.
func (n *nftCounters) setMarkBase(base int64) {
	if base == 0 {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.markBase = base
}

// tableName returns this counter's table, defaulting to the plain
// name for a zero-value counter.
func (n *nftCounters) tableName() string {
	if n.table == "" {
		return nftTable
	}
	return n.table
}

// markCollision is one of this agent's socket marks found in another
// nft table: another agent (or panel) is minting the same mark, so
// both agents count the same packets into the same subscription
// (E-01). Only the table and the mark are named — no traffic data.
type markCollision struct {
	Table string
	Mark  int64
}

func (c markCollision) String() string {
	return fmt.Sprintf("mark 0x%08x in table %s", c.Mark, c.Table)
}

// markInRuleset matches a mark value in a rendered ruleset line:
// "ct mark 0x47010002" (input chain) or "meta mark 0x47010002"
// (output chain). nft prints marks in hex; the value is compared
// numerically so padding differences cannot hide a collision.
var markInRuleset = regexp.MustCompile(`mark 0x([0-9a-fA-F]+)`)

// tableInRuleset matches a table header line: "table inet
// flmx_kernel_abc12345 {".
var tableInRuleset = regexp.MustCompile(`^table\s+\S+\s+(\S+)\s*\{`)

// findMarkCollisions scans a `nft list ruleset` dump for this agent's
// marks (markBase|id) in tables other than its own. It is pure and
// table-driven so tests can feed canned ruleset output; the live
// listing comes from detectMarkCollision. A line with no "mark 0x"
// token (e.g. "ct mark set meta mark") cannot collide and is skipped.
func findMarkCollisions(ruleset, ownTable string, marks map[int64]bool) []markCollision {
	if len(marks) == 0 {
		return nil
	}
	seen := map[markCollision]bool{}
	var out []markCollision
	table := ""
	for _, line := range strings.Split(ruleset, "\n") {
		trimmed := strings.TrimSpace(line)
		if m := tableInRuleset.FindStringSubmatch(trimmed); m != nil {
			table = m[1]
			continue
		}
		if table == "" || table == ownTable {
			continue
		}
		for _, m := range markInRuleset.FindAllStringSubmatch(line, -1) {
			v, err := strconv.ParseInt(m[1], 16, 64)
			if err != nil || !marks[v] {
				continue
			}
			c := markCollision{Table: table, Mark: v}
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Mark < out[j].Mark
	})
	return out
}

// detectMarkCollision lists the live ruleset and reports this agent's
// marks in other tables. It only runs for per-agent bases (top byte
// 0x47 — the 0x46 space also holds the legacy base itself, so a hit
// there cannot distinguish a collision from legacy counting and
// would be noise) and only when user marks exist. Best-effort: a
// failed listing (nft missing, timeout) returns nil and never blocks
// the apply that called it.
func (n *nftCounters) detectMarkCollision(users []int64) []markCollision {
	n.mu.Lock()
	base := n.markBase
	table := n.tableName()
	n.mu.Unlock()
	if base>>24 != 0x47 || len(users) == 0 {
		return nil
	}
	marks := make(map[int64]bool, len(users))
	for _, id := range sortedUsers(users) {
		marks[base|id] = true
	}
	if len(marks) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := nftRunner(ctx, "", "list", "ruleset")
	if err != nil {
		return nil
	}
	return findMarkCollisions(string(out), table, marks)
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
	out, err := nftRunner(ctx, "", "list", "table", "inet", n.tableName())
	if err != nil {
		return "", err
	}
	return hashString(counterValuesPattern.ReplaceAllString(string(out), "packets # bytes #")), nil
}

// removeTables 删除 agent 建的全部 nftables 表（本 agent 自己的
// 计数/白名单主表、改名前的旧表、legacy 代的表、端口跳跃探针表），
// 供 agent 卸载时调用：表驻留在内核里，不随进程退出消失，必须在
// 进程还活着时显式删掉。脚本沿用 nftScript 的「先建空表再删」惯例，
// 表不存在时也不报错。
// F2-NFT-01: 只删自己的表——旧表仅曾拥有 plain 表的安装（canonical
// 根或容器形态，见 hadPlainTable）删（对其他安装，它可能是另一
// agent 的 live 表）；plain 主表本身也只在升级迁移从未跑过、且有
// 旧签名的安装上删（那是本安装自己的旧表），全新安装绝不碰它。
// F-R4-02：canonical 卸载一并清 legacy 代的表（迁移收尾）——但
// 只在确认无 legacy 代 agent 存活后（R18-C3-06/M-4）：legacy
// agent 活着时 flmx_kernel_legacy 是它的 live 表，删了它的入站
// 白名单在窗口内失效；存活时跳过该表并在卸载输出中点名，残留
// 表待 legacy agent 自身卸载或运维扫除。legacy agent 自身卸载
// 只删自己的表，绝不碰 plain 表（那可能是 current 代的 live 表）。
func (n *nftCounters) removeTables() error {
	var b strings.Builder
	tables := []string{n.tableName()}
	if n.hadPlainTable() {
		tables = append(tables, legacyNFTTable)
		// R18-C3-06/M-4：先判 legacy agent 是否存活再删表。
		if legacyAgentAlive() {
			log.Printf("agent uninstall: legacy agent still running, keeping its live table %s (remove it after the legacy agent is uninstalled)", legacyAgentNFTTable)
		} else {
			tables = append(tables, legacyAgentNFTTable)
		}
		if !n.state.Migrated && n.state.Signature != "" {
			tables = append(tables, nftTable)
		}
	}
	tables = append(tables, nftHopProbeTable)
	for _, table := range tables {
		fmt.Fprintf(&b, "table inet %s {}\n", table)
		fmt.Fprintf(&b, "delete table inet %s\n", table)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := nftRunner(ctx, b.String(), "-f", "-")
	return err
}

// removeOwnTable deletes only this agent's own nft table (R2C-02):
// the graceful-exit reclaim. Unlike removeTables it never touches
// the legacy tables — a live legacy agent's table must survive a
// current-agent restart — and never the hop probe table. The table
// is rebuilt from scratch by the next sync anyway, and the panel's
// nft collection only polls online nodes, so no consumer can still
// read this table after exit. Kill -9 / crash exits cannot run this;
// residue from unclean exits still needs an operator sweep.
func (n *nftCounters) removeOwnTable() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	script := fmt.Sprintf("table inet %s {}\ndelete table inet %s\n", n.tableName(), n.tableName())
	_, err := nftRunner(ctx, script, "-f", "-")
	return err
}

// hopRulesError reports that every port-hop rule was rejected by the
// kernel (typically an old kernel without nft_fib) while the main
// ruleset — traffic counters and inbound whitelists — was applied
// successfully. sync returns it so the
// failure stays named and per-rule, but it is not a ruleset failure:
// port hopping is a reachability nicety outside the security posture,
// so gating callers must not block a kernel start on it or report it
// as a whitelist/guard sync failure (R2-F2-E02). Manager call sites
// split it off with isolateHopSync.
type hopRulesError struct{ msg string }

func (e *hopRulesError) Error() string { return e.msg }

// installStatus 报告最近一次自动安装的结果（状态上报面）：本进
// 程是否装过 nft、最近一次安装失败的原因（"" 表示无失败）。
func (n *nftCounters) installStatus() (autoInstalled bool, installErr string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.autoInstalled, n.installErr
}

// setInstallFailure records a failed auto-install. Short n.mu
// take, callable from the unlocked preflight.
func (n *nftCounters) setInstallFailure(err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.installErr = err.Error()
	n.autoInstalled = false
	n.lastErr = err.Error()
}

// setAutoInstalled records a successful auto-install. Short n.mu
// take, callable from the unlocked preflight.
func (n *nftCounters) setAutoInstalled() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.autoInstalled = true
	n.installErr = ""
}

// installNftIfNeeded runs the slow auto-install OUTSIDE n.mu
// (R18-D3-02): sync used to hold the state lock across the whole
// install (up to nftInstallTimeout), starving read() — the panel's
// traffic poll — and setMarkBase() for minutes. installMu
// serializes concurrent installs only; state updates go through
// short n.mu takes. The locked section re-checks availability, so
// an install that did not stick (or a binary that vanished in the
// window) fails closed for whitelists without reinstalling under
// the lock.
func (n *nftCounters) installNftIfNeeded(ports []Port) error {
	if nftAvailable() == nil || !portsHaveWhitelist(ports) {
		return nil
	}
	n.installMu.Lock()
	defer n.installMu.Unlock()
	if nftAvailable() == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), nftInstallTimeout)
	defer cancel()
	installed, ierr := nftEnsureInstalled(ctx)
	if ierr != nil {
		n.setInstallFailure(ierr)
		return ierr
	}
	if installed {
		n.setAutoInstalled()
		log.Printf("nftables 已自动安装（入站白名单需要）")
	}
	return nil
}

// hasAppliedState reports whether this agent has ever applied a table
// (the signature in nft.json survives restarts). The periodic verify
// loop only guards a table that exists by design — a fresh agent
// that never applied anything has nothing to rebuild.
func (n *nftCounters) hasAppliedState() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state.Signature != ""
}

// sync rebuilds the whole table atomically: traffic counters and
// inbound whitelists in one ruleset. Traffic counting alone is
// best-effort (a node without nftables just loses its counters), but
// a port carrying a source whitelist is a security control — with a
// whitelist in play, a missing nft binary or a failed ruleset load
// is a hard error so the caller can refuse to run the kernel
// unguarded.
//
// The auto-install runs in the unlocked preflight above
// (R18-D3-02): the state lock is taken only for the ruleset work,
// so a slow install never starves read().
func (n *nftCounters) sync(ports []Port, users []int64, blocked []int64) error {
	if err := n.installNftIfNeeded(ports); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := nftAvailable(); err != nil {
		if portsHaveWhitelist(ports) {
			// The preflight either already installed it or
			// failed and returned; reaching here with a
			// whitelist and no nft means the install did not
			// stick (or the binary vanished in the window):
			// fail closed without reinstalling under the lock.
			return fmt.Errorf("nftables 自动安装失败：安装后 nft 命令仍不可用，白名单端口拒绝无保护运行")
		}
		n.lastErr = "未安装 nftables，无法统计流量"
		return nil
	}
	// nft 在场：清除陈旧的安装失败记录（上次失败后已由他途装
	// 好，或本次无需安装）。
	n.installErr = ""
	rules := expandPorts(ports)
	users = sortedUsers(users)
	blocked = sortedUsers(blocked)
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

	script := nftScript(n.tableName(), rules, expandGuards(ports), hops, users, n.markBase, blocked)
	sig := hashString(script)
	if sig == n.state.Signature {
		// The signature only proves what this process last tried to
		// install. The table lives in the kernel, outside the agent:
		// it can be flushed or rewritten by other tools while guarded
		// kernels keep running, and an existence check would never
		// notice (A-03). Skip the re-apply only when the live content
		// still matches the fingerprint captured at the last apply;
		// anything else — missing table, unreadable table, tampered
		// content, or a state file predating fingerprints — falls
		// through to a full re-apply below.
		//
		// R3A-NEW-1/C NEW-1: the check runs even when the desired
		// ruleset is empty. The old early return for !hasContent
		// read a deleted table as "consistent", so sync() never
		// rebuilt it while read() kept failing on the missing
		// table; the (possibly empty) table must exist for read()
		// to succeed.
		if n.state.Content != "" {
			if fp, err := n.tableFingerprint(ctx); err == nil && fp == n.state.Content {
				n.lastErr = ""
				return nil
			}
		}
	}
	// P1-1 升级迁移：首次升级后同步随同一条 nft 脚本删掉本安装自己
	// 的旧 plain 表（见 needsTableMigration）。迁移行不进签名：
	// 下次同步渲染的脚本与本次相同、无迁移行，签名才能命中并走
	// 上面的 early return，否则每次同步都会重建表、计数归零。
	migrating := n.needsTableMigration()
	applyScript := script
	if migrating {
		applyScript = legacyPlainTableCleanupScript() + script
	}
	if _, err := nftRunner(ctx, applyScript, "-f", "-"); err != nil {
		n.lastErr = err.Error()
		return err
	}
	// Capture what the kernel actually holds now, so later syncs can
	// verify content instead of existence. A failed capture leaves
	// Content empty; the next sync then re-applies once and retries
	// the capture — degraded, never wrongly trusted.
	content, _ := n.tableFingerprint(ctx)
	// Migrated is true after any successful apply by this build:
	// either the migration just ran, or this install never owned a
	// plain table (fresh install, or a root whose table never
	// changed) — either way there is nothing left to migrate.
	n.state = nftState{Signature: sig, Epoch: newEpoch(), Content: content, Migrated: true}
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
			// Packets is the packet count of this counter. FIX-029
			// (AUDIT-HIST): the panel converts realm nft counters
			// from the IP layer to the payload layer, and that
			// conversion needs packets as well as bytes.
			Packets int64 `json:"packets"`
			Bytes   int64 `json:"bytes"`
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
	out, err := nftRunner(ctx, "", "-j", "list", "counters", "table", "inet", n.tableName())
	if err != nil {
		return nil, nil, "", err
	}
	counters, users, err := parseCounters(out, n.tableName())
	return counters, users, n.state.Epoch, err
}

func parseCounters(raw []byte, table string) ([]Counter, []UserCounter, error) {
	var parsed nftListOutput
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, nil, fmt.Errorf("解析 nft 计数失败: %v", err)
	}
	counters := make([]Counter, 0)
	users := make([]UserCounter, 0)
	for _, item := range parsed.Nftables {
		c := item.Counter
		if c == nil || c.Table != table {
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
		counters = append(counters, Counter{InboundID: id, Proto: parts[2], Dir: parts[0], Packets: c.Packets, Bytes: c.Bytes})
	}
	return counters, users, nil
}

func newEpoch() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
