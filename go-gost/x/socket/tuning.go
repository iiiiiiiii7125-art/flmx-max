package socket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Node network tuning (节点调优), second generation. The tuning model
// is ported from the tcp.vpsing.de tuning script (user-supplied): four
// independent faces, each applied and reported on its own, plus a full
// rollback to distribution defaults.
//
//	apply-ipv4    IPv4 preferred in /etc/gai.conf
//	apply-bbr     BBR + fq via /etc/sysctl.d/10-bbr.conf
//	apply-kernel  production sysctl set + limits + MSS clamp
//	apply-nic     NIC multi-queue steering (RPS + ring buffers)
//	rollback      remove every footprint, restore defaults
//
// The first-generation apply model is retired wholesale: BDP buffer
// sizing from panel-supplied bandwidth/RTT, the iperf3 automatic
// job, the vendored tcpfit deep-shaping engine and the deep shaper
// are gone, and the old command types (TuneApply / TuneStart /
// TuneCancel) answer with an explicit unsupported error. The line
// probe (TuneProbe, tuning_probe*.go) is NOT part of that model and
// is retained unchanged: it measures return-path latency / loss /
// jitter against a reference host, and its result vocabulary
// (tuneParams, below) survives with it.
//
// Deliberate deviations from the reference script, by design:
//   - net.ipv4.tcp_congestion_control_version is NOT written. It is
//     not a mainline sysctl; the script only gets away with it
//     because `sysctl --system` output is discarded. This agent
//     applies keys one by one and reports failures, so the key would
//     surface as a spurious apply failure on every kernel.
//   - Rollback also removes first-generation footprints (the old
//     99-flmx-tuning.conf, the tcp_bbr modules-load file and the
//     qdisc unit): leaving them would silently resurrect the retired
//     tuning at the next boot, which contradicts a rollback.
//   - Rollback restores the distribution state, not scripted
//     guesses: the first apply of any runtime-writing face
//     snapshots the complete pre-apply state first (TL-02) — every
//     sysctl any face writes, plus the NIC ring sizes — and
//     rollback writes exactly that back. (The reference script's
//     rollback writes its own factory defaults instead; that
//     approach silently clobbers values the operator had set
//     before tuning, so it is not followed here.)

const (
	tuneActionApplyIPv4   = "apply-ipv4"
	tuneActionApplyBBR    = "apply-bbr"
	tuneActionApplyKernel = "apply-kernel"
	tuneActionApplyNIC    = "apply-nic"
	tuneActionRollback    = "rollback"
)

// gaiPrecedenceLine is the IPv4-preference line, byte-identical to the
// reference script (two spaces before 100; status detection greps the
// same form).
const gaiPrecedenceLine = "precedence ::ffff:0:0/96  100"

// gaiDefaultTemplate is written when /etc/gai.conf does not exist at
// all, mirroring the reference script.
const gaiDefaultTemplate = `label ::1/128       0
label ::/0          1
label 2002::/16     2
label ::/96         3
label ::ffff:0:0/96 4
precedence  ::1/128       50
precedence  ::/0          40
precedence  2002::/16     30
precedence  ::/96         20
precedence  ::ffff:0:0/96 10
`

const tuneBBRConfBody = "net.core.default_qdisc = fq\n" +
	"net.ipv4.tcp_congestion_control = bbr\n"

const tuneLimitsConfBody = `* soft nofile 1048576
* hard nofile 1048576
* soft nproc 65535
* hard nproc 65535
`

// Host footprint paths of the new model, plus the first-generation
// files kept for compatibility cleanup (rollback / uninstall remove
// them too). Variables so tests can point them at a temporary tree.
var (
	tuneProcRoot       = "/proc/sys"
	tuneMeminfoPath    = "/proc/meminfo"
	tuneGaiConfPath    = "/etc/gai.conf"
	tuneGaiBackupPath  = "/etc/gai.conf.bak"
	tuneBBRConfPath    = "/etc/sysctl.d/10-bbr.conf"
	tuneKernelConfPath = "/etc/sysctl.d/99-network-performance.conf"
	tuneLimitsConfPath = "/etc/security/limits.d/99-network-performance.conf"
	tuneRPSUnitPath    = "/etc/systemd/system/flmx-tuning-rps.service"
	tuneRPSScriptPath  = "/usr/local/bin/flmx-tuning-rps.sh"
	tuneNetDir         = "/sys/class/net"
	// Pre-apply snapshot of the full tuning state (every sysctl any
	// face writes, plus the NIC ring sizes), written by the first
	// apply of any runtime-writing face and consumed by rollback
	// (TL-02). It lives with the limits footprint under
	// /etc/security: a generic state
	// directory (/var/lib/…) could be wiped by container/image
	// hygiene while the sysctl confs survive, which would strand the
	// node in the tuned state with no way back.
	tuneKernelSnapshotPath = "/etc/security/flmx-tuning-kernel-sysctls.json"

	// First-generation footprints (retired model): still cleaned up
	// so an upgraded node does not keep replaying them at boot.
	oldTuneConfPath      = "/etc/sysctl.d/99-flmx-tuning.conf"
	legacyTuneConfPath   = "/etc/sysctl.d/99-flvx-tuning.conf"
	oldTuneBBRModulePath = "/etc/modules-load.d/flmx-bbr.conf"
	oldTuneShapeUnitPath = "/etc/systemd/system/flmx-tuning-qdisc.service"
)

const (
	tuneRPSUnitName      = "flmx-tuning-rps.service"
	oldTuneShapeUnitName = "flmx-tuning-qdisc.service"
)

// Test seams: production values, replaced by unit tests.
var (
	tuneWrite    = writeSysctl
	tuneRead     = readSysctl
	tuneStat     = os.Stat
	tuneCommand  = runTuneCommand
	tuneLookPath = exec.LookPath
	tuneNumCPU   = runtime.NumCPU
)

// tuningOperation serializes tuning mutations (applies, rollback and
// the uninstall restore share it).
var tuningOperation sync.Mutex

func commandExists(name string) bool { _, err := tuneLookPath(name); return err == nil }

