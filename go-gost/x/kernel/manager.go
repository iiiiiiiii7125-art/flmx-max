package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// defaultBaseDir is the script form's kernel root under the current
// install layout. Legacy (pre-rename) installs keep their root via
// the executable-directory recognition in ResolveBaseDir instead of
// this constant.
const defaultBaseDir = "/etc/flmx_agent/kernels"

// legacyBaseDir is the pre-rename script form's kernel root
// (/etc/flux_agent, mirroring legacyScriptLayoutDirName). It gets its
// own nft table (flmx_kernel_legacy, see nftTableForBaseDir) so it
// never shares one with the current root (F-R4-02). A not-yet-migrated
// legacy install's pre-fix plain table lingers as stale counting
// rules after the agent upgrades — harmless (counting-only, no
// filtering; the agent reads only its own table) and gone once the
// install is migrated or uninstalled.
const legacyBaseDir = "/etc/flux_agent/kernels"

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
	// tombstones are the auth_user names removed from the config
	// whose live connections the sweeper keeps closing until the
	// instance is reloaded or restarted (C-(4)): a removal-only
	// change skips the SIGHUP reload (the reload drops every
	// user's sessions), so the running config still admits these
	// users and only the selective kill + sweeper enforces the
	// removal — plus the nft reject rule for their marks (V-D),
	// which is what makes the removal airtight for fast new
	// connections. Re-adding a user clears its tombstone.
	tombstones map[string]bool
	// clashAddr is the companion's Clash API address
	// (experimental.clash_api.external_controller) from the last
	// applied config; "" when the API is not configured.
	clashAddr string
}

