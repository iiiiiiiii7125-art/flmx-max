package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-gost/x/internal/connlog"
)

// defaultBaseDir is the script form's kernel root under the current
// install layout. Legacy (pre-rename) installs keep their root via
// the working-directory recognition in ResolveBaseDir instead of
// this constant.
const defaultBaseDir = "/etc/flmx_agent/kernels"

// Install-layout directory names recognized by ResolveBaseDir's
// script-form branch: the current generation and the legacy
// pre-rename one (kept recognized until every legacy install has
// been migrated by install.sh). x/kernel cannot import x/socket,
// so the pair is spelled here too and both sides must agree.
const (
	scriptLayoutDirName       = "flmx_agent"
	legacyScriptLayoutDirName = "flux_agent"
)

type instance struct {
	req     ApplyRequest
	binDir  string            // main binary directory
	bins    map[string]string // "" = main binary, else extra name -> path
	version string
	logs    *logRing
	procs   []*process
	err     string
	// goodHash 是最近一次被确认 Running 的配置 Hash（F-032）。回滚判定
	// 以它为准，不用下发瞬间的进程快照——旧进程正处崩溃退避间隙时
	// 快照为 false，旧实现会因此放弃回滚并把坏配置 persist 固化。
	goodHash string
	// connlog is the inbound connection log collector for this
	// instance (sing-box/xray only), created on first Apply and
	// kept across process relaunches so connection-id generations
	// stay scoped per process launch.
	connlog *connLogCollector
}

// Manager owns every kernel instance on this agent. Apply/Control/Uninstall
// are serialized by applyMu (downloads can take minutes); mu guards the maps
// so Status stays responsive meanwhile.
type Manager struct {
	baseDir string

	applyMu     sync.Mutex
	mu          sync.Mutex
	restoreOnce sync.Once
	instances   map[string]*instance
	nft         *nftCounters
}

var (
	defaultManager     *Manager
	defaultManagerOnce sync.Once
)

// Container-form detection for the kernel-root fallback in
// ResolveBaseDir. It mirrors socket.InContainer (/.dockerenv marker
// or a container runtime name in the cgroup path); x/kernel cannot
// import x/socket (socket imports kernel), so the probe lives here
// too and both sides must keep the same notion of "container form".
// F2-11: a bare "lxc" cgroup hit is not a container-form signal
// (LXC system containers run the script-form install); see
// socket/container.go for the rationale — both probes must stay
// in sync. The seams are variables so tests can pin every
// resolution branch.
var (
	kernelContainerMarkerPath = "/.dockerenv"
	kernelContainerCgroupPath = "/proc/self/cgroup"
	detectKernelContainer     = probeKernelContainer
	kernelWorkDir             = os.Getwd
)

func probeKernelContainer() bool {
	if _, err := os.Stat(kernelContainerMarkerPath); err == nil {
		return true
	}
	b, err := os.ReadFile(kernelContainerCgroupPath)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, "docker") || strings.Contains(line, "kubepods") || strings.Contains(line, "containerd") {
			return true
		}
	}
	return false
}

// ResolveBaseDir resolves the kernel state root:
//
//  1. FLMX_KERNEL_DIR, when set — explicit configuration always
//     wins. The container image pins FLMX_KERNEL_DIR to the data
//     volume for exactly this reason. (The legacy FLVX_KERNEL_DIR
//     fallback read retired in 2.0.0.)
//  2. In the container form, <working directory>/kernels: the
//     entrypoint pins the working directory to the mounted data
//     directory that also holds config.json, so kernel binaries,
//     state.json and geodata persist across container rebuilds
//     instead of dying with the container's writable layer.
//  3. In the script form, <working directory>/kernels when the
//     working directory is a recognized install layout: the unit's
//     WorkingDirectory is the install directory in both layout
//     generations, so current installs root under /etc/flmx_agent
//     and not-yet-migrated legacy installs keep their existing
//     /etc/flux_agent root (kernel state stays continuous across a
//     panel-driven in-place upgrade; install.sh's migration moves
//     the whole directory, root included).
//  4. defaultBaseDir — the current layout's root.
func ResolveBaseDir() string {
	dir := strings.TrimSpace(os.Getenv("FLMX_KERNEL_DIR"))
	if dir != "" {
		return dir
	}
	if detectKernelContainer() {
		if wd, err := kernelWorkDir(); err == nil && filepath.IsAbs(wd) {
			return filepath.Join(wd, "kernels")
		}
	} else if wd, err := kernelWorkDir(); err == nil && filepath.IsAbs(wd) {
		switch filepath.Base(wd) {
		case scriptLayoutDirName, legacyScriptLayoutDirName:
			return filepath.Join(wd, "kernels")
		}
	}
	return defaultBaseDir
}

// Default returns the process-wide manager rooted at ResolveBaseDir().
func Default() *Manager {
	defaultManagerOnce.Do(func() {
		defaultManager = NewManager(ResolveBaseDir())
	})
	return defaultManager
}