// runTuneCommand runs a tuning subprocess in its own process group
// (see tuning_process_unix.go) with a locale-pinned environment, and
// folds the output tail into the error on failure.
func runTuneCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	prepareTuneProcess(cmd)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "DEBIAN_FRONTEND=noninteractive")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return out.String(), ctx.Err()
		}
		return out.String(), fmt.Errorf("%s 执行失败: %v；%s", name, err, tuneOutputTail(out.String()))
	}
	return out.String(), nil
}

func tuneOutputTail(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	out := strings.Join(lines, "\n")
	if r := []rune(out); len(r) > 300 {
		out = "…" + string(r[len(r)-300:])
	}
	return out
}

func tuneShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// writeFileAtomic replaces path in one step, so a full disk or a crash
// never leaves a half-written file behind.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, mode)
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

// --- sysctl primitives -------------------------------------------------

func sysctlPath(key string) string {
	return filepath.Join(tuneProcRoot, strings.ReplaceAll(key, ".", "/"))
}

func readSysctl(key string) (string, bool) {
	b, err := os.ReadFile(sysctlPath(key))
	if err != nil {
		return "", false
	}
	return strings.Join(strings.Fields(string(b)), " "), true
}

func writeSysctl(key, value string) error {
	return os.WriteFile(sysctlPath(key), []byte(value+"\n"), 0o644)
}

// applySysctlSettings writes each key now and reports per-key
// failures as warnings: a key the running kernel does not have
// (ENOENT/EINVAL) is skipped, never fatal — the conf file carries
// the full set for kernels that do have it.
func applySysctlSettings(settings []tuneKV) []string {
	var warnings []string
	for _, s := range settings {
		if err := tuneWrite(s.key, s.value); err != nil {
			if os.IsNotExist(err) || errors.Is(err, syscall.EINVAL) {
				warnings = append(warnings, fmt.Sprintf("%s 不支持，已跳过", s.key))
			} else {
				warnings = append(warnings, fmt.Sprintf("%s 设置失败: %v", s.key, err))
			}
		}
	}
	return warnings
}

type tuneKV struct {
	key   string
	value string
}

// --- face ① IPv4 priority ----------------------------------------------

func gaiPrecedenceActive(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimRight(line, " \t\r") == gaiPrecedenceLine {
			return true
		}
	}
	return false
}

