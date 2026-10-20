package kernel

import (
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
	if err := install(d, dir); err != nil {
		return "", fmt.Errorf("安装 %s 失败: %v", label, err)
	}
	inst.logs.add(fmt.Sprintf("[flmx] %s 安装完成", label))
	return path, nil
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
	prevFiles := readFiles(runDir, req.Files)
	for _, f := range req.Files {
		if err := writeFileAtomic(filepath.Join(runDir, f.Name), []byte(f.Content), 0o600); err != nil {
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

	stable := true
	switch {
	case !req.Enabled:
		m.stopProcesses(inst)
	case changed || !running:
		m.stopProcesses(inst)
		m.startProcesses(inst)
		// F-032: 探活窗口内持续存活判定——窗口内曾存活又退出即失败，
		// 单点快照会把"启动后随即退出"误判成功、把慢启动误判失败。
		stable = m.waitStable(req.Type)
	}

	var failure error
	confirmed := true
	if req.Enabled && (!stable || !m.state(req.Type, false).Running) {
		failed := lastLines(inst.logs, 5)
		failure = fmt.Errorf("内核启动失败: %s", failed)
		confirmed = false
		if prevGood {
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

	if confirmed {
		m.persist()
	} else {
		// F-032: 未确认可用的坏配置不得 persist 固化，否则 agent
		// 重启 Restore 会反复拉起坏配置；保留上一次的持久状态。
		inst.logs.add("[flmx] 新配置未确认可用，未写入持久状态；重启后将恢复上一次的配置")
	}
	if err := m.nft.sync(m.allPorts(), m.allUserMarks()); err != nil {
		inst.logs.add("[flmx] 流量计数规则同步失败: " + err.Error())
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
		if err := m.nft.sync(m.allPorts(), m.allUserMarks()); err != nil {
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
		m.stopProcesses(inst)
		m.mu.Lock()
		inst.req.Enabled = true
		m.mu.Unlock()
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