func NewManager(baseDir string) *Manager {
	return &Manager{
		baseDir:   baseDir,
		instances: make(map[string]*instance),
		nft:       newNftCounters(baseDir),
	}
}

func (m *Manager) statePath() string { return filepath.Join(m.baseDir, "state.json") }
func (m *Manager) runDir(t string) string {
	return filepath.Join(m.baseDir, t, "run")
}
func (m *Manager) binDir(t, version string) string {
	return filepath.Join(m.baseDir, t, "bin", version)
}

// Restore relaunches the kernels recorded by the last Apply so they come back
// after an agent restart even while the panel is unreachable.
func (m *Manager) Restore() {
	m.restoreOnce.Do(func() {
		m.restore()
		m.startGeoUpdater()
		m.startRuleSetUpdater()
	})
}

func (m *Manager) restore() {
	b, err := os.ReadFile(m.statePath())
	if err != nil {
		return
	}
	var reqs []ApplyRequest
	if err := json.Unmarshal(b, &reqs); err != nil {
		fmt.Printf("⚠️ 内核状态文件损坏: %v\n", err)
		return
	}
	for _, req := range reqs {
		if _, err := m.Apply(req); err != nil {
			fmt.Printf("⚠️ 恢复内核 %s 失败: %v\n", req.Type, err)
		}
	}
}

func (m *Manager) persist() {
	m.mu.Lock()
	reqs := make([]ApplyRequest, 0, len(m.instances))
	for _, inst := range m.instances {
		if inst.req.Type != "" {
			reqs = append(reqs, inst.req)
		}
	}
	m.mu.Unlock()
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].Type < reqs[j].Type })
	b, err := json.Marshal(reqs)
	if err != nil {
		return
	}
	if err := os.MkdirAll(m.baseDir, 0o700); err != nil {
		return
	}
	_ = writeFileAtomic(m.statePath(), b, 0o600)
}

// allPorts and allUserMarks derive the nft desired state. Only
// enabled instances contribute: a stopped or disabled kernel must
// leave none of its rules behind — its whitelist drop rules would
// otherwise outlive it indefinitely and silently discard traffic
// for whatever later binds the port, and its counters would keep
// attributing that traffic to the disabled inbound (R2-001). The
// Enabled predicate matches reqHasWhitelist.
func (m *Manager) allPorts() []Port {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ports []Port
	for _, inst := range m.instances {
		if !inst.req.Enabled {
			continue
		}
		ports = append(ports, inst.req.Ports...)
	}
	return ports
}

func (m *Manager) allUserMarks() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []int64
	for _, inst := range m.instances {
		if !inst.req.Enabled {
			continue
		}
		ids = append(ids, inst.req.UserMarks...)
	}
	return ids
}

// pinnedSHA256 returns the expected SHA-256 for the binary a
// Download describes, when that expectation is available locally
// without any network fetch: the pinned digest keyed by the mapped
// arch (Download.SHA256). Expectations that only exist remotely —
// a checksum file (ChecksumURL) or a GitHub release asset digest
// resolved by install() at download time — are NOT available here,
// so callers with only those sources cannot re-verify an on-disk
// binary in ensureBinary and keep the existence-skip below.
func pinnedSHA256(d *Download) (string, bool) {
	if d == nil || len(d.SHA256) == 0 {
		return "", false
	}
	arch, err := resolveArch(d)
	if err != nil {
		return "", false
	}
	want := strings.ToLower(strings.TrimSpace(d.SHA256[arch]))
	if !sha256HexPattern.MatchString(want) || len(want) != 64 {
		return "", false
	}
	return want, true
}

// fileSHA256 itself is shared from geodata.go.

// ensureBinary installs a binary into dir unless it is already there.
//
// C4 (low): an existing file is no longer trusted on existence
// alone when a pinned expected SHA-256 is available (see
// pinnedSHA256): it is re-hashed and a mismatch — or an unreadable
// file — is treated as missing and falls through to install(),
// which re-downloads, verifies and atomically swaps the whole
// directory in as before. When no pinned expectation is available
// for this Download (checksum-file-only or GitHub-digest-only
// specs), the existence-skip is kept, because the expectation
// cannot be established without a network fetch; those call faces
// are the main and extra-binary calls in Apply, whose Downloads
// may carry only ChecksumURL/GitHub sources.
func (m *Manager) ensureBinary(inst *instance, label string, d *Download, dir string) (string, error) {
	path := filepath.Join(dir, d.BinName)
	if _, err := os.Stat(path); err == nil {
		if want, ok := pinnedSHA256(d); ok {
			if got, herr := fileSHA256(path); herr == nil && got == want {
				return path, nil
			} else {
				inst.logs.add(fmt.Sprintf("[flmx] %s 已存在但 SHA256 复检不符，按缺失重装", label))
			}
		} else {
			// No locally available expected SHA-256 (see above):
			// keep the existence-skip for this call face.
			return path, nil
		}
	}
	inst.logs.add(fmt.Sprintf("[flmx] 安装 %s", label))
	// RV2-03: anchor-corroboration degradations land in this
	// instance's log ring — the surface the panel shows for the
	// kernel — not only in the agent's stdout.
	note := func(msg string) { inst.logs.add("[flmx] " + msg) }
	if err := install(d, dir, note); err != nil {
		return "", fmt.Errorf("安装 %s 失败: %v", label, err)
	}
	inst.logs.add(fmt.Sprintf("[flmx] %s 安装完成", label))
	return path, nil
}