// applyIPv4Priority enables IPv4 preference in gai.conf. The file is
// backed up to gai.conf.bak before the first change; an already
// active face is a no-op (in particular the backup is never
// overwritten with the tuned content).
func applyIPv4Priority() error {
	content, err := os.ReadFile(tuneGaiConfPath)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	s := string(content)
	if gaiPrecedenceActive(s) {
		return nil
	}
	if exists {
		if err := os.MkdirAll(filepath.Dir(tuneGaiBackupPath), 0o755); err != nil {
			return err
		}
		if err := writeFileAtomic(tuneGaiBackupPath, content, 0o644); err != nil {
			return fmt.Errorf("备份 %s 失败: %w", tuneGaiConfPath, err)
		}
	} else {
		s = gaiDefaultTemplate
	}
	if strings.Contains(s, "#"+gaiPrecedenceLine) {
		s = strings.Replace(s, "#"+gaiPrecedenceLine, gaiPrecedenceLine, 1)
	} else {
		if s != "" && !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		s += gaiPrecedenceLine + "\n"
	}
	if err := os.MkdirAll(filepath.Dir(tuneGaiConfPath), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(tuneGaiConfPath, []byte(s), 0o644)
}

// restoreGaiConf undoes face ①: the backup is restored verbatim when
// present; otherwise the precedence line is commented back out.
func restoreGaiConf() []string {
	var warnings []string
	bak, err := os.ReadFile(tuneGaiBackupPath)
	if err == nil {
		if werr := writeFileAtomic(tuneGaiConfPath, bak, 0o644); werr != nil {
			warnings = append(warnings, fmt.Sprintf("还原 %s 失败: %v", tuneGaiConfPath, werr))
		} else if rerr := os.Remove(tuneGaiBackupPath); rerr != nil && !os.IsNotExist(rerr) {
			warnings = append(warnings, fmt.Sprintf("删除 %s 失败: %v", tuneGaiBackupPath, rerr))
		}
		return warnings
	}
	if err != nil && !os.IsNotExist(err) {
		warnings = append(warnings, fmt.Sprintf("读取 %s 失败: %v", tuneGaiBackupPath, err))
		return warnings
	}
	content, err := os.ReadFile(tuneGaiConfPath)
	if err != nil {
		if !os.IsNotExist(err) {
			warnings = append(warnings, fmt.Sprintf("读取 %s 失败: %v", tuneGaiConfPath, err))
		}
		return warnings
	}
	s := string(content)
	if !gaiPrecedenceActive(s) {
		return warnings
	}
	s = strings.Replace(s, gaiPrecedenceLine, "#"+gaiPrecedenceLine, 1)
	if werr := writeFileAtomic(tuneGaiConfPath, []byte(s), 0o644); werr != nil {
		warnings = append(warnings, fmt.Sprintf("回退 %s 失败: %v", tuneGaiConfPath, werr))
	}
	return warnings
}

// --- face ② BBR ---------------------------------------------------------

func applyBBR(ctx context.Context) ([]string, error) {
	// Snapshot the pre-apply state before this face's first write
	// (TL-02): apply-bbr rewrites the congestion control and the
	// default qdisc at runtime, and only a snapshot taken now —
	// not one a later apply-kernel happens to take of the tuned
	// values — lets rollback restore the operator's originals.
	warnings := captureTuningSnapshot(ctx)
	if err := os.MkdirAll(filepath.Dir(tuneBBRConfPath), 0o755); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(tuneBBRConfPath, []byte(tuneBBRConfBody), 0o644); err != nil {
		return nil, fmt.Errorf("写入 %s 失败: %w", tuneBBRConfPath, err)
	}
	warnings = append(warnings, applySysctlSettings([]tuneKV{
		{"net.core.default_qdisc", "fq"},
		{"net.ipv4.tcp_congestion_control", "bbr"},
	})...)
	return warnings, nil
}

// --- face ③ kernel tuning ------------------------------------------------

// readMemTotalKB returns MemTotal in KiB from /proc/meminfo.
func readMemTotalKB() (int64, error) {
	b, err := os.ReadFile(tuneMeminfoPath)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
			return strconv.ParseInt(f[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("%s 中找不到 MemTotal", tuneMeminfoPath)
}

// tuneBufferBytes is the reference script's dynamic buffer size:
// 5% of total memory.
func tuneBufferBytes(memTotalKB int64) int64 {
	return memTotalKB * 5 / 100 * 1024
}

// kernelTuneSettings is the face-③ key set in the reference script's
// order. tcp_congestion_control_version is intentionally absent (see
// the package comment).
func kernelTuneSettings(bufBytes int64) []tuneKV {
	buf := strconv.FormatInt(bufBytes, 10)
	return []tuneKV{
		{"net.core.default_qdisc", "fq"},
		{"net.ipv4.tcp_congestion_control", "bbr"},
		{"net.core.somaxconn", "65535"},
		{"net.core.netdev_max_backlog", "65535"},
		{"net.ipv4.tcp_max_syn_backlog", "16384"},
		{"net.ipv4.ip_local_port_range", "1024 65535"},
		{"net.core.rmem_max", buf},
		{"net.core.wmem_max", buf},
		{"net.ipv4.tcp_rmem", "4096 87380 " + buf},
		{"net.ipv4.tcp_wmem", "4096 65536 " + buf},
		{"net.core.rmem_default", "2097152"},
		{"net.core.wmem_default", "2097152"},
		{"net.ipv4.tcp_notsent_lowat", "16384"},
		{"net.ipv4.tcp_mtu_probing", "1"},
		{"net.ipv4.udp_rmem_min", "16384"},
		{"net.ipv4.udp_wmem_min", "16384"},
		{"net.ipv4.tcp_ecn", "1"},
		{"net.ipv4.tcp_max_orphans", "32768"},
		{"net.ipv4.tcp_slow_start_after_idle", "0"},
		{"net.ipv4.tcp_tw_reuse", "1"},
		{"net.ipv4.tcp_fin_timeout", "15"},
		{"net.ipv4.tcp_retries2", "8"},
		{"net.ipv4.tcp_fastopen", "3"},
	}
}

func sysctlConfBody(settings []tuneKV) string {
	var b strings.Builder
	b.WriteString("# Written by the FLMX panel (节点调优); remove via the panel to restore.\n")
	for _, s := range settings {
		b.WriteString(s.key + " = " + s.value + "\n")
	}
	return b.String()
}

// tuneMSSRuleMatch is the rule-spec part of the MSS clamp rule
// (everything after the chain name). tuneMSSRuleArgs is the single
// construction point for every iptables invocation on this rule:
// the action verb (-A/-D/-C) always comes immediately after
// "-t mangle" and before the chain name. TL-01: the rule spec used
// to lead the argv (chain first), so prepending the verb produced
// `iptables -A -t mangle …`, which iptables rejects with exit 2.
var tuneMSSRuleMatch = []string{"-p", "tcp",
	"--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu"}

func tuneMSSRuleArgs(action string) []string {
	args := make([]string, 0, len(tuneMSSRuleMatch)+4)
	args = append(args, "-t", "mangle", action, "POSTROUTING")
	return append(args, tuneMSSRuleMatch...)
}

// tuneMSSClampAdd deploys the MSS clamp rule (delete-then-add, so a
// repeat apply never stacks duplicates). It returns a note when the
// step was skipped or failed; "" on success.
func tuneMSSClampAdd(ctx context.Context) string {
	if _, err := tuneLookPath("iptables"); err != nil {
		return "iptables 不存在，已跳过 MSS 钳制"
	}
	_, _ = tuneCommand(ctx, "iptables", tuneMSSRuleArgs("-D")...)
	if _, err := tuneCommand(ctx, "iptables", tuneMSSRuleArgs("-A")...); err != nil {
		return fmt.Sprintf("MSS 钳制规则部署失败: %v", err)
	}
	return ""
}

// tuneMSSClampRemove deletes the MSS clamp rule if iptables exists.
func tuneMSSClampRemove(ctx context.Context) {
	if _, err := tuneLookPath("iptables"); err != nil {
		return
	}
	_, _ = tuneCommand(ctx, "iptables", tuneMSSRuleArgs("-D")...)
}

func applyKernelTuning(ctx context.Context) ([]string, error) {
	memKB, err := readMemTotalKB()
	if err != nil {
		return nil, fmt.Errorf("无法读取系统内存，已停止调优: %w", err)
	}
	settings := kernelTuneSettings(tuneBufferBytes(memKB))
	// Snapshot the pre-apply state before the first write of any
	// kind (TL-02): rollback restores it verbatim.
	warnings := captureTuningSnapshot(ctx)
	if err := os.MkdirAll(filepath.Dir(tuneKernelConfPath), 0o755); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(tuneKernelConfPath, []byte(sysctlConfBody(settings)), 0o644); err != nil {
		return nil, fmt.Errorf("写入 %s 失败: %w", tuneKernelConfPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(tuneLimitsConfPath), 0o755); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(tuneLimitsConfPath, []byte(tuneLimitsConfBody), 0o644); err != nil {
		return nil, fmt.Errorf("写入 %s 失败: %w", tuneLimitsConfPath, err)
	}
	warnings = append(warnings, applySysctlSettings(settings)...)
	if note := tuneMSSClampAdd(ctx); note != "" {
		warnings = append(warnings, note)
	}
	return warnings, nil
}

// --- face ④ NIC multi-queue ----------------------------------------------

// tuneNICExcludePattern is the reference script's interface filter:
// virtual and tunnel devices are never steered.
var tuneNICExcludePattern = regexp.MustCompile(`lo|docker|veth|br-|any|tung3|sit0|tun|wg`)

func listTuneInterfaces() ([]string, error) {
	entries, err := os.ReadDir(tuneNetDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if tuneNICExcludePattern.MatchString(e.Name()) {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// rpsCPUMask renders the all-cores mask the way the script's
// printf '%x' of (2^cpus - 1) does.
func rpsCPUMask(cpus int) string {
	if cpus <= 0 {
		return "1"
	}
	if cpus >= 64 {
		return "ffffffffffffffff"
	}
	return strconv.FormatUint((uint64(1)<<uint(cpus))-1, 16)
}

// ethtoolRingMax holds the "Pre-set maximums" RX/TX ring sizes from
// `ethtool -g`. The two maxima differ per driver (e.g. ens5: RX 2048 /
// TX 1024); one shared value makes `ethtool -G rx <v> tx <v>` fail
// wholesale when <v> exceeds the smaller maximum, so RX and TX are
// parsed and applied separately.
type ethtoolRingMax struct {
	RX int
	TX int
}

// parseEthtoolMaxRings extracts the "Pre-set maximums" RX and TX ring
// sizes from `ethtool -g` output. The second return is true when a
// usable RX maximum was found; TX is 0 when that line is absent or
// unparsable, and the caller falls back to the default for TX only.
func parseEthtoolMaxRings(out string) (ethtoolRingMax, bool) {
	var max ethtoolRingMax
	rxFound := false
	inMax := false
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Pre-set maximums") {
			inMax = true
			continue
		}
		if !inMax {
			continue
		}
		if strings.HasPrefix(trimmed, "Current hardware settings") {
			break
		}
		f := strings.Fields(trimmed)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "RX:":
			n, err := strconv.Atoi(f[1])
			if err != nil || n <= 0 {
				return ethtoolRingMax{}, false
			}
			max.RX, rxFound = n, true
		case "TX:":
			if n, err := strconv.Atoi(f[1]); err == nil && n > 0 {
				max.TX = n
			}
		}
	}
	return max, rxFound
}

// parseEthtoolMaxRing extracts the "Pre-set maximums" RX ring size
// from `ethtool -g` output.
func parseEthtoolMaxRing(out string) (int, bool) {
	max, ok := parseEthtoolMaxRings(out)
	return max.RX, ok
}

// tuneRingSnapshot is one interface's ring sizes as they were
// before face ④ pushed them to the preset maxima, recorded in the
// TL-02 snapshot so rollback can write them back. TX is 0 when the
// driver did not report a TX ring: that direction is then left
// alone at restore time.
type tuneRingSnapshot struct {
	RX int `json:"rx"`
	TX int `json:"tx,omitempty"`
}

// parseEthtoolCurrentRings extracts the "Current hardware settings"
// RX and TX ring sizes from `ethtool -g` output. The second return
// is true when a usable RX value was found; TX is 0 when that line
// is absent or unparsable.
func parseEthtoolCurrentRings(out string) (tuneRingSnapshot, bool) {
	var cur tuneRingSnapshot
	rxFound := false
	inCurrent := false
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Current hardware settings") {
			inCurrent = true
			continue
		}
		if !inCurrent {
			continue
		}
		f := strings.Fields(trimmed)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "RX:":
			n, err := strconv.Atoi(f[1])
			if err != nil || n <= 0 {
				return tuneRingSnapshot{}, false
			}
			cur.RX, rxFound = n, true
		case "TX:":
			if n, err := strconv.Atoi(f[1]); err == nil && n > 0 {
				cur.TX = n
			}
		}
	}
	return cur, rxFound
}

// ethtoolRingRestoreArgs builds the `ethtool -G` arguments that put
// one interface's rings back to the snapshot values. A direction
// the snapshot does not record is not named, so the driver keeps
// its current value for it.
func ethtoolRingRestoreArgs(iface string, ring tuneRingSnapshot) []string {
	args := []string{"-G", iface, "rx", strconv.Itoa(ring.RX)}
	if ring.TX > 0 {
		args = append(args, "tx", strconv.Itoa(ring.TX))
	}
	return args
}

// ethtoolRingArgs builds the `ethtool -G` arguments that push both
// rings to their own preset maxima. A non-positive maximum falls
// back to 1024 for that direction only, never borrowing the other
// direction's value.
func ethtoolRingArgs(iface string, max ethtoolRingMax) []string {
	rx, tx := max.RX, max.TX
	if rx <= 0 {
		rx = 1024
	}
	if tx <= 0 {
		tx = 1024
	}
	return []string{"-G", iface, "rx", strconv.Itoa(rx), "tx", strconv.Itoa(tx)}
}

func tuneWriteSysFile(path, value string) error {
	return os.WriteFile(path, []byte(value+"\n"), 0o644)
}

// applyNICSteering pushes the ring buffers to their preset maximum
// and spreads receive processing over all cores for one interface.
// It returns warnings for every ethtool step that failed, so a
// rejected `ethtool -G` (e.g. a TX value above the TX maximum) is
// surfaced in the TuneStatus reply instead of silently reporting
// success with the rings untouched.
func applyNICSteering(ctx context.Context, iface, mask string) []string {
	var warnings []string
	if _, err := tuneLookPath("ethtool"); err == nil {
		max := ethtoolRingMax{RX: 1024, TX: 1024}
		if out, err := tuneCommand(ctx, "ethtool", "-g", iface); err == nil {
			if parsed, ok := parseEthtoolMaxRings(out); ok {
				max.RX = parsed.RX
				if parsed.TX > 0 {
					max.TX = parsed.TX
				}
			}
		} else {
			warnings = append(warnings, fmt.Sprintf("网卡 %s 读取环形缓冲上限失败: %v", iface, err))
		}
		if _, err := tuneCommand(ctx, "ethtool", ethtoolRingArgs(iface, max)...); err != nil {
			warnings = append(warnings, fmt.Sprintf("网卡 %s 设置环形缓冲失败（rx %d tx %d）: %v", iface, max.RX, max.TX, err))
		}
	}
	queues := filepath.Join(tuneNetDir, iface, "queues")
	if files, _ := filepath.Glob(filepath.Join(queues, "rx-*", "rps_cpus")); files != nil {
		for _, f := range files {
			_ = tuneWriteSysFile(f, mask)
		}
	}
	if files, _ := filepath.Glob(filepath.Join(queues, "rx-*", "rps_flow_cnt")); files != nil {
		for _, f := range files {
			_ = tuneWriteSysFile(f, "4096")
		}
	}
	return warnings
}

// tuneRPSScript renders the boot-persistence script for face ④: the
// same steering reapplied by the oneshot unit at every boot. Ring
// sizes are runtime-only in the reference script too, so they are
// not persisted here either.
func tuneRPSScript(ifaces []string, mask string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# Written by the FLMX panel (节点调优 · 网卡多队列); removed on tune rollback/uninstall.\n")
	fmt.Fprintf(&b, "for eth in %s; do\n", strings.Join(ifaces, " "))
	b.WriteString("  for f in /sys/class/net/$eth/queues/rx-*/rps_cpus; do [ -f \"$f\" ] && echo " + tuneShellQuote(mask) + " > \"$f\"; done\n")
	b.WriteString("  for f in /sys/class/net/$eth/queues/rx-*/rps_flow_cnt; do [ -f \"$f\" ] && echo \"4096\" > \"$f\"; done\n")
	b.WriteString("done\n")
	b.WriteString("sysctl -w net.core.rps_sock_flow_entries=32768 >/dev/null 2>&1 || true\n")
	return b.String()
}

func tuneRPSUnitBody() string {
	return "[Unit]\n" +
		"Description=FLMX tuning: NIC receive packet steering (RPS)\n" +
		"After=network.target\n\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"ExecStart=/bin/sh " + tuneRPSScriptPath + "\n" +
		"RemainAfterExit=yes\n\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"
}

// persistNICSteering installs the oneshot unit that reapplies face ④
// at boot. Without systemd (container form) the steering still
// applies now but does not survive a reboot; that is reported.
func persistNICSteering(ctx context.Context, ifaces []string, mask string) []string {
	if InContainer() || !commandExists("systemctl") {
		return []string{"无 systemd，网卡多队列未持久化（重启后失效）"}
	}
	var warnings []string
	if err := os.MkdirAll(filepath.Dir(tuneRPSScriptPath), 0o755); err != nil {
		return append(warnings, fmt.Sprintf("写入 RPS 脚本失败: %v", err))
	}
	if err := writeFileAtomic(tuneRPSScriptPath, []byte(tuneRPSScript(ifaces, mask)), 0o755); err != nil {
		return append(warnings, fmt.Sprintf("写入 RPS 脚本失败: %v", err))
	}
	if err := os.MkdirAll(filepath.Dir(tuneRPSUnitPath), 0o755); err != nil {
		return append(warnings, fmt.Sprintf("写入 RPS 服务失败: %v", err))
	}
	if err := writeFileAtomic(tuneRPSUnitPath, []byte(tuneRPSUnitBody()), 0o644); err != nil {
		return append(warnings, fmt.Sprintf("写入 RPS 服务失败: %v", err))
	}
	if _, err := tuneCommand(ctx, "systemctl", "daemon-reload"); err != nil {
		warnings = append(warnings, fmt.Sprintf("systemctl daemon-reload 失败: %v", err))
	}
	if _, err := tuneCommand(ctx, "systemctl", "enable", tuneRPSUnitName); err != nil {
		warnings = append(warnings, fmt.Sprintf("启用 RPS 服务失败: %v", err))
	}
	return warnings
}

func applyNICTuning(ctx context.Context) ([]string, error) {
	// Snapshot the pre-apply state before this face's first write
	// (TL-02): the ring sizes pushed below are runtime-only, so
	// the snapshot is the only record of the originals rollback
	// can restore.
	warnings := captureTuningSnapshot(ctx)
	ifaces, err := listTuneInterfaces()
	if err != nil {
		return nil, fmt.Errorf("枚举网卡失败: %w", err)
	}
	mask := rpsCPUMask(tuneNumCPU())
	for _, iface := range ifaces {
		warnings = append(warnings, applyNICSteering(ctx, iface, mask)...)
	}
	warnings = append(warnings, applySysctlSettings([]tuneKV{{"net.core.rps_sock_flow_entries", "32768"}})...)
	warnings = append(warnings, persistNICSteering(ctx, ifaces, mask)...)
	return warnings, nil
}

// --- pre-apply snapshot (TL-02) --------------------------------------------

// tuneKernelSnapshot is the on-disk record of the tuning state as
// it was immediately before the first apply of any runtime-writing
// face overwrote it: every sysctl value any face writes, plus the
// NIC ring sizes face ④ pushes to their maxima. Rollback restores
// exactly this state; nothing else can reconstruct it (the
// distribution defaults are not knowable once overwritten, and
// `sysctl --system` does not reset keys no remaining conf covers).
type tuneKernelSnapshot struct {
	Values map[string]string `json:"values"`
	// Absent lists keys that did not exist at capture time. They
	// cannot be un-created at rollback; they are reported instead.
	Absent []string `json:"absent,omitempty"`
	// Rings holds the pre-apply ring sizes per interface. The
	// field postdates the first snapshot format: snapshots written
	// by older agents have no Rings, and rollback then leaves the
	// rings as they are — exactly the pre-recording behavior,
	// since there is nothing truthful to write back.
	Rings map[string]tuneRingSnapshot `json:"rings,omitempty"`
}

// tuningSnapshotSysctls is the union of every sysctl key any face
// writes at runtime: the face-③ set (which already includes the
// two keys face ② writes, net.core.default_qdisc and
// net.ipv4.tcp_congestion_control) plus face ④'s
// net.core.rps_sock_flow_entries. The snapshot covers this union
// no matter which face applies first — per-face lists would let
// the first face's snapshot freeze out a later face's originals.
// Derived from kernelTuneSettings, never a hand list.
func tuningSnapshotSysctls() []tuneKV {
	return append(kernelTuneSettings(0), tuneKV{"net.core.rps_sock_flow_entries", ""})
}

// captureTuningSnapshot records the complete pre-apply state:
// the current values of every key in tuningSnapshotSysctls, plus
// the current ring sizes of every interface face ④ would steer.
// It runs before the first write of whichever face applies first,
// so no face can pollute another face's originals (an apply-bbr
// landing before apply-kernel must not let the kernel snapshot
// record the tuned fq/bbr as the "originals"). The first snapshot
// wins, mirroring the gai.conf backup: a repeat apply must not
// overwrite the true originals with tuned values. A sysctl key
// that cannot be read is fatal rather than recorded as a guess:
// an incomplete snapshot would silently strand that key at
// rollback. Ring capture is best-effort per interface instead: a
// failure names the interface whose rings rollback cannot
// restore, but never blocks the sysctl record.
func captureTuningSnapshot(ctx context.Context) []string {
	if _, err := os.Stat(tuneKernelSnapshotPath); err == nil {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return []string{fmt.Sprintf("检查调优前快照失败，本次未留快照: %v", err)}
	}
	settings := tuningSnapshotSysctls()
	snap := tuneKernelSnapshot{Values: map[string]string{}}
	for _, s := range settings {
		v, ok := tuneRead(s.key)
		if !ok {
			// A stat error other than "not there" means the key
			// exists but cannot be read — abort rather than
			// guess. A missing node is how a key this kernel
			// does not have surfaces through /proc/sys; it lands
			// in Absent instead. (The write side's ENOENT/EINVAL
			// tolerance in applySysctlSettings is the same
			// judgment for the same kernel variance.)
			if _, serr := tuneStat(sysctlPath(s.key)); serr != nil && !os.IsNotExist(serr) {
				return []string{fmt.Sprintf("读取 %s 现值失败，已中止留存调优前快照（回滚将无法完整回退）: %v", s.key, serr)}
			}
			snap.Absent = append(snap.Absent, s.key)
			continue
		}
		snap.Values[s.key] = v
	}
	rings, warnings := captureNICRings(ctx)
	snap.Rings = rings
	raw, err := json.Marshal(snap)
	if err != nil {
		return append(warnings, fmt.Sprintf("序列化调优前快照失败: %v", err))
	}
	if err := os.MkdirAll(filepath.Dir(tuneKernelSnapshotPath), 0o755); err != nil {
		return append(warnings, fmt.Sprintf("保存调优前快照失败（回滚将无法完整回退）: %v", err))
	}
	if err := writeFileAtomic(tuneKernelSnapshotPath, raw, 0o644); err != nil {
		return append(warnings, fmt.Sprintf("保存调优前快照失败（回滚将无法完整回退）: %v", err))
	}
	return warnings
}

// captureNICRings records the current ring sizes of every
// interface face ④ would steer. Without ethtool, face ④ writes no
// ring values either, so there is nothing to record; an interface
// enumeration failure is likewise left to the apply path to
// report. A per-interface read or parse failure is named as a
// warning and the interface is left out of the snapshot: rollback
// then leaves its rings alone, with this warning as the record of
// why.
func captureNICRings(ctx context.Context) (map[string]tuneRingSnapshot, []string) {
	if _, err := tuneLookPath("ethtool"); err != nil {
		return nil, nil
	}
	ifaces, err := listTuneInterfaces()
	if err != nil {
		return nil, nil
	}
	var warnings []string
	rings := make(map[string]tuneRingSnapshot, len(ifaces))
	for _, iface := range ifaces {
		out, err := tuneCommand(ctx, "ethtool", "-g", iface)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("读取网卡 %s 环形缓冲现值失败，回滚将无法恢复其环形缓冲: %v", iface, err))
			continue
		}
		cur, ok := parseEthtoolCurrentRings(out)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("解析网卡 %s 环形缓冲现值失败，回滚将无法恢复其环形缓冲", iface))
			continue
		}
		rings[iface] = cur
	}
	if len(rings) == 0 {
		return nil, warnings
	}
	return rings, warnings
}

