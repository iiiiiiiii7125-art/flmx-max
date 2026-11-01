package kernel

// FN34 (round-5 L7-N34): sing-box 分流 rule sets used to be downloaded
// by the sing-box process itself, straight from raw.githubusercontent.com
// — the one GitHub download in the product that bypassed the 1.7.7
// caliber (built-in candidates, probe-selected route, direct-first
// short-circuit). On a node whose direct route to GitHub is dead, the
// download failed and a fresh split-routing config could not start.
//
// The agent now owns the download, exactly like Xray geodata: at Apply
// time the remote rule-set definitions in the rendered config are
// fetched through a download.Plan (probe = the first set's own SRS
// content; candidates = direct + built-in mirrors + the panel-source
// route), validated as real SRS binaries, written next to the config,
// and the definitions are rewritten to sing-box "local" rule sets
// pointing at those files. A periodic updater (same cadence and round
// budget as the geodata updater) refreshes the files afterwards, so
// freshness no longer depends on how often the panel re-applies.
//
// Failure caliber:
//   - a rule set with no usable local copy whose download fails fails
//     the Apply BEFORE any file is written — the running kernel keeps
//     its previous config untouched;
//   - a failed refresh with a usable local copy keeps the old copy
//     (the local equivalent of sing-box falling back to its cache)
//     and only logs;
//   - downloaded content is never trusted on a bare 200: it must
//     carry the SRS magic and a fully decodable zlib payload (the
//     on-disk format of SagerNet's published .srs files), so a
//     mirror's HTML landing page can never pose as a rule set.

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-gost/x/internal/util/download"
)

const (
	// ruleSetDirName holds the localized rule-set files inside the
	// kernel's run directory.
	ruleSetDirName = "rule-sets"
	// ruleSetMaxSize caps one rule-set download (the largest
	// published SagerNet sets are a few MB; the cap only exists so
	// a broken route cannot stream without bound).
	ruleSetMaxSize = 64 << 20
)

// ruleSetApplyTimeout bounds the whole rule-set fetch round of one
// Apply (all sets share it, serial-budget caliber like the geodata
// round). It stays well inside the margin the panel's KernelApply
// patience (300s) leaves next to a binary install (240s budget,
// A001): the common config-only apply has the binary already
// installed and finishes its downloads in seconds on any admitted
// route. A var so tests can shrink it.
var ruleSetApplyTimeout = 2 * time.Minute

// remoteRuleSet is one remote rule-set definition found in a rendered
// sing-box config.
type remoteRuleSet struct {
	tag       string
	directURL string // mirror prefix the panel may have attached stripped
	def       map[string]interface{}
}

// ruleSetFileName is the deterministic local file name of a set: the
// sanitized tag plus a short hash of its source URL, so two sets can
// never collide and a set's file is stable across applies.
func ruleSetFileName(tag, directURL string) string {
	var b strings.Builder
	for _, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), ".")
	if name == "" {
		name = "set"
	}
	if len(name) > 48 {
		name = name[:48]
	}
	sum := sha256.Sum256([]byte(directURL))
	return name + "-" + hex.EncodeToString(sum[:])[:8] + ".srs"
}

func (s *remoteRuleSet) localPath(dir string) string {
	return filepath.Join(dir, ruleSetFileName(s.tag, s.directURL))
}

// extractRemoteRuleSets parses a rendered sing-box config and returns
// its root object plus every remote, binary rule-set definition in it
// (route.rule_set and, on newer sing-box, the top-level rule_set).
// Definitions of any other shape (local, inline, source format) are
// left alone: the panel only ever renders remote binary ones.
func extractRemoteRuleSets(content string) (map[string]interface{}, []*remoteRuleSet, error) {
	var root map[string]interface{}
	if err := json.Unmarshal([]byte(content), &root); err != nil {
		return nil, nil, err
	}
	var sets []*remoteRuleSet
	collect := func(holder map[string]interface{}) {
		defs, _ := holder["rule_set"].([]interface{})
		for _, raw := range defs {
			def, _ := raw.(map[string]interface{})
			if def == nil {
				continue
			}
			if def["type"] != "remote" || def["format"] != "binary" {
				continue
			}
			tag, _ := def["tag"].(string)
			rawURL, _ := def["url"].(string)
			if tag == "" || rawURL == "" {
				continue
			}
			sets = append(sets, &remoteRuleSet{tag: tag, directURL: directForm(rawURL), def: def})
		}
	}
	collect(root)
	if route, _ := root["route"].(map[string]interface{}); route != nil {
		collect(route)
	}
	return root, sets, nil
}