// reqNeedsNftGuard reports whether an enabled request's security
// posture depends on the nft ruleset being installed: any port
// carrying a source whitelist.
// Such a kernel must never run while its rules are missing — the
// panel would show the protection on while nothing enforces it.
func reqNeedsNftGuard(req ApplyRequest) bool {
	return reqHasWhitelist(req)
}

// nftGuardFailure builds the fail-closed error surfaced when the nft
// rules a kernel depends on could not be installed and the kernel was
// therefore kept (or put) down.
func nftGuardFailure(req ApplyRequest, err error) error {
	switch {
	case reqHasWhitelist(req):
		return fmt.Errorf("入站白名单规则同步失败，已停止受保护内核（不允许无白名单运行）: %v", err)
	default:
		return fmt.Errorf("流量计数规则同步失败，内核未启动: %v", err)
	}
}

// stopGuardedInstances stops every enabled instance whose security
// posture depends on the nft ruleset after a failed sync: the failed
// table generation carries their rules too, so any of them still
// running is unguarded. It reports whether cur itself is such an
// instance; cur's processes are stopped as well, but its error is the
// caller's to set.
func (m *Manager) stopGuardedInstances(cur *instance, syncErr error) bool {
	type ent struct {
		inst *instance
		req  ApplyRequest
	}
	m.mu.Lock()
	ents := make([]ent, 0, len(m.instances))
	for _, in := range m.instances {
		ents = append(ents, ent{in, in.req})
	}
	m.mu.Unlock()
	curGuarded := false
	for _, e := range ents {
		if !reqNeedsNftGuard(e.req) {
			continue
		}
		m.stopProcesses(e.inst)
		if e.inst == cur {
			curGuarded = true
			continue
		}
		m.setErr(e.inst, nftGuardFailure(e.req, syncErr))
	}
	return curGuarded
}

// syncGuardBeforeRestart re-runs the full ruleset sync ahead of an
// internal restart (geodata / rule-set refresh, supervisor relaunch)
// that brings processes back up outside Apply/Control. The sync is
// content-verified, so a table flushed or rewritten out-of-band is
// rebuilt here instead of the restart silently running unguarded.
func (m *Manager) syncGuardBeforeRestart() error {
	gate, _ := isolateHopSync(m.nft.sync(m.allPorts(), m.allUserMarks()))
	return gate
}

// isolateHopSync splits a port-hop failure off a guard sync result
// (R2-F2-E02): when an old kernel rejects every hop rule, the main
// ruleset has still been applied, so the sync counts as successful
// for every gating decision and the returned gate error is nil; the
// hop failure comes back separately so the caller can surface it by
// name (instance log) instead of blocking a start or misreporting it
// as a whitelist/guard sync failure.
func isolateHopSync(err error) (gate error, hopNote string) {
	var he *hopRulesError
	if errors.As(err, &he) {
		return nil, he.Error()
	}
	return err, ""
}

// restartGuardError gates an internal restart of inst: nil when the
// restart may proceed, the sync error when it must not. Unguarded
// kernels always pass — counting alone is best-effort. Guarded ones
// (reqNeedsNftGuard, the B-02 predicate) pass only once the ruleset
// sync succeeds, the same bar Control start applies.
func (m *Manager) restartGuardError(inst *instance) error {
	m.mu.Lock()
	req := inst.req
	m.mu.Unlock()
	if !reqNeedsNftGuard(req) {
		return nil
	}
	return m.syncGuardBeforeRestart()
}