// restoreKernelSysctls is the rollback counterpart for the sysctl
// part of the snapshot. With a snapshot, every recorded value is
// written back and every absent key is reported (it did not exist
// pre-apply and cannot be un-created). Without one — the kernel
// conf exists but no snapshot, e.g. the face was applied by an
// older agent — the historical three headline keys still go back
// to their scripted defaults and the remaining face-③ keys are
// named as unrestorable (fail-safe: a loud warning, never
// silence). A snapshot that exists but does not parse is treated
// the same way, with the parse failure named. The scripted
// defaults are written ONLY on those no-usable-snapshot paths:
// with a snapshot, the recorded values are the whole restore set,
// net.core.rps_sock_flow_entries included; a legacy snapshot that
// predates recording that key keeps the historical reset to 0.
// The snapshot file is consumed here, in every case. The parsed
// snapshot is returned (nil when none was usable) so the caller
// can restore the ring sizes from the same record.
func restoreKernelSysctls(kernelConfPresent bool) ([]string, *tuneKernelSnapshot) {
	defer func() { _ = os.Remove(tuneKernelSnapshotPath) }()
	// The memory-dependent buffer keys vary per machine; the set
	// itself does not, so any buffer size yields the full key set.
	settings := kernelTuneSettings(0)
	writeDefaults := func() {
		_ = tuneWrite("net.ipv4.tcp_congestion_control", "cubic")
		_ = tuneWrite("net.core.default_qdisc", "pfifo_fast")
		_ = tuneWrite("net.core.rps_sock_flow_entries", "0")
	}
	restoredByDefault := map[string]bool{
		"net.ipv4.tcp_congestion_control": true,
		"net.core.default_qdisc":          true,
		"net.core.rps_sock_flow_entries":  true,
	}
	unrestored := func() []string {
		var keys []string
		for _, s := range settings {
			if !restoredByDefault[s.key] {
				keys = append(keys, s.key)
			}
		}
		return keys
	}
	raw, err := os.ReadFile(tuneKernelSnapshotPath)
	if err != nil {
		if !os.IsNotExist(err) {
			writeDefaults()
			return []string{fmt.Sprintf("读取调优前快照失败: %v；以下 sysctl 键未能回退: %s", err, strings.Join(unrestored(), ", "))}, nil
		}
		writeDefaults()
		if kernelConfPresent {
			return []string{fmt.Sprintf("调优前快照缺失，以下 sysctl 键未能回退（仍为调优值）: %s", strings.Join(unrestored(), ", "))}, nil
		}
		return nil, nil
	}
	var snap tuneKernelSnapshot
	if jerr := json.Unmarshal(raw, &snap); jerr != nil || snap.Values == nil {
		writeDefaults()
		return []string{fmt.Sprintf("调优前快照损坏（%v），以下 sysctl 键未能回退（仍为调优值）: %s", jerr, strings.Join(unrestored(), ", "))}, nil
	}
	absent := make(map[string]bool, len(snap.Absent))
	for _, k := range snap.Absent {
		absent[k] = true
	}
	var warnings []string
	for _, s := range tuningSnapshotSysctls() {
		v, ok := snap.Values[s.key]
		if !ok {
			continue
		}
		if err := tuneWrite(s.key, v); err != nil {
			warnings = append(warnings, fmt.Sprintf("回退 %s 失败: %v", s.key, err))
		}
	}
	// A legacy snapshot predates recording the face-④ counter:
	// keep the historical reset to 0 for it. A counter recorded
	// as absent did not exist pre-apply and cannot be written.
	if _, ok := snap.Values["net.core.rps_sock_flow_entries"]; !ok && !absent["net.core.rps_sock_flow_entries"] {
		_ = tuneWrite("net.core.rps_sock_flow_entries", "0")
	}
	if len(snap.Absent) > 0 {
		warnings = append(warnings, fmt.Sprintf("以下 sysctl 键在调优前不存在、无法回退删除: %s", strings.Join(snap.Absent, ", ")))
	}
	return warnings, &snap
}

