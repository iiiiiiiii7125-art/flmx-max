package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultBaseDir = "/etc/flux_agent/kernels"

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
}

// Manager owns every kernel instance on this agent. Apply/Control are
// serialized by applyMu (downloads can take minutes); mu guards the maps so
// Status stays responsive meanwhile.
type Manager struct {
	baseDir string

	applyMu     sync.Mutex
	mu          sync.Mutex
	restoreOnce sync.Once
	instances   map[string]*instance
	nft         *nftCounters

	// Snell egress guard identity (snell_guard.go), resolved when a
	// Snell apply first needs it and reused by Control start.
	snellUID uint32
	snellGID uint32
}

var (
	defaultManager     *Manager
	defaultManagerOnce sync.Once
)

// Default returns the process-wide manager rooted at FLMX_KERNEL_DIR
// (legacy FLVX_KERNEL_DIR still honored) or /etc/flux_agent/kernels.
func Default() *Manager {
	defaultManagerOnce.Do(func() {
		dir := strings.TrimSpace(os.Getenv("FLMX_KERNEL_DIR"))
		if dir == "" {
			dir = strings.TrimSpace(os.Getenv("FLVX_KERNEL_DIR"))
		}
		if dir == "" {
			dir = defaultBaseDir
		}
		defaultManager = NewManager(dir)
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

func (m *Manager) allPorts() []Port {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ports []Port
	for _, inst := range m.instances {
		ports = append(ports, inst.req.Ports...)
	}
	return ports
}

func (m *Manager) allUserMarks() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []int64
	for _, inst := range m.instances {
		ids = append(ids, inst.req.UserMarks...)
	}
	return ids
}

// ensureBinary installs a binary into dir unless it is already there.
func (m *Manager) ensureBinary(inst *instance, label string, d *Download, dir string) (string, error) {
	path := filepath.Join(dir, d.BinName)
	if _, err := os.Stat(path); err == nil {
		return path, nil
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
// posture depends on the nft ruleset being installed: a Snell server
// (private-egress guard) or any port carrying a source whitelist.
// Such a kernel must never run while its rules are missing — the
// panel would show the protection on while nothing enforces it.
func reqNeedsNftGuard(req ApplyRequest) bool {
	return reqHasSnellServer(req) || reqHasWhitelist(req)
}

// nftGuardFailure builds the fail-closed error surfaced when the nft
// rules a kernel depends on could not be installed and the kernel was
// therefore kept (or put) down.
func nftGuardFailure(req ApplyRequest, err error) error {
	switch {
	case reqHasSnellServer(req):
		return fmt.Errorf("Snell 私网拦截规则下发失败，已停止 Snell（不允许无拦截运行）: %v", err)
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
	m.mu.Lock()
	guard := m.snellGuardForLocked(m.snellUID)
	m.mu.Unlock()
	return m.nft.syncGuard(m.allPorts(), m.allUserMarks(), guard)
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

	// Snell egress guard (snell_guard.go): an enabled Snell request
	// must never start unguarded, so the guard's prerequisites — the
	// dedicated user and nftables — are hard prerequisites of the
	// apply itself, checked before any download or process starts.
	needGuard := reqHasSnellServer(req)
	if needGuard {
		uid, gid, err := ensureSnellGuardUser(context.Background())
		if err != nil {
			m.setErr(inst, err)
			return m.state(req.Type, false), err
		}
		if err := nftAvailable(); err != nil {
			err = fmt.Errorf("Snell 私网拦截需要 nftables，本机不可用: %v", err)
			m.setErr(inst, err)
			return m.state(req.Type, false), err
		}
		m.mu.Lock()
		m.snellUID, m.snellGID = uid, gid
		m.mu.Unlock()
	}

	if needGuard {
		// R8-021: install the guard BEFORE any snell-server process
		// starts. The sync used to run only at the end of the apply,
		// leaving a seconds-wide window — first enable, stop→start,
		// and Restore after a node reboot (the nft table is gone
		// then) — in which a freshly started snell-server ran
		// unguarded. The table rebuild is a single atomic nft
		// transaction and the guard content depends only on the
		// guard uid and the system resolvers, not on this apply's
		// files or processes, so it can go first; the sync at the
		// end still runs and finalizes the counter rules (a no-op
		// when nothing changed). Failing here means no process was
		// started at all. If a later step fails, the guard simply
		// stays installed with no snell-server running under it —
		// harmless, and the next sync cleans it up.
		m.mu.Lock()
		guardUID := m.snellUID
		m.mu.Unlock()
		guard := &snellGuard{UID: guardUID, Resolvers: systemResolvers()}
		if err := m.nft.syncGuard(m.allPorts(), m.allUserMarks(), guard); err != nil {
			err = fmt.Errorf("Snell 私网拦截规则下发失败，未启动 Snell（不允许无拦截运行）: %v", err)
			m.setErr(inst, err)
			return m.state(req.Type, false), err
		}
	}

	binDir := m.binDir(req.Type, req.Version)
	bins := map[string]string{}
	mainBin, err := m.ensureBinary(inst, req.Type+" "+req.Version, req.Download, binDir)
	if err != nil {
		m.setErr(inst, err)
		return m.state(req.Type, false), err
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
	if needGuard {
		// snell-server runs as the guard user and must be able to
		// reach its binary and its config under the agent's 0700
		// directories: make the path ancestors traverse-only.
		if err := chmodTraverseAncestors(m.baseDir, binDir); err != nil {
			m.setErr(inst, err)
			return m.state(req.Type, false), err
		}
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
	if needGuard {
		m.mu.Lock()
		uid, gid := m.snellUID, m.snellGID
		m.mu.Unlock()
		if err := fixSnellRunPerms(runDir, files, uid, gid); err != nil {
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
	if needVersion {
		v := detectVersion(mainBin, adapters[req.Type].versionArgs)
		m.mu.Lock()
		inst.version = v
		m.mu.Unlock()
	}

	// The nft sync runs BEFORE any process starts: the ruleset carries
	// the traffic counters, the inbound whitelists and the Snell egress
	// guard. Counting alone is best-effort, but a kernel guarded by the
	// ruleset (reqNeedsNftGuard) must never listen while its rules are
	// not installed, so a failed sync blocks its start outright.
	m.mu.Lock()
	guardUID := m.snellUID
	m.mu.Unlock()
	guard := m.snellGuardFor(guardUID)
	syncErr := m.nft.syncGuard(m.allPorts(), m.allUserMarks(), guard)
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
	return m.state(req.Type, false), failure
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
	if reqHasSnellServer(prev.req) {
		// Restored configs are root-owned again after the rewrite;
		// hand them back to the guard user before the processes
		// start or snell-server cannot read its own config.
		m.mu.Lock()
		uid, gid := m.snellUID, m.snellGID
		m.mu.Unlock()
		files := make([]File, 0, len(prev.files))
		for name, content := range prev.files {
			if content != nil {
				files = append(files, File{Name: name})
			}
		}
		_ = fixSnellRunPerms(runDir, files, uid, gid)
	}
	m.mu.Lock()
	inst.req = prev.req
	inst.binDir = prev.binDir
	inst.bins = prev.bins
	inst.version = prev.version
	m.mu.Unlock()
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

// startProcesses launches every process of the instance's current request.
func (m *Manager) startProcesses(inst *instance) {
	m.mu.Lock()
	specs := inst.req.processSpecs()
	runDir := m.runDir(inst.req.Type)
	bins := inst.bins
	kernelType := inst.req.Type
	guarded := reqNeedsNftGuard(inst.req)
	guardUID, guardGID := m.snellUID, m.snellGID
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
		if guarded {
			// B-02 (A-03): the supervisor's crash relaunch is a start
			// like any other — a guarded kernel must not come back
			// while its rules are not installed. The gate re-runs the
			// content-verified sync before every relaunch; the first
			// launch needs no gate because every caller of
			// startProcesses already synced (or deliberately failed
			// closed) before calling it.
			p.relaunchGuard = m.syncGuardBeforeRestart
		}
		if kernelType == "snell" && isSnellServerSpec(spec.Name) {
			// Snell egress guard: only snell-server itself drops to
			// the guard user; shadow-tls fronts stay on the agent
			// identity (they dial admin-configured targets only).
			p.credUID, p.credGID = guardUID, guardGID
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
		m.mu.Lock()
		guard := m.snellGuardForLocked(m.snellUID)
		m.mu.Unlock()
		if err := m.nft.syncGuard(m.allPorts(), m.allUserMarks(), guard); err != nil {
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
		// reqHasSnellServer keys on Enabled, which a preceding stop
		// left false: judge the request as the start will leave it,
		// or the prerequisites and the guard pre-sync below are both
		// skipped exactly on the stop→start path.
		startReq := inst.req
		startReq.Enabled = true
		hasSnell := reqHasSnellServer(startReq)
		if hasSnell {
			// Same hard prerequisites as Apply: no guard identity or
			// no nftables means Snell must not come up at all.
			uid, gid, err := ensureSnellGuardUser(context.Background())
			if err != nil {
				m.setErr(inst, err)
				return m.state(kernelType, false), err
			}
			if err := nftAvailable(); err != nil {
				err = fmt.Errorf("Snell 私网拦截需要 nftables，本机不可用: %v", err)
				m.setErr(inst, err)
				return m.state(kernelType, false), err
			}
			m.mu.Lock()
			m.snellUID, m.snellGID = uid, gid
			m.mu.Unlock()
		}
		m.mu.Lock()
		inst.req.Enabled = true
		guard := m.snellGuardForLocked(m.snellUID)
		m.mu.Unlock()
		m.stopProcesses(inst)
		// Sync before start, as in Apply (B-02, R8-021): a guarded
		// kernel (Snell egress guard or an inbound whitelist) must
		// never listen before its rules are installed — a preceding
		// stop stripped them from the table, and after a node reboot
		// the table is gone entirely — so the guard goes back in
		// after stopping and before starting, and a failed sync
		// leaves the kernel stopped instead of running unguarded.
		if err := m.nft.syncGuard(m.allPorts(), m.allUserMarks(), guard); err != nil {
			ferr := nftGuardFailure(inst.req, err)
			m.setErr(inst, ferr)
			return m.state(kernelType, false), ferr
		}
		m.startProcesses(inst)
		m.waitStable(kernelType)
		if m.state(kernelType, false).Running {
			m.mu.Lock()
			inst.goodHash = inst.req.Hash
			m.mu.Unlock()
		}
	default:
		return State{}, fmt.Errorf("未知操作: %s", action)
	}
	if action == "stop" {
		// Keep the rules in lockstep with the lifecycle: stopping a
		// kernel must leave none of its rules behind. A sync failure
		// here is surfaced, not swallowed.
		m.mu.Lock()
		guard := m.snellGuardForLocked(m.snellUID)
		m.mu.Unlock()
		if err := m.nft.syncGuard(m.allPorts(), m.allUserMarks(), guard); err != nil {
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

	out := Status{Kernels: make([]State, 0, len(types))}
	for _, t := range types {
		out.Kernels = append(out.Kernels, m.state(t, t == logsFor))
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
	out.Counters = counters
	out.Users = users
	out.Epoch = epoch
	return out
}

// Uninstall stops every kernel and removes the agent's nftables tables,
// whose counters, whitelists and port hopping would otherwise outlive
// the agent in the kernel. Used when the panel deletes this server and
// asks the agent to remove itself.
func (m *Manager) Uninstall() error {
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
	cmd := exec.Command(bin, args...)
	done := make(chan struct{})
	var out []byte
	go func() {
		out, _ = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	}
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