// Apply installs (if needed), writes the configuration and makes the
// processes match req.Enabled. Unchanged running kernels are left alone.
func (m *Manager) Apply(req ApplyRequest) (State, error) {
	if err := req.validate(); err != nil {
		return State{}, err
	}
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	m.mu.Lock()
	inst := m.instances[req.Type]
	if inst == nil {
		inst = &instance{logs: &logRing{}}
		m.instances[req.Type] = inst
	}
	m.mu.Unlock()

	binDir := m.binDir(req.Type, req.Version)
	bins := map[string]string{}
	// R2-F2-A2: a request carrying the unresolved "latest" sentinel can
	// never prove the binary on disk is current — the version-keyed
	// directory makes ensureBinary skip the download, which used to
	// turn "install the latest" into a silent no-op. The panel now
	// resolves "latest" to a concrete version before dispatch; when an
	// unresolved request still arrives (older panel, restored state)
	// and a binary is already present, name the skip in the instance
	// log instead of letting the apply read as an upgrade.
	unresolvedVersion := req.Version == "" || strings.EqualFold(req.Version, "latest")
	binPresent := false
	if req.Download != nil && req.Download.BinName != "" {
		_, statErr := os.Stat(filepath.Join(binDir, req.Download.BinName))
		binPresent = statErr == nil
	}
	mainBin, err := m.ensureBinary(inst, req.Type+" "+req.Version, req.Download, binDir)
	if err != nil {
		m.setErr(inst, err)
		return m.state(req.Type, false), err
	}
	if unresolvedVersion && binPresent {
		inst.logs.add("[flmx] 请求版本为 latest 且二进制已存在，本次未重新下载；实际运行版本以状态上报为准，升级请在面板选择具体版本")
	}
	bins[""] = mainBin
	for _, e := range req.Extras {
		dir := filepath.Join(m.baseDir, req.Type, "bin", "extra-"+e.Name+"-"+e.Version)
		path, err := m.ensureBinary(inst, e.Name+" "+e.Version, e.Download, dir)
		if err != nil {
			m.setErr(inst, err)
			return m.state(req.Type, false), err
		}
		bins[e.Name] = path
	}

	runDir := m.runDir(req.Type)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		m.setErr(inst, err)
		return m.state(req.Type, false), err
	}
	files := req.Files
	if req.Type == "singbox" {
		// FN34: localize the config's remote rule sets (downloaded
		// through the probe-selected plan) before anything is
		// written; on failure the previous files stay untouched.
		localized, lerr := m.localizeSingboxRuleSets(inst, req, runDir)
		if lerr != nil {
			m.setErr(inst, lerr)
			return m.state(req.Type, false), lerr
		}
		files = localized
	}
	prevFiles := readFiles(runDir, files)
	fix := adapters[req.Type].fixFile
	for _, f := range files {
		content := f.Content
		if fix != nil {
			content = fix(f.Name, content)
		}
		if err := writeFileAtomic(filepath.Join(runDir, f.Name), []byte(content), 0o600); err != nil {
			m.setErr(inst, err)
			return m.state(req.Type, false), err
		}
	}
	m.mu.Lock()
	changed := inst.req.Hash != req.Hash || inst.bins[""] != mainBin || len(inst.bins) != len(bins)
	running := len(inst.procs) > 0
	for _, p := range inst.procs {
		if r, _, _, _, _ := p.snapshot(); !r {
			running = false
		}
	}
	prev := prevApply{req: inst.req, binDir: inst.binDir, bins: inst.bins, version: inst.version, files: prevFiles}
	// F-032: 上一版是否"曾确认可用"在覆盖 inst.req 前定格，与瞬时
	// 进程快照无关（退避间隙 procs 不在运行，prev 仍是可回滚的好配置）。
	prevGood := prev.req.Enabled && prev.req.Hash != "" && prev.req.Hash == inst.goodHash
	inst.req = req
	inst.binDir = binDir
	inst.bins = bins
	inst.err = ""
	needVersion := inst.version == "" || changed
	m.mu.Unlock()
	// Point the connection log collector at the request just
	// accepted. This deliberately reads the CURRENT request on
	// every Apply (the R2-002 lesson): the rebuild predicate above
	// does not watch ConnLogInbounds, so a same-hash apply that
	// only flips collection keeps the process objects — the
	// collector, consulted per log line, is what follows.
	m.applyConnLog(inst, req)
	if needVersion {
		v := detectVersion(mainBin, adapters[req.Type].versionArgs)
		m.mu.Lock()
		inst.version = v
		m.mu.Unlock()
	}

	// The nft sync runs BEFORE any process starts: the ruleset carries
	// the traffic counters and the inbound whitelists. Counting alone
	// is best-effort, but a kernel guarded by the
	// ruleset (reqNeedsNftGuard) must never listen while its rules are
	// not installed, so a failed sync blocks its start outright.
	syncErr, hopNote := isolateHopSync(m.nft.sync(m.allPorts(), m.allUserMarks()))
	if hopNote != "" {
		// R2-F2-E02: the main ruleset is in place; only port hopping
		// is ineffective. Name it as what it is — never as a guard
		// sync failure — and let the kernel start.
		inst.logs.add("[flmx] " + hopNote)
	}
	if syncErr != nil {
		inst.logs.add("[flmx] 流量计数规则同步失败: " + syncErr.Error())
	}
	startBlocked := syncErr != nil && reqNeedsNftGuard(req)

	stable := true
	switch {
	case !req.Enabled:
		m.stopProcesses(inst)
	case changed || !running:
		m.stopProcesses(inst)
		if !startBlocked {
			m.startProcesses(inst)
			// F-032: 探活窗口内持续存活判定——窗口内曾存活又退出即失败，
			// 单点快照会把"启动后随即退出"误判成功、把慢启动误判失败。
			stable = m.waitStable(req.Type)
		} else {
			stable = false
		}
	}

	var failure error
	confirmed := true
	if startBlocked {
		// No rollback either: the previous configuration is guarded by
		// the same missing ruleset, so serving it would be just as
		// unprotected. The apply is torn down and never confirmed.
		failure = nftGuardFailure(req, syncErr)
		m.setErr(inst, failure)
		confirmed = false
	} else if req.Enabled && (!stable || !m.state(req.Type, false).Running) {
		failed := lastLines(inst.logs, 5)
		failure = fmt.Errorf("内核启动失败: %s", failed)
		confirmed = false
		if prevGood && syncErr != nil && reqNeedsNftGuard(prev.req) {
			// B-02 (A-02): startBlocked above keys on the NEW request,
			// but the rollback target is the PREVIOUS one — and it is
			// guarded by the same ruleset this sync just failed to
			// install. Rolling back would start it unguarded for the
			// whole probe window, only for the sweep below to stop it
			// again. Skip the rollback and stay down (fail-closed),
			// exactly as for a guarded new request.
			failure = nftGuardFailure(prev.req, syncErr)
			m.setErr(inst, failure)
		} else if prevGood {
			// The previous config is known-good: keep the node serving
			// on it instead of leaving it down.
			m.rollback(inst, runDir, prev)
			if m.state(req.Type, false).Running {
				failure = fmt.Errorf("新配置无法启动，已恢复上一次的配置: %s", failed)
				m.setErr(inst, failure)
				confirmed = true
			}
		}
	}

	// 记录最近一次确认可用的配置，供后续回滚判定（F-032）。
	if st := m.state(req.Type, false); st.Running {
		m.mu.Lock()
		if cur := m.instances[req.Type]; cur != nil && cur.req.Enabled {
			cur.goodHash = cur.req.Hash
		}
		m.mu.Unlock()
	}

	// The failed table generation carries every other instance's
	// rules too: any guarded kernel still running — this one left
	// unchanged, or a previous guarded instance — is unguarded now
	// and must stop. When this apply's own kernel is stopped by the
	// sweep, the apply must not confirm either.
	if syncErr != nil && m.stopGuardedInstances(inst, syncErr) && !startBlocked {
		if failure == nil {
			failure = nftGuardFailure(req, syncErr)
			m.setErr(inst, failure)
		}
		confirmed = false
	}

	if confirmed {
		m.persist()
	} else {
		// F-032: 未确认可用的坏配置不得 persist 固化，否则 agent
		// 重启 Restore 会反复拉起坏配置；保留上一次的持久状态。
		inst.logs.add("[flmx] 新配置未确认可用，未写入持久状态；重启后将恢复上一次的配置")
	}

	if confirmed {
		// R6-09: enforce the kernel binary retention policy —
		// only the current and previous generations' bin dirs
		// survive a confirmed apply (see pruneStaleBinDirs). An
		// unconfirmed apply prunes nothing: the persisted previous
		// state must find every directory exactly where it was.
		keep := make(map[string]bool)
		for _, p := range prev.bins {
			keep[filepath.Dir(p)] = true
		}
		m.mu.Lock()
		for _, p := range inst.bins {
			keep[filepath.Dir(p)] = true
		}
		m.mu.Unlock()
		if removed, perr := m.pruneStaleBinDirs(req.Type, keep); perr != nil {
			inst.logs.add("[flmx] 回收旧版本内核目录失败: " + perr.Error())
		} else if len(removed) > 0 {
			inst.logs.add("[flmx] 已回收旧版本内核目录: " + strings.Join(removed, ", "))
		}
	}
	// Publish the connection-log entry marks of the configuration
	// now in force — the new request, or the previous one when this
	// apply rolled back (rollback restores inst.req) — so the
	// kernel log collector's view (internal/connlog) always matches
	// the applied topology and a panel-side topology change leaves
	// no stale mark behind.
	m.mu.Lock()
	effReq := inst.req
	m.mu.Unlock()
	entries := make([]connlog.KernelEntry, 0, len(effReq.ConnlogEntries))
	for _, e := range effReq.ConnlogEntries {
		entries = append(entries, connlog.KernelEntry{InboundID: e.InboundID, Tag: e.Tag})
	}
	connlog.PublishKernelEntries(req.Type, entries)

	return m.state(req.Type, false), failure
}