// restoreNICRings writes the snapshot's per-interface ring sizes
// back — face ④'s runtime-only write set, which no conf file
// carries. A snapshot without ring data (written by an older
// agent, or captured when ethtool was unavailable) leaves the
// rings as they are: there is nothing truthful to write back.
func restoreNICRings(ctx context.Context, snap *tuneKernelSnapshot) []string {
	if snap == nil || len(snap.Rings) == 0 {
		return nil
	}
	if _, err := tuneLookPath("ethtool"); err != nil {
		return []string{"ethtool 不存在，调优前网卡环形缓冲值未能恢复"}
	}
	ifaces := make([]string, 0, len(snap.Rings))
	for iface := range snap.Rings {
		ifaces = append(ifaces, iface)
	}
	sort.Strings(ifaces)
	var warnings []string
	for _, iface := range ifaces {
		if _, err := tuneCommand(ctx, "ethtool", ethtoolRingRestoreArgs(iface, snap.Rings[iface])...); err != nil {
			warnings = append(warnings, fmt.Sprintf("恢复网卡 %s 环形缓冲失败: %v", iface, err))
		}
	}
	return warnings
}

// --- rollback --------------------------------------------------------------

// removeTuneFile deletes one footprint file, recording real failures
// (a missing file is the desired end state, not a failure).
func removeTuneFile(path string, warnings *[]string) bool {
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return false
		}
		*warnings = append(*warnings, fmt.Sprintf("删除 %s 失败: %v", path, err))
		return false
	}
	return true
}