// validateSRSBytes checks that b is a real sing-box binary rule set:
// the published .srs layout is the magic "SRS", one version byte,
// then a zlib stream holding the compiled rules. The stream is
// decoded to its end, so truncated bodies and HTML error pages both
// fail — a bare HTTP 200 never counts as success.
func validateSRSBytes(b []byte) error {
	if len(b) < 8 {
		return errors.New("规则集文件过小")
	}
	if string(b[:3]) != "SRS" {
		return errors.New("规则集魔数不符，不是 SRS 文件")
	}
	if v := b[3]; v < 1 || v > 16 {
		return fmt.Errorf("规则集格式版本异常: %d", v)
	}
	zr, err := zlib.NewReader(bytes.NewReader(b[4:]))
	if err != nil {
		return fmt.Errorf("规则集压缩流无效: %v", err)
	}
	defer zr.Close()
	n, err := io.Copy(io.Discard, zr)
	if err != nil {
		return fmt.Errorf("规则集内容损坏: %v", err)
	}
	if n == 0 {
		return errors.New("规则集内容为空")
	}
	return nil
}

func validSRSFile(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && validateSRSBytes(b) == nil
}

// ruleSetProbe admits a route into the plan only when it answers the
// rule-set URL itself with the start of a real SRS file (assetProbe
// caliber — these downloads have no small companion file to probe
// with). Only the first bytes are read, so the probe cost stays
// bounded by the plan's probe context.
func ruleSetProbe(ctx context.Context, url string) error {
	if err := validateFetchEntryURL(ctx, url); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := fetchHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败: HTTP %d (%s)", resp.StatusCode, url)
	}
	head, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		return err
	}
	if len(head) < 6 || string(head[:3]) != "SRS" || head[3] < 1 || head[3] > 16 {
		return errors.New("规则集探针内容不是 SRS 文件")
	}
	zr, err := zlib.NewReader(bytes.NewReader(head[4:]))
	if err != nil {
		return fmt.Errorf("规则集探针压缩流无效: %v", err)
	}
	_ = zr.Close()
	return nil
}