// pruneStaleBinDirs enforces the kernel binary retention policy
// (R6-09): under <base>/<type>/bin, only the directories in keep
// survive; every other directory — a superseded version's main bin
// dir or an extra's versioned dir, each carrying its own geodata
// copy — is removed. Before this, install only ever replaced the
// same-version directory and nothing enumerated the version dirs,
// so every successful version change left one more generation on
// disk forever, recoverable only by uninstalling the whole kernel.
// The working set the code actually reads is two generations: the
// current one (Apply/Restore) and the previous one (the intra-Apply
// rollback source); anything older has no reader. Non-directory
// entries are left alone. Removal is best-effort: every entry is
// attempted, the first error is returned, and the caller logs it
// without failing the apply.
func (m *Manager) pruneStaleBinDirs(kernelType string, keep map[string]bool) ([]string, error) {
	binRoot := filepath.Join(m.baseDir, kernelType, "bin")
	entries, err := os.ReadDir(binRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var removed []string
	var firstErr error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(binRoot, e.Name())
		if keep[dir] {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed = append(removed, e.Name())
	}
	sort.Strings(removed)
	return removed, firstErr
}

// prevApply is what an instance ran before an apply, to go back to when the
// new config does not start.
type prevApply struct {
	req     ApplyRequest
	binDir  string
	bins    map[string]string
	version string
	files   map[string][]byte
}

func (m *Manager) rollback(inst *instance, runDir string, prev prevApply) {
	for name, content := range prev.files {
		path := filepath.Join(runDir, name)
		if content == nil {
			_ = os.Remove(path)
		} else {
			_ = writeFileAtomic(path, content, 0o600)
		}
	}
	m.mu.Lock()
	inst.req = prev.req
	inst.binDir = prev.binDir
	inst.bins = prev.bins
	inst.version = prev.version
	m.mu.Unlock()
	// The rolled-back request owns collection posture again.
	m.applyConnLog(inst, prev.req)
	m.stopProcesses(inst)
	m.startProcesses(inst)
	m.waitStable(prev.req.Type)
}

// waitStable 在探活窗口（startupProbeIn）内持续观察进程存活：达到过
// Running 后在窗口内退出即判失败；全程未 Running 也判失败。窗口长度
// 与旧的单次 Sleep 一致，不增加 Apply/Control 时延（F-032）。
func (m *Manager) waitStable(kernelType string) bool {
	deadline := time.Now().Add(startupProbeIn)
	seenRunning := false
	for {
		running := m.state(kernelType, false).Running
		if running {
			seenRunning = true
		} else if seenRunning {
			return false
		}
		if !time.Now().Before(deadline) {
			return seenRunning && running
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// readFiles returns the current contents of the files an apply is about to
// overwrite; nil marks a file that does not exist yet.
func readFiles(dir string, files []File) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f.Name))
		if err != nil {
			b = nil
		}
		out[f.Name] = b
	}
	return out
}