// disableTuneUnit disables and removes one systemd unit file.
func disableTuneUnit(ctx context.Context, unitPath, unitName string, warnings *[]string) {
	if _, err := os.Stat(unitPath); err != nil {
		return
	}
	if !InContainer() && commandExists("systemctl") {
		_, _ = tuneCommand(ctx, "systemctl", "disable", "--now", unitName)
	}
	removeTuneFile(unitPath, warnings)
}

// rollbackTuning removes every tuning footprint — the new model's
// four faces and the first-generation leftovers — and puts the
// in-memory values back to distribution defaults. Best effort: one
// failed item never blocks the others; failures come back as
// warnings. Shared by the Tune rollback action and the agent
// self-uninstall restore.
func rollbackTuning(ctx context.Context) []string {
	var warnings []string
	// Whether the face-③ conf is on disk before the footprint files
	// go: restoreKernelSysctls needs it to tell "nothing was ever
	// applied" apart from "applied without a snapshot" (TL-02).
	_, statErr := os.Stat(tuneKernelConfPath)
	kernelConfPresent := statErr == nil
	// 1. Files: new model + first-generation leftovers. The
	// snapshot is consumed by restoreKernelSysctls below, not
	// here: it must survive until step 3 has read it.
	removeTuneFile(tuneKernelConfPath, &warnings)
	removeTuneFile(tuneLimitsConfPath, &warnings)
	removeTuneFile(tuneBBRConfPath, &warnings)
	removeTuneFile(oldTuneConfPath, &warnings)
	removeTuneFile(oldTuneBBRModulePath, &warnings)
	// The legacy (pre-rename) conf is removed here as well: rollback
	// is an explicit act on this node by its owner, unlike the
	// uninstall cleanup, which keeps its ownership guard.
	removeTuneFile(legacyTuneConfPath, &warnings)
	// Units first disable, then delete; one daemon-reload at the end.
	unitTouched := false
	for _, u := range []struct{ path, name string }{
		{tuneRPSUnitPath, tuneRPSUnitName},
		{oldTuneShapeUnitPath, oldTuneShapeUnitName},
	} {
		if _, err := os.Stat(u.path); err == nil {
			unitTouched = true
		}
		disableTuneUnit(ctx, u.path, u.name, &warnings)
	}
	removeTuneFile(tuneRPSScriptPath, &warnings)
	if unitTouched && !InContainer() && commandExists("systemctl") {
		_, _ = tuneCommand(ctx, "systemctl", "daemon-reload")
	}
	// 2. gai.conf back to its pre-tune content.
	warnings = append(warnings, restoreGaiConf()...)
	// 3. In-memory sysctl values back to the pre-apply snapshot
	// (TL-02); without one, the scripted headline defaults plus a
	// fail-safe warning for the keys that cannot be restored.
	sysctlWarnings, snap := restoreKernelSysctls(kernelConfPresent)
	warnings = append(warnings, sysctlWarnings...)
	// 4. NIC ring sizes back to the snapshot's per-interface
	// originals: face ④'s runtime-only write set, restorable only
	// from the same record step 3 just consumed.
	warnings = append(warnings, restoreNICRings(ctx, snap)...)
	// 5. MSS clamp rule.
	tuneMSSClampRemove(ctx)
	// 6. Per-queue steering off: rps_cpus and rps_flow_cnt are a
	// pair — apply sets both, rollback clears both (TL-03).
	if ifaces, err := listTuneInterfaces(); err == nil {
		for _, iface := range ifaces {
			for _, name := range []string{"rps_cpus", "rps_flow_cnt"} {
				files, _ := filepath.Glob(filepath.Join(tuneNetDir, iface, "queues", "rx-*", name))
				for _, f := range files {
					_ = tuneWriteSysFile(f, "0")
				}
			}
		}
	}
	// 7. Reload whatever conf files remain (other tools' settings).
	if commandExists("sysctl") {
		_, _ = tuneCommand(ctx, "sysctl", "--system")
	}
	return warnings
}