// fetchRuleSet downloads one rule set over the plan. Content
// validation runs INSIDE the attempt: a route that delivers anything
// but a valid SRS file fails the leg and the plan moves on, instead
// of poisoning the whole operation after the fact.
func fetchRuleSet(ctx context.Context, plan *download.Plan, directURL string) ([]byte, error) {
	var out []byte
	_, err := download.TryPlan(plan, directURL, func(route download.Route, url string) error {
		var buf bytes.Buffer
		if err := fetch(ctx, url, ruleSetMaxSize, &buf); err != nil {
			return err
		}
		if err := validateSRSBytes(buf.Bytes()); err != nil {
			return err
		}
		out = buf.Bytes()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// localizeSingboxRuleSets prepares the files of a sing-box Apply:
// every remote rule set of the rendered config is downloaded (or a
// usable local copy kept) and its definition rewritten to a local
// one. It returns the file list to write — the input list with only
// config.json's content replaced — or an error, in which case Apply
// must write nothing. See the package-level caliber note above.
func (m *Manager) localizeSingboxRuleSets(inst *instance, req ApplyRequest, runDir string) ([]File, error) {
	idx := -1
	for i, f := range req.Files {
		if f.Name == "config.json" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return req.Files, nil
	}
	content := req.Files[idx].Content
	root, sets, err := extractRemoteRuleSets(content)
	if err != nil {
		// A config that mentions rule sets but does not parse must
		// not slip through unlocalized; anything else is not this
		// hook's business (sing-box will judge it as before).
		if strings.Contains(content, `"rule_set"`) {
			return nil, fmt.Errorf("解析 sing-box 配置失败: %v", err)
		}
		return req.Files, nil
	}
	if len(sets) == 0 {
		return req.Files, nil
	}
	dir := filepath.Join(runDir, ruleSetDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	m.mu.Lock()
	prevHash := inst.req.Hash
	m.mu.Unlock()
	unchanged := prevHash != "" && prevHash == req.Hash

	// The probe plan (and its round context) is built lazily: an
	// apply whose sets are all on disk already pays no network.
	var plan *download.Plan
	var fetchCtx context.Context
	var cancel context.CancelFunc
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()
	ensurePlan := func() *download.Plan {
		if plan == nil {
			fetchCtx, cancel = context.WithTimeout(context.Background(), ruleSetApplyTimeout)
			plan = download.NewPlan(fetchCtx, sets[0].directURL, ruleSetProbe, panelExtraRoutes()...)
		}
		return plan
	}

	var failures []string
	for _, s := range sets {
		path := s.localPath(dir)
		if unchanged && validSRSFile(path) {
			continue // same config already localized on disk
		}
		p := ensurePlan()
		data, ferr := fetchRuleSet(fetchCtx, p, s.directURL)
		if ferr != nil {
			if validSRSFile(path) {
				inst.logs.add(fmt.Sprintf("[flmx] 规则集 %s 刷新失败，沿用本地副本: %v", s.tag, ferr))
				continue
			}
			failures = append(failures, s.tag+": "+ferr.Error())
			continue
		}
		if have, herr := fileSHA256(path); herr != nil || have != bytesSHA256(data) {
			if werr := writeFileAtomic(path, data, 0o644); werr != nil {
				failures = append(failures, s.tag+": "+werr.Error())
			}
		}
	}
	if len(failures) > 0 {
		return nil, fmt.Errorf("分流规则集下载失败（%s）", strings.Join(failures, "；"))
	}

	for _, s := range sets {
		// Rewrite in place: the definition object becomes a local
		// rule set; url / http_client / update knobs of the remote
		// form do not carry over.
		for k := range s.def {
			delete(s.def, k)
		}
		s.def["type"] = "local"
		s.def["tag"] = s.tag
		s.def["format"] = "binary"
		s.def["path"] = s.localPath(dir)
	}
	rewritten, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("重写 sing-box 配置失败: %v", err)
	}
	files := make([]File, len(req.Files))
	copy(files, req.Files)
	files[idx].Content = string(rewritten)
	return files, nil
}

func bytesSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- periodic refresh (geodata updater's twin) ---

// startRuleSetUpdater mirrors startGeoUpdater: a first check shortly
// after start, then a daily one with jitter. Localized rule sets
// would otherwise only refresh when the panel re-applies a config,
// while the remote form refreshed itself on sing-box's own schedule.
func (m *Manager) startRuleSetUpdater() {
	go func() {
		wait := geoFirstCheck
		for {
			time.Sleep(wait)
			ctx, cancel := context.WithTimeout(context.Background(), geoTimeout)
			m.UpdateRuleSets(ctx)
			cancel()
			wait = geoCheckEvery + time.Duration(rand.Int63n(int64(time.Hour)))
		}
	}()
}

type ruleSetJob struct {
	kernelType string
	configHash string
	dir        string
	sets       []*remoteRuleSet
}

// UpdateRuleSets refreshes the localized rule-set files of every
// sing-box instance that has some, restarting a kernel whose files
// changed (and restoring the old files when the new ones do not
// start), exactly the geodata updater's contract.
func (m *Manager) UpdateRuleSets(ctx context.Context) {
	m.mu.Lock()
	var jobs []ruleSetJob
	for t, inst := range m.instances {
		if t != "singbox" {
			continue
		}
		var content string
		for _, f := range inst.req.Files {
			if f.Name == "config.json" {
				content = f.Content
				break
			}
		}
		if content == "" {
			continue
		}
		if _, sets, err := extractRemoteRuleSets(content); err == nil && len(sets) > 0 {
			jobs = append(jobs, ruleSetJob{t, inst.req.Hash, filepath.Join(m.runDir(t), ruleSetDirName), sets})
		}
	}
	m.mu.Unlock()

	for _, job := range jobs {
		staged, err := stageRuleSets(ctx, job)
		if err != nil {
			m.logGeo(job.kernelType, "分流规则集更新失败: "+err.Error())
		}
		if len(staged) > 0 {
			m.swapRuleSets(job, staged)
		}
	}
}

// stageRuleSets downloads the sets that differ from the installed
// ones to "<name>.new" and returns them. One probe plan per round,
// shared by every set; a set that fails is recorded and the round
// continues with the remaining ones (the geodata serial-budget
// ruling).
func stageRuleSets(ctx context.Context, job ruleSetJob) ([]*remoteRuleSet, error) {
	if err := os.MkdirAll(job.dir, 0o700); err != nil {
		return nil, err
	}
	plan := download.NewPlan(ctx, job.sets[0].directURL, ruleSetProbe, panelExtraRoutes()...)
	var staged []*remoteRuleSet
	var errs []string
	for _, s := range job.sets {
		data, err := fetchRuleSet(ctx, plan, s.directURL)
		if err != nil {
			errs = append(errs, s.tag+": "+err.Error())
			continue
		}
		path := s.localPath(job.dir)
		if have, herr := fileSHA256(path); herr == nil && have == bytesSHA256(data) {
			continue
		}
		if err := writeFileAtomic(path+".new", data, 0o644); err != nil {
			errs = append(errs, s.tag+": "+err.Error())
			continue
		}
		staged = append(staged, s)
	}
	if len(errs) > 0 {
		return staged, errors.New(strings.Join(errs, "；"))
	}
	return staged, nil
}

// swapRuleSets moves staged rule-set files in and restarts the
// kernel; on a failed start the previous files are restored.
func (m *Manager) swapRuleSets(job ruleSetJob, staged []*remoteRuleSet) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	m.mu.Lock()
	inst := m.instances[job.kernelType]
	current := inst != nil && inst.req.Hash == job.configHash
	m.mu.Unlock()
	if !current {
		// Re-applied meanwhile: the newer config's apply fetched
		// its own copies.
		for _, s := range staged {
			_ = os.Remove(s.localPath(job.dir) + ".new")
		}
		return
	}

	var swapped []*remoteRuleSet
	hadOrig := map[string]bool{}
	for _, s := range staged {
		path := s.localPath(job.dir)
		_ = os.Remove(path + ".bak")
		if _, err := os.Stat(path); err == nil {
			hadOrig[s.tag] = true
			if err := os.Rename(path, path+".bak"); err != nil {
				_ = os.Remove(path + ".new")
				continue
			}
		}
		if err := os.Rename(path+".new", path); err != nil {
			if hadOrig[s.tag] {
				_ = os.Rename(path+".bak", path)
			}
			continue
		}
		swapped = append(swapped, s)
	}
	if len(swapped) == 0 {
		return
	}

	m.mu.Lock()
	enabled := inst.req.Enabled
	m.mu.Unlock()
	if enabled {
		m.stopProcesses(inst)
		m.startProcesses(inst)
		if !m.waitStable(job.kernelType) {
			for _, s := range swapped {
				path := s.localPath(job.dir)
				if hadOrig[s.tag] {
					_ = os.Rename(path+".bak", path)
				} else {
					_ = os.Remove(path)
				}
			}
			m.stopProcesses(inst)
			m.startProcesses(inst)
			m.logGeo(job.kernelType, "新的分流规则集无法启动内核，已恢复原来的文件")
			return
		}
	}
	var tags []string
	for _, s := range swapped {
		_ = os.Remove(s.localPath(job.dir) + ".bak")
		tags = append(tags, s.tag)
	}
	m.logGeo(job.kernelType, "分流规则集已更新: "+strings.Join(tags, ", "))
}