func expandArgs(args []string, runDir, binDir string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		a = strings.ReplaceAll(a, "{RUN_DIR}", runDir)
		out[i] = strings.ReplaceAll(a, "{BIN_DIR}", binDir)
	}
	return out
}

func lastLines(r *logRing, n int) string {
	lines := r.tail(n)
	if len(lines) == 0 {
		return "无输出"
	}
	return strings.Join(lines, " | ")
}

func (m *Manager) setErr(inst *instance, err error) {
	m.mu.Lock()
	inst.err = err.Error()
	m.mu.Unlock()
	inst.logs.add("[flmx] " + err.Error())
}

// applyConnLog creates the instance's connection log collector on
// first use (sing-box/xray only — the stacks with a parseable
// connection log) and points it at the given request's opt-in set.
// For xray it also (re)arms the access-file tailer against the
// freshly written run directory. Called from Apply and from
// rollback: whichever request currently owns the instance owns
// the collection posture.
func (m *Manager) applyConnLog(inst *instance, req ApplyRequest) {
	if req.Type != "singbox" && req.Type != "xray" {
		return
	}
	m.mu.Lock()
	if inst.connlog == nil {
		inst.connlog = newConnLogCollector(req.Type, nil)
	}
	c := inst.connlog
	m.mu.Unlock()
	c.setOptIns(connLogTags(req.ConnLogInbounds))
	if req.Type == "xray" {
		c.ensureTailer(m.runDir(req.Type), req.Files)
	}
}