// --- status ------------------------------------------------------------------

type tuneNoFileStatus struct {
	Soft       uint64 `json:"soft"`
	Hard       uint64 `json:"hard"`
	LimitsFile bool   `json:"limitsFile"`
}

// tuneStatus is the TuneStatus reply: the four faces as booleans
// (detected exactly the way the reference script's menu detects
// them), the live headline values, and the agent process's own
// nofile summary.
type tuneStatus struct {
	IPv4Priority       bool             `json:"ipv4Priority"`
	BBR                bool             `json:"bbr"`
	Kernel             bool             `json:"kernel"`
	NIC                bool             `json:"nic"`
	CongestionControl  string           `json:"congestionControl"`
	DefaultQdisc       string           `json:"defaultQdisc"`
	RPSSockFlowEntries int64            `json:"rpsSockFlowEntries"`
	NoFile             tuneNoFileStatus `json:"nofile"`
	Warnings           []string         `json:"warnings,omitempty"`
}

func readTuneStatus() tuneStatus {
	var st tuneStatus
	if b, err := os.ReadFile(tuneGaiConfPath); err == nil {
		st.IPv4Priority = gaiPrecedenceActive(string(b))
	}
	st.CongestionControl, _ = readSysctl("net.ipv4.tcp_congestion_control")
	st.BBR = st.CongestionControl == "bbr"
	st.DefaultQdisc, _ = readSysctl("net.core.default_qdisc")
	if v, ok := readSysctl("net.core.rps_sock_flow_entries"); ok {
		st.RPSSockFlowEntries, _ = strconv.ParseInt(v, 10, 64)
	}
	st.NIC = st.RPSSockFlowEntries == 32768
	if _, err := os.Stat(tuneKernelConfPath); err == nil {
		st.Kernel = true
	}
	soft, hard := currentNoFileLimit()
	st.NoFile.Soft, st.NoFile.Hard = soft, hard
	if _, err := os.Stat(tuneLimitsConfPath); err == nil {
		st.NoFile.LimitsFile = true
	}
	return st
}