// Manager owns every kernel instance on this agent. Apply/Control/Uninstall
// are serialized by applyMu (downloads can take minutes); mu guards the maps
// so Status stays responsive meanwhile.
type Manager struct {
	baseDir string

	applyMu     sync.Mutex
	mu          sync.Mutex
	restoreOnce sync.Once
	sweepOnce   sync.Once
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
	// kernelExePath is os.Executable, the seam ResolveBaseDir uses
	// to learn the agent's own install directory (F2-NFT-01).
	kernelExePath = os.Executable
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
//     instead of dying with the container's writable layer. A
//     container whose working directory is unusable falls back to
//     defaultBaseDir — never to the executable's directory, whose
//     binary lives in the ephemeral image layer.
//  3. In the script form, <directory of this agent's own
//     executable>/kernels. The install identity is the binary's
//     own location: the unit runs the binary out of its install
//     directory, so current installs root under /etc/flmx_agent
//     and not-yet-migrated legacy installs keep their existing
//     /etc/flux_agent root (kernel state stays continuous across a
//     panel-driven in-place upgrade; install.sh's migration moves
//     the whole directory, root included). Crucially, a second
//     agent on the same host — a scratch install, a relocated
//     install — roots under ITS OWN directory instead of falling
//     through to the live install's root: before this, any agent
//     whose working directory was not a recognized layout silently
//     shared /etc/flmx_agent/kernels with the live agent and
//     overwrote its binaries and state.json (F2-NFT-01).
//  4. When the executable path cannot be resolved, the working
//     directory is a recognized install layout after all: root
//     under it (the pre-F2-NFT-01 rule, kept as a fallback so a
//     degraded process still finds the canonical install).
//  5. defaultBaseDir — the current layout's root.
func ResolveBaseDir() string {
	dir := strings.TrimSpace(os.Getenv("FLMX_KERNEL_DIR"))
	if dir != "" {
		return dir
	}
	if detectKernelContainer() {
		if wd, err := kernelWorkDir(); err == nil && filepath.IsAbs(wd) {
			return filepath.Join(wd, "kernels")
		}
		return defaultBaseDir
	}
	if exe, err := kernelExePath(); err == nil && filepath.IsAbs(exe) {
		return filepath.Join(filepath.Dir(exe), "kernels")
	}
	if wd, err := kernelWorkDir(); err == nil && filepath.IsAbs(wd) {
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

// allBlockedUserMarks derives the nft desired state for tombstoned
// users (V-D): a removed user's mark is rejected in the output
// chain, so a new connection admitted by the still-running old
// config dies at the handshake instead of draining through the 2s
// sweeper's graceful close. Only enabled instances contribute; a
// tombstone whose name does not parse to a user id is skipped here
// (the sweeper still closes it by name).
func (m *Manager) allBlockedUserMarks() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []int64
	for _, inst := range m.instances {
		if !inst.req.Enabled {
			continue
		}
		for name := range inst.tombstones {
			if id, ok := tombstoneUserID(name); ok {
				ids = append(ids, id)
			}
		}
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
	gate, _ := isolateHopSync(m.nft.sync(m.allPorts(), m.allUserMarks(), m.allBlockedUserMarks()))
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
	// C-(4): a running sing-box instance can apply a config-only
	// change by SIGHUP reload instead of a process restart (see
	// tryReloadSingbox). The reload needs the config to have
	// changed, the binary to be unchanged, and the process to be
	// up; anything else restarts as before.
	reloadable := isSingboxType(req.Type) && running &&
		prev.req.Hash != req.Hash && req.Hash != "" &&
		prev.bins[""] == mainBin && len(prev.bins) == len(bins)
	// F-032: 上一版是否"曾确认可用"在覆盖 inst.req 前定格，与瞬时
	// 进程快照无关（退避间隙 procs 不在运行，prev 仍是可回滚的好配置）。
	prevGood := prev.req.Enabled && prev.req.Hash != "" && prev.req.Hash == inst.goodHash
	inst.req = req
	// F2-DBL-01：先采纳面板下发的 mark base，再跑 nft sync，
	// 本次 sync 的用户计数规则就按新 base 造 mark（与内核配
	// 置里的 mark 同源）。0（旧面板）时 setMarkBase 忽略，
	// legacy base 继续生效。
	m.nft.setMarkBase(req.UserMarkBase)
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

	// C-(4): validate the new config before the nft sync when a
	// reload is planned — a bad config must fail here, while the
	// old process is still running the old config and the old nft
	// rules are still installed. Restoring the files and the
	// in-memory request keeps a later restart on the good config;
	// the nft rules are never synced for the bad config.
	if reloadable {
		if cerr := checkSingboxConfig(mainBin, singboxConfigPath(req, runDir)); cerr != nil {
			restorePrevFiles(runDir, prev)
			m.mu.Lock()
			inst.req = prev.req
			inst.binDir = prev.binDir
			inst.bins = prev.bins
			inst.version = prev.version
			m.mu.Unlock()
			m.applyConnLog(inst, prev.req)
			err := fmt.Errorf("sing-box 配置校验失败，已保留旧配置: %w", cerr)
			m.setErr(inst, err)
			return m.state(req.Type, false), err
		}
	}

	// C-(4)/V-D: which users did this apply remove, and is the change
	// removal-only (only the user list changed)? The diff is on the
	// on-disk configs (previous files vs the just-written new ones).
	// This runs BEFORE the nft sync below — and after the config
	// validation above, so a bad config never kills anything — so
	// the tombstones it creates are already in the render: the nft
	// reject rule for a tombstoned mark must land in the SAME table
	// rebuild as the truncation, or a fast new connection completes
	// before the next rebuild (V-D live proof 2026-11-16).
	removedUsers, addedUsers, userOnlyChange := m.applyUserDiff(req.Type, prev, runDir)
	// Cache the companion's Clash API address from the new config
	// for the selective kill and the tombstone sweeper.
	clashAddr := ""
	if isSingboxType(req.Type) {
		if b, err := os.ReadFile(filepath.Join(runDir, "config.json")); err == nil {
			clashAddr = clashAPIAddr(b)
			m.mu.Lock()
			inst.clashAddr = clashAddr
			m.mu.Unlock()
		}
	}
	// C-(4): a removal-only change is a candidate for skipping the
	// SIGHUP reload — the reload drops every user's sessions (live
	// proof 2026-11-15), so it cannot be the per-user kill. The
	// candidate is confirmed only if the selective kill below can
	// actually reach the companion's Clash API; otherwise the
	// reload stays (the removal is still enforced, at the cost of
	// the pre-existing reload blast radius).
	skipReloadCandidate := isSingboxType(req.Type) && req.Enabled && running && reloadable &&
		userOnlyChange && len(removedUsers) > 0 && len(addedUsers) == 0
	skipReload := false
	if skipReloadCandidate {
		if clashAddr == "" {
			inst.logs.add(fmt.Sprintf("[flmx] 伴生未配置 Clash API，用户 %v 的已有连接无法连接级关闭，改走配置重载", removedUsers))
		} else if closed, err := closeClashUserConnections(clashAddr, removedUsers); err != nil {
			inst.logs.add(fmt.Sprintf("[flmx] 用户 %v 的已有连接连接级关闭失败（%v），改走配置重载", removedUsers, err))
		} else {
			skipReload = true
			inst.logs.add(fmt.Sprintf("[flmx] 已连接级关闭用户 %v 的已有连接 %d 条，未重载配置（其他用户会话保留）", removedUsers, closed))
			// Tombstone the removed users: the running config still
			// admits them, so the sweeper keeps closing any they
			// open again until a reload/restart refreshes it — and
			// the nft sync below rejects their new dials at the
			// handshake (V-D), which is what makes the removal
			// airtight for fast transfers.
			m.mu.Lock()
			if inst.tombstones == nil {
				inst.tombstones = make(map[string]bool)
			}
			for _, u := range removedUsers {
				inst.tombstones[u] = true
			}
			m.mu.Unlock()
			m.startSweeperOnce()
		}
	}
	// V-D: a re-added user is live again — clear it from the
	// tombstones BEFORE the sync, so the rebuild does not carry a
	// reject rule for a user the new config admits. The reconcile
	// after the switch repeats this; doing it here is what makes
	// the render correct.
	m.mu.Lock()
	if len(inst.tombstones) > 0 {
		if b, err := os.ReadFile(filepath.Join(runDir, "config.json")); err == nil {
			if live, err := singboxUsers(b); err == nil {
				for _, u := range live {
					delete(inst.tombstones, u)
				}
			}
		}
	}
	m.mu.Unlock()

	// The nft sync runs BEFORE any process starts: the ruleset carries
	// the traffic counters and the inbound whitelists. Counting alone
	// is best-effort, but a kernel guarded by the
	// ruleset (reqNeedsNftGuard) must never listen while its rules are
	// not installed, so a failed sync blocks its start outright.
	// V-D: the sync renders the tombstones created above as reject
	// rules, so the truncation and its data-plane enforcement land
	// in one rebuild.
	syncErr, hopNote := isolateHopSync(m.nft.sync(m.allPorts(), m.allUserMarks(), m.allBlockedUserMarks()))
	if hopNote != "" {
		// R2-F2-E02: the main ruleset is in place; only port hopping
		// is ineffective. Name it as what it is — never as a guard
		// sync failure — and let the kernel start.
		inst.logs.add("[flmx] " + hopNote)
	}
	if syncErr != nil {
		inst.logs.add("[flmx] 流量计数规则同步失败: " + syncErr.Error())
	}
	// E-01: the per-agent base is derived from the panel's node
	// secret, so two panels' same-id nodes collide only when their
	// secrets hash to the same first byte (1/256 per pair). Make
	// that residual visible instead of silent: after a successful
	// sync, scan the ruleset for our marks in other tables and log
	// any hit (best-effort — a failed scan never blocks the apply).
	if syncErr == nil {
		for _, c := range m.nft.detectMarkCollision(m.allUserMarks()) {
			msg := "[flmx] socket-mark 碰撞：" + c.String() + "——另一 agent/面板在用同一 mark，本节点与对方的订阅计数会互相串扰"
			inst.logs.add(msg)
			log.Printf("%s", msg)
		}
	}
	startBlocked := syncErr != nil && reqNeedsNftGuard(req)

	stable := true
	// reloadedClean is set when the config was applied by a SIGHUP
	// reload with the process never restarting. A reload drops
	// every user's established sessions (C-(4) live proof
	// 2026-11-15) — it is not a per-user kill — so a removal-only
	// change skips it (skipReload above) and the removed users are
	// enforced selectively instead.
	reloadedClean := false
	// configRefreshed is set when the running process now has the
	// new config (reload or restart): the tombstones are then spent
	// and cleared below.
	configRefreshed := false
	switch {
	case !req.Enabled:
		m.stopProcesses(inst)
		m.stopProcesses(inst)
	case skipReload:
		// Removal-only: the selective kill + sweeper + nft reject
		// already enforce it; the process keeps running the old
		// config untouched, so the kept users' sessions are
		// preserved. stable stays true.
	case changed || !running:
		// C-(4): try a SIGHUP reload before restarting — a
		// restart drops every established session of every user
		// on the instance, and a reload drops them too (it
		// re-creates the shared inbound/outbound/router).
		applied, restarted := m.tryReloadSingbox(inst, reloadable)
		if applied {
			reloadedClean = !restarted
			configRefreshed = true
			if restarted {
				inst.logs.add("[flmx] sing-box 配置重载致进程重启（会话已断），已按新配置恢复")
			}
			stable = m.waitStable(req.Type)
		} else {
			m.stopProcesses(inst)
			if !startBlocked {
				m.startProcesses(inst)
				configRefreshed = true
				// F-032: 探活窗口内持续存活判定——窗口内曾存活又退出即失败，
				// 单点快照会把"启动后随即退出"误判成功、把慢启动误判失败。
				stable = m.waitStable(req.Type)
			} else {
				stable = false
			}
		}
	}

	// C-(4): how the removed users' live sessions were handled.
	// A SIGHUP reload drops EVERY user's established sessions
	// (live proof 2026-11-15: bare SIGHUP closed both u2's and
	// u3's upstreams within seconds; which download then
	// "survived" was a buffer race, not a selective kill). The
	// reload is therefore not a per-user kill: a removal-only
	// change skips it and closes the removed users' connections
	// selectively through the companion's Clash API (skipReload
	// above, already logged). A reload/restart drops every
	// session, so there is nothing selective to do then.
	if skipReload {
		// Already logged at kill time; the tombstone sweeper and
		// the nft reject rule keep enforcing it until a
		// reload/restart.
	} else if reloadedClean && req.Enabled && stable && len(req.UserMarks) > 0 {
		removed := diffUserMarks(prev.req.UserMarks, req.UserMarks)
		if len(removed) > 0 {
			inst.logs.add(fmt.Sprintf("[flmx] 配置重载完成，用户 %v 已从配置移除（重载同时断开了本实例全部用户的已有连接，见 C-(4)）", removed))
		}
	} else if len(req.UserMarks) > 0 && len(diffUserMarks(prev.req.UserMarks, req.UserMarks)) > 0 {
		inst.logs.add(fmt.Sprintf("[flmx] 配置未走干净重载（reloadedClean=%v enabled=%v stable=%v），移除用户 %v 的已有连接未切断", reloadedClean, req.Enabled, stable, diffUserMarks(prev.req.UserMarks, req.UserMarks)))
	}
	// Reconcile the tombstones: a reload/restart (or a stop) makes
	// the in-memory config fresh, so the tombstones are spent; a
	// re-added user is live again and leaves the set.
	m.mu.Lock()
	if !req.Enabled || configRefreshed {
		inst.tombstones = nil
	} else if len(inst.tombstones) > 0 {
		if b, err := os.ReadFile(filepath.Join(runDir, "config.json")); err == nil {
			if live, err := singboxUsers(b); err == nil {
				for _, u := range live {
					delete(inst.tombstones, u)
				}
			}
		}
	}
	m.mu.Unlock()

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
	return m.state(req.Type, false), failure
}

// applyUserDiff diffs the previous and the just-written sing-box
// configs (auth_user names) and reports which users were removed,
// which were added, and whether the change is user-only (nothing
// but the user list and its derived outbounds/rules changed).
// Non-sing-box types and first applies report no diff.
func (m *Manager) applyUserDiff(kernelType string, prev prevApply, runDir string) (removed, added []string, userOnly bool) {
	if !isSingboxType(kernelType) {
		return nil, nil, false
	}
	oldConfig, ok := prev.files["config.json"]
	if !ok || len(oldConfig) == 0 {
		return nil, nil, false
	}
	newConfig, err := os.ReadFile(filepath.Join(runDir, "config.json"))
	if err != nil || len(newConfig) == 0 {
		return nil, nil, false
	}
	removed, err = removedSingboxUsers(oldConfig, newConfig)
	if err != nil {
		return nil, nil, false
	}
	added, err = addedSingboxUsers(oldConfig, newConfig)
	if err != nil {
		return nil, nil, false
	}
	userOnly, err = isUserOnlySingboxChange(oldConfig, newConfig)
	if err != nil {
		return nil, nil, false
	}
	return removed, added, userOnly
}

// tombstoneSweepInterval is how often the sweeper re-closes a
// tombstoned user's new connections (C-(4)).
const tombstoneSweepInterval = 2 * time.Second

// startSweeperOnce starts the tombstone sweeper goroutine (once per
// Manager lifetime). It ticks every tombstoneSweepInterval and, for
// each instance with tombstones, closes any live connection admitted
// as a tombstoned user. It does no work (and no HTTP call) when
// there is nothing tombstoned.
func (m *Manager) startSweeperOnce() {
	m.sweepOnce.Do(func() {
		go m.sweepTombstonesLoop()
	})
}

func (m *Manager) sweepTombstonesLoop() {
	for {
		time.Sleep(tombstoneSweepInterval)
		m.mu.Lock()
		type target struct {
			kernelType string
			addr       string
			users      []string
		}
		var targets []target
		for kernelType, inst := range m.instances {
			if len(inst.tombstones) == 0 || inst.clashAddr == "" {
				continue
			}
			users := make([]string, 0, len(inst.tombstones))
			for u := range inst.tombstones {
				users = append(users, u)
			}
			targets = append(targets, target{kernelType, inst.clashAddr, users})
		}
		m.mu.Unlock()
		for _, t := range targets {
			closed, err := closeClashUserConnections(t.addr, t.users)
			if err != nil {
				// The companion may be mid-restart; the next tick
				// retries. Do not spam the log every 2s.
				continue
			}
			if closed > 0 {
				m.mu.Lock()
				if inst := m.instances[t.kernelType]; inst != nil {
					inst.logs.add(fmt.Sprintf("[flmx] 兜底关闭已移除用户 %v 的新建连接 %d 条", t.users, closed))
				}
				m.mu.Unlock()
			}
		}
	}
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

// restorePrevFiles writes the previous apply's file contents back
// over the run dir (nil content removes the file). Used by rollback
// and by the reload config-check failure path, both of which must
// leave the on-disk config consistent with the in-memory request.
func restorePrevFiles(runDir string, prev prevApply) {
	for name, content := range prev.files {
		path := filepath.Join(runDir, name)
		if content == nil {
			_ = os.Remove(path)
		} else {
			_ = writeFileAtomic(path, content, 0o600)
		}
	}
}

func (m *Manager) rollback(inst *instance, runDir string, prev prevApply) {
	restorePrevFiles(runDir, prev)
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

// isSingboxType reports whether a kernel type runs the sing-box
// binary (main or metering companion). Only these support a SIGHUP
// config reload that preserves established connections.
func isSingboxType(t string) bool {
	return t == "singbox" || t == "singbox-companion"
}

// diffUserMarks returns the user ids present in prev but absent in
// cur (removed users), sorted for deterministic logging.
func diffUserMarks(prev, cur []int64) []int64 {
	inCur := make(map[int64]bool, len(cur))
	for _, id := range cur {
		inCur[id] = true
	}
	seen := make(map[int64]bool)
	var out []int64
	for _, id := range prev {
		if !inCur[id] && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	// small sets; insertion sort keeps the import list minimal.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// singboxConfigPath is the config file a sing-box process was started
// with (the `-c` argument, {RUN_DIR} expanded). It falls back to the
// panel-rendered well-known name when the args carry no -c.
func singboxConfigPath(req ApplyRequest, runDir string) string {
	args := req.Args
	if len(args) == 0 && len(req.Processes) > 0 {
		args = req.Processes[0].Args
	}
	for i, a := range args {
		if a == "-c" && i+1 < len(args) {
			return strings.ReplaceAll(args[i+1], "{RUN_DIR}", runDir)
		}
	}
	return filepath.Join(runDir, "config.json")
}

// checkSingboxConfig runs `sing-box check` on a config file. A
// non-zero exit or a timeout is an error carrying the tool output,
// so a bad config fails the apply before the old process or the nft
// rules are touched.
func checkSingboxConfig(bin, configPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "check", "-c", configPath)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sing-box check -c %s: %w (output: %s)", configPath, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// tryReloadSingbox SIGHUPs a running sing-box instance so it re-reads
// its (already written) config without dropping established
// connections. It returns (applied, restarted):
//   - (true, false): reloaded in place; sessions preserved.
//   - (true, true): the process restarted on its own (supervisor
//     relaunch) with the new config; sessions were lost but the
//     config is applied — the caller must NOT restart it again.
//   - (false, false): reload not applicable (caller falls through
//     to the normal restart path).
func (m *Manager) tryReloadSingbox(inst *instance, reloadable bool) (applied, restarted bool) {
	if !reloadable {
		return false, false
	}
	type generation struct {
		restarts  int
		startedAt int64
	}
	m.mu.Lock()
	procs := append([]*process(nil), inst.procs...)
	m.mu.Unlock()
	if len(procs) == 0 {
		return false, false
	}
	before := make([]generation, 0, len(procs))
	for _, p := range procs {
		_, _, r, _, s := p.snapshot()
		before = append(before, generation{r, s})
	}
	for _, p := range procs {
		p.sighup()
	}
	// sing-box applies a reload within milliseconds; give it a
	// second to settle, watching for a supervisor relaunch (the
	// backoff starts at 1s, so a crash shows up as a new generation
	// inside this window).
	deadline := time.Now().Add(1 * time.Second)
	for {
		restartedNow := false
		running := false
		for i, p := range procs {
			r, _, restarts, _, startedAt := p.snapshot()
			if r {
				running = true
			}
			if i < len(before) && (restarts != before[i].restarts || startedAt != before[i].startedAt) {
				restartedNow = true
			}
		}
		if restartedNow {
			return true, true
		}
		if time.Now().After(deadline) {
			if running {
				return true, false
			}
			// Died and not back yet; the supervisor may still
			// relaunch it, so let the caller restart cleanly.
			return false, false
		}
		time.Sleep(100 * time.Millisecond)
	}
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

// connlogEffectiveOptIns resolves the collection-effective opt-in
// set of an ApplyRequest: the per-inbound opt-in list
// (ConnLogInbounds, unit U5) intersected with the panel's topology
// qualification (ConnlogEntries, unit U7). An inbound is collected
// only when BOTH lists name it — the opt-in list is the node's
// collection gate (the node switch is the only switch; there is no
// per-inbound switch anymore), and the topology list is the entry
// qualification (a chain target's kernel log lines name the
// previous hop, not the client: wrong data, not weak data). A relay
// landing inbound IS named by both lists on purpose (R2-D-P2-1):
// a public inbound can take direct connections as well as relay
// landings, so excluding it would hide its direct records; its
// relay leg shows one row whose client IP is the previous hop.
// Resolved here, at the point of use, from the one request that
// owns the instance, so Apply and rollback can never disagree
// about the set.
func connlogEffectiveOptIns(req ApplyRequest) []int64 {
	if len(req.ConnLogInbounds) == 0 || len(req.ConnlogEntries) == 0 {
		return nil
	}
	qualified := make(map[int64]bool, len(req.ConnlogEntries))
	for _, e := range req.ConnlogEntries {
		qualified[e.InboundID] = true
	}
	out := make([]int64, 0, len(req.ConnLogInbounds))
	for _, id := range req.ConnLogInbounds {
		if qualified[id] {
			out = append(out, id)
		}
	}
	return out
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
	c.setOptIns(connLogTags(connlogEffectiveOptIns(req)))
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
		if err, _ := isolateHopSync(m.nft.sync(m.allPorts(), m.allUserMarks(), m.allBlockedUserMarks())); err != nil {
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
		if err, hopNote := isolateHopSync(m.nft.sync(m.allPorts(), m.allUserMarks(), m.allBlockedUserMarks())); err != nil {
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
		if err, _ := isolateHopSync(m.nft.sync(m.allPorts(), m.allUserMarks(), m.allBlockedUserMarks())); err != nil {
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
	// nft 面状态随每次上报：NftError 只承载自动安装失败的原因
	// （见 types.go），单纯缺失不进此字段。
	if auto, ierr := m.nft.installStatus(); auto {
		out.NftAutoInstalled = true
	} else if ierr != "" {
		out.NftError = ierr
	}
	out.NftAvailable = nftAvailable() == nil
	if len(m.allPorts()) == 0 {
		return out
	}
	counters, users, epoch, err := m.nft.read()
	if err != nil {
		return out
	}
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
	// R2C-02：优雅退出回收自家 nft 表——只删自己的表（removeTables
	// 的 legacy 清理分支会动活着的 legacy agent 的表，退出路径不
	// 得碰；Uninstall 仍走 removeTables 全量清理）。失败只记日志、
	// 不阻塞退出；下次启动 sync 即重建该表。
	if m.nft != nil {
		if err := m.nft.removeOwnTable(); err != nil {
			log.Printf("agent shutdown nft reclaim failed table=%s err=%v", m.nft.tableName(), err)
		}
	}
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