// startProcesses launches every process of the instance's current request.
func (m *Manager) startProcesses(inst *instance) {
	m.mu.Lock()
	specs := inst.req.processSpecs()
	runDir := m.runDir(inst.req.Type)
	bins := inst.bins
	collector := inst.connlog
	m.mu.Unlock()

	procs := make([]*process, 0, len(specs))
	for _, spec := range specs {
		bin := bins[spec.Bin]
		prefix := ""
		if len(specs) > 1 {
			prefix = "[" + spec.Name + "] "
		}
		p := newProcess(bin, expandArgs(spec.Args, runDir, filepath.Dir(bin)), runDir, inst.logs)
		p.prefix = prefix
		// B-02 (A-03): the supervisor's crash relaunch is a start
		// like any other — a guarded kernel must not come back while
		// its rules are not installed. The gate re-runs the
		// content-verified sync before every relaunch; the first
		// launch needs no gate because every caller of
		// startProcesses already synced (or deliberately failed
		// closed) before calling it.
		//
		// R2-002: the gate is attached to EVERY process and judges
		// the instance's CURRENT posture at relaunch time
		// (restartGuardError reads inst.req then), never the posture
		// frozen at process creation. Apply's rebuild predicate does
		// not watch Ports/Allow, so adding a whitelist to a running
		// kernel under an unchanged hash keeps the process object —
		// a creation-time binding left exactly that surviving
		// process ungated for its whole generation. For an unguarded
		// posture the gate is a no-op pass, so unguarded kernels keep
		// the bare supervisor behavior.
		p.relaunchGuard = func() error { return m.restartGuardError(inst) }
		if collector != nil {
			// Connection log parsing (U4): every output line is
			// offered to the collector first — it diverts the
			// per-connection lines it recognises out of the log
			// ring and turns them into events — and each launch
			// opens a fresh connection-id generation for this
			// process. The hooks are attached unconditionally
			// (even with an empty opt-in set): Apply's rebuild
			// predicate does not watch ConnLogInbounds, so a
			// process created before collection was switched on
			// must still follow when the collector activates
			// (same R2-002 reasoning as relaunchGuard). Feed is a
			// no-op while no inbound is opted in.
			name := spec.Name
			p.lineTap = func(line string, at time.Time) bool {
				return collector.FeedAs(name, line, at)
			}
			p.launchHook = func(_ int, at time.Time) { collector.beginGeneration(name, at) }
			p.exitHook = func(time.Time) { collector.endGeneration(name) }
		}
		procs = append(procs, p)
	}
	m.mu.Lock()
	inst.procs = procs
	m.mu.Unlock()
	for _, p := range procs {
		p.start()
	}
}

func (m *Manager) stopProcesses(inst *instance) {
	m.mu.Lock()
	procs := inst.procs
	inst.procs = nil
	collector := inst.connlog
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range procs {
		wg.Add(1)
		go func(p *process) {
			defer wg.Done()
			p.stop()
		}(p)
	}
	wg.Wait()
	if collector != nil {
		// Each process's exitHook already swept its own open
		// connections; the tailer is instance-scoped and stops
		// with the process set.
		collector.stopTailer()
	}
}

// Control handles start / stop / restart / remove for an applied kernel.
func (m *Manager) Control(kernelType, action string) (State, error) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	m.mu.Lock()
	inst := m.instances[kernelType]
	m.mu.Unlock()

	if action == "remove" {
		if inst != nil {
			m.stopProcesses(inst)
			m.mu.Lock()
			delete(m.instances, kernelType)
			m.mu.Unlock()
		}
		if _, ok := adapters[kernelType]; ok {
			_ = os.RemoveAll(filepath.Join(m.baseDir, kernelType))
		}
		m.persist()
		// A hop-only failure does not make the removal fail: the
		// remaining ruleset synced fine (R2-F2-E02).
		if err, _ := isolateHopSync(m.nft.sync(m.allPorts(), m.allUserMarks())); err != nil {
			return State{Type: kernelType}, err
		}
		return State{Type: kernelType}, nil
	}
	if inst == nil {
		return State{Type: kernelType}, fmt.Errorf("内核 %s 未部署", kernelType)
	}

	switch action {
	case "stop":
		m.stopProcesses(inst)
		m.mu.Lock()
		inst.req.Enabled = false
		m.mu.Unlock()
	case "start", "restart":
		m.mu.Lock()
		inst.req.Enabled = true
		m.mu.Unlock()
		m.stopProcesses(inst)
		// Sync before start, as in Apply (B-02, R8-021): a guarded
		// kernel (an inbound whitelist) must
		// never listen before its rules are installed — a preceding
		// stop stripped them from the table, and after a node reboot
		// the table is gone entirely — so the guard goes back in
		// after stopping and before starting, and a failed sync
		// leaves the kernel stopped instead of running unguarded.
		if err, hopNote := isolateHopSync(m.nft.sync(m.allPorts(), m.allUserMarks())); err != nil {
			ferr := nftGuardFailure(inst.req, err)
			m.setErr(inst, ferr)
			return m.state(kernelType, false), ferr
		} else if hopNote != "" {
			inst.logs.add("[flmx] " + hopNote)
		}
		m.startProcesses(inst)
		// R2-029: the probe result is the receipt. Apply reports
		// this same event as an error; Control used to discard it
		// and return success for a kernel that died inside the
		// probe window. Enabled stays persisted either way (the
		// persist below is unconditional), so a later Control start
		// and the Restore self-heal retry exactly as before — only
		// the receipt now tells the truth.
		stable := m.waitStable(kernelType)
		if stable && m.state(kernelType, false).Running {
			m.mu.Lock()
			inst.goodHash = inst.req.Hash
			m.mu.Unlock()
		} else {
			ferr := fmt.Errorf("内核启动失败: %s", lastLines(inst.logs, 5))
			m.setErr(inst, ferr)
			m.persist()
			return m.state(kernelType, false), ferr
		}
	default:
		return State{}, fmt.Errorf("未知操作: %s", action)
	}
	if action == "stop" {
		// Keep the rules in lockstep with the lifecycle: stopping a
		// kernel must leave none of its rules behind. A sync failure
		// here is surfaced, not swallowed.
		if err, _ := isolateHopSync(m.nft.sync(m.allPorts(), m.allUserMarks())); err != nil {
			return m.state(kernelType, false), err
		}
	}
	m.persist()
	return m.state(kernelType, false), nil
}