// --- line-probe vocabulary (retained with the probe) -------------------------

// tuneParams is the suggested-line vocabulary of the retained line
// probe (tuning_probe.go): the probe measures a line and reports the
// suggestion in bandwidth/RTT terms. The BDP apply path that used to
// consume these values is retired; the type and its default stay
// because the probe's result contract with the panel still uses
// them.
type tuneParams struct {
	BandwidthMbps int `json:"bandwidthMbps"`
	RTTMs         int `json:"rttMs"`
}

// defaultTuneParams is the probe's fallback suggestion when nothing
// could be measured.
var defaultTuneParams = tuneParams{BandwidthMbps: 1000, RTTMs: 150}

// --- command handling -----------------------------------------------------------

// handleTuneCommand runs the tuning commands: TuneStatus (read-only),
// Tune with an action from the new model's action set, and TuneProbe
// (the retained line probe, read-only). The first-generation command
// types are retired but still answered — with an explicit unsupported
// error, so an old panel gets a clear message instead of an "unknown
// command" fallback.
func handleTuneCommand(cmdType string, data interface{}) (interface{}, error) {
	return handleTuneCommandContext(context.Background(), cmdType, data)
}

// handleTuneCommandContext is handleTuneCommand under a per-command
// budget (D1-06): an already-spent budget refuses the command instead
// of executing it for a caller that has given up.
func handleTuneCommandContext(ctx context.Context, cmdType string, data interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("命令执行预算已耗尽，拒绝执行: %w", err)
	}
	switch cmdType {
	case "TuneStatus":
		return readTuneStatus(), nil
	case "TuneProbe":
		// The retained line probe: read-only, takes no line input,
		// dispatched before the Tune action handling (see
		// probeTuneContext in tuning_probe.go).
		var preq tuneProbeRequest
		if data != nil {
			raw, err := json.Marshal(data)
			if err != nil {
				return nil, fmt.Errorf("序列化数据失败: %v", err)
			}
			if err := json.Unmarshal(raw, &preq); err != nil {
				return nil, fmt.Errorf("解析探测参数失败: %v", err)
			}
		}
		return probeTuneContext(ctx, preq)
	case "Tune":
		var req struct {
			Action string `json:"action"`
		}
		if data != nil {
			raw, err := json.Marshal(data)
			if err != nil {
				return nil, fmt.Errorf("序列化数据失败: %v", err)
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, fmt.Errorf("解析调优参数失败: %v", err)
			}
		}
		return runTuneAction(ctx, req.Action)
	case "TuneApply", "TuneStart", "TuneCancel":
		return nil, fmt.Errorf("旧调优命令 %s 已退役，不再支持：请使用新版调优（Tune 命令）", cmdType)
	}
	return nil, fmt.Errorf("未知命令类型: %s", cmdType)
}

func runTuneAction(ctx context.Context, action string) (interface{}, error) {
	switch action {
	case tuneActionApplyIPv4, tuneActionApplyBBR, tuneActionApplyKernel, tuneActionApplyNIC, tuneActionRollback:
	default:
		return nil, fmt.Errorf("未知调优动作: %s（支持 apply-ipv4 / apply-bbr / apply-kernel / apply-nic / rollback）", action)
	}
	if !tuningOperation.TryLock() {
		return nil, fmt.Errorf("已有调优操作正在进行")
	}
	defer tuningOperation.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var warnings []string
	var err error
	switch action {
	case tuneActionApplyIPv4:
		err = applyIPv4Priority()
	case tuneActionApplyBBR:
		warnings, err = applyBBR(cctx)
	case tuneActionApplyKernel:
		warnings, err = applyKernelTuning(cctx)
	case tuneActionApplyNIC:
		warnings, err = applyNICTuning(cctx)
	case tuneActionRollback:
		warnings = rollbackTuning(cctx)
	}
	if err != nil {
		return nil, err
	}
	st := readTuneStatus()
	st.Warnings = warnings
	return st, nil
}