func (m *Manager) state(kernelType string, withLogs bool) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst := m.instances[kernelType]
	st := State{Type: kernelType}
	if inst == nil {
		return st
	}
	st.Enabled = inst.req.Enabled
	st.ConfigHash = inst.req.Hash
	st.Version = inst.version
	st.Error = inst.err
	if len(inst.procs) > 0 {
		st.Running = true
		for i, p := range inst.procs {
			running, pid, restarts, lastErr, startedAt := p.snapshot()
			if i == 0 {
				st.Pid, st.StartedAt = pid, startedAt
			}
			st.Restarts += restarts
			if !running {
				st.Running = false
				if st.Error == "" && lastErr != "" {
					st.Error = p.prefix + lastErr + ": " + lastLines(inst.logs, 3)
				}
			}
		}
	}
	if withLogs {
		st.Logs = inst.logs.tail(logRingSize)
	}
	return st
}

// Status reports every kernel plus the nft traffic counters. logsFor selects
// one kernel whose recent output is included.
func (m *Manager) Status(logsFor string) Status {
	m.mu.Lock()
	types := make([]string, 0, len(m.instances))
	for t := range m.instances {
		types = append(types, t)
	}
	m.mu.Unlock()
	sort.Strings(types)

	out := Status{Kernels: make([]State, 0, len(types)), Counters: make([]Counter, 0), Users: make([]UserCounter, 0)}
	for _, t := range types {
		out.Kernels = append(out.Kernels, m.state(t, t == logsFor))
	}
	// F2-23: a logs query for a type with no instance must say so
	// explicitly — otherwise the panel synthesizes an empty state
	// with code 0 and the caller cannot tell "not installed" from
	// "installed but no logs yet".
	if logsFor != "" {
		found := false
		for _, t := range types {
			if t == logsFor {
				found = true
				break
			}
		}
		if !found {
			out.MissingLogsFor = logsFor
		}
	}
	if len(m.allPorts()) == 0 {
		out.NftAvailable = nftAvailable() == nil
		return out
	}
	counters, users, epoch, err := m.nft.read()
	if err != nil {
		out.NftError = err.Error()
		return out
	}
	out.NftAvailable = true
	if counters != nil {
		out.Counters = counters
	}
	if users != nil {
		out.Users = users
	}
	out.Epoch = epoch
	return out
}

// Uninstall stops every kernel and removes the agent's nftables tables,
// whose counters, whitelists and port hopping would otherwise outlive
// the agent in the kernel. Used when the panel deletes this server and
// asks the agent to remove itself.
//
// R9-07 (I-F2-02): Uninstall takes applyMu like Apply/Control — it
// previously bypassed the lock, so its Shutdown snapshot could predate
// an in-flight Apply, which then kept writing config, starting
// processes and persisting state into a directory being wiped. With
// applyMu held across Shutdown, an in-flight Apply settles first and
// no new instance can appear after the snapshot (Apply is the only
// instance creator and needs applyMu), so the snapshot Shutdown stops
// is final. Lock order stays applyMu → mu, the same order Apply and
// Control already use, and Shutdown only takes mu, so this cannot
// self-deadlock.
func (m *Manager) Uninstall() error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.Shutdown()
	return m.nft.removeTables()
}

// Shutdown stops every kernel process; the persisted state is kept so the
// next agent start restores them.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	insts := make([]*instance, 0, len(m.instances))
	for _, inst := range m.instances {
		insts = append(insts, inst)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, inst := range insts {
		wg.Add(1)
		go func(inst *instance) {
			defer wg.Done()
			m.stopProcesses(inst)
		}(inst)
	}
	wg.Wait()
}

func detectVersion(bin string, args []string) string {
	if len(args) == 0 {
		return ""
	}
	// D2-04: the previous hand-rolled timeout ran CombinedOutput
	// on a child goroutine and read cmd.Process from the caller on
	// the timeout path — a data race — and if Start itself was
	// delayed past 5s the kill was skipped and the caller (Apply,
	// holding applyMu) blocked for the process's full natural
	// life. CommandContext owns the whole lifecycle: the deadline
	// kills the process and CombinedOutput returns.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	// A killed probe's children can inherit the output pipes and
	// hold CombinedOutput open past the kill; WaitDelay bounds
	// that wait so the 5s budget is real.
	cmd.WaitDelay = 2 * time.Second
	out, _ := cmd.CombinedOutput()
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if len(line) > 100 {
		line = line[:100]
	}
	return line
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
