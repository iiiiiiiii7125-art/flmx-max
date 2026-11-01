package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-gost/x/internal/util/download"
)

// GeoFile is a data file a kernel reads next to its binary (Xray's
// geosite.dat / geoip.dat, used by 分流). The kernel archive ships one; the
// agent keeps it current from URL, verified against ChecksumURL.
type GeoFile struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	ChecksumURL string `json:"checksumUrl"`
}

const (
	geoFirstCheck = 10 * time.Minute
	geoCheckEvery = 24 * time.Hour
	geoMaxSize    = 64 << 20
	geoTimeout    = 5 * time.Minute
)

func (g GeoFile) validate() error {
	if !safeName(g.Name) || !strings.HasSuffix(g.Name, ".dat") {
		return fmt.Errorf("非法数据文件名: %s", g.Name)
	}
	for _, u := range []string{g.URL, g.ChecksumURL} {
		// F-090: 只允许 https。http 明文通道下数据文件与校验文件同源
		// 同通道，SHA256 只能防传输损坏，中间人可同时替换两者，
		// 校验和不提供真实性（面板下发的 geo 地址均为 https，含
		// 镜像前缀形式；与内核二进制 digest 走可信通道的口径一致）。
		if !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("数据文件地址必须使用 https: %s", u)
		}
	}
	return nil
}

// startGeoUpdater checks the kernels' data files shortly after start and
// then daily (with jitter so nodes do not all fetch at once).
func (m *Manager) startGeoUpdater() {
	go func() {
		wait := geoFirstCheck
		for {
			time.Sleep(wait)
			ctx, cancel := context.WithTimeout(context.Background(), geoTimeout)
			m.UpdateGeoData(ctx)
			cancel()
			wait = geoCheckEvery + time.Duration(rand.Int63n(int64(time.Hour)))
		}
	}()
}

type geoJob struct {
	kernelType string
	binDir     string
	files      []GeoFile
}

// UpdateGeoData refreshes the data files of every kernel that has some and
// restarts the kernels whose files changed. A kernel that fails to start on
// the new files gets the old ones back.
func (m *Manager) UpdateGeoData(ctx context.Context) {
	m.mu.Lock()
	var jobs []geoJob
	for t, inst := range m.instances {
		if len(inst.req.GeoData) > 0 && inst.binDir != "" {
			jobs = append(jobs, geoJob{t, inst.binDir, inst.req.GeoData})
		}
	}
	m.mu.Unlock()

	for _, job := range jobs {
		staged, err := stageGeoFiles(ctx, job.binDir, job.files)
		if err != nil {
			m.logGeo(job.kernelType, "分流数据更新失败: "+err.Error())
		}
		if len(staged) > 0 {
			m.swapGeoFiles(job, staged)
		}
	}
}

// stageGeoFiles downloads the files that differ from the installed ones to
// "<name>.new" and returns their names. Files are kept as they are when the
// download or the checksum fails.
//
// R3-WDG-01: the per-file loop is the serial-budget ruling for the
// shared 5-minute round ctx — a file that fails (including a stall
// failed fast by fetch's no-progress watchdog, after its mirror
// fallback leg) is recorded and the round continues with the
// remaining files; one stalled file must not starve the whole
// round. The round ctx itself stays a single 5-minute budget.
func stageGeoFiles(ctx context.Context, binDir string, files []GeoFile) ([]string, error) {
	// KU4 (1.7.7): one probe plan per round, shared by every file
	// (probe target: the first file's checksum URL) — routes are
	// measured once per operation, not per file.
	var plan *download.Plan
	if len(files) > 0 {
		plan = download.NewPlan(ctx, directForm(files[0].ChecksumURL), checksumProbe, panelExtraRoutes()...)
	}
	var staged []string
	var errs []string
	for _, f := range files {
		changed, err := stageGeoFile(ctx, binDir, f, plan)
		if err != nil {
			errs = append(errs, f.Name+": "+err.Error())
			continue
		}
		if changed {
			staged = append(staged, f.Name)
		}
	}
	if len(errs) > 0 {
		return staged, errors.New(strings.Join(errs, "；"))
	}
	return staged, nil
}

func stageGeoFile(ctx context.Context, binDir string, f GeoFile, plan *download.Plan) (bool, error) {
	if plan == nil {
		plan = download.NewPlan(ctx, directForm(f.ChecksumURL), checksumProbe, panelExtraRoutes()...)
	}
	var sumBody strings.Builder
	// F-139 口径、KU4 (1.7.7) 形态：校验文件走本轮共享的测速计划
	// （替代原 fetchWithMirrorFallback 的镜像->直连双试）。
	if err := fetchRanked(ctx, plan, directForm(f.ChecksumURL), 4096, &sumBody); err != nil {
		return false, err
	}
	want, ok := parseChecksum(sumBody.String())
	if !ok {
		return false, errors.New("校验文件中没有 SHA256")
	}
	path := filepath.Join(binDir, f.Name)
	if have, err := fileSHA256(path); err == nil && have == want {
		return false, nil
	}

	// F-139 口径、KU4 形态 + 1.7.12 续传：数据文件（大文件流式）
	// 走同一计划与可续传 transfer——中途失败的线路保留已落盘
	// 字节、下一线路以 Range 续传。一条线路下完但组装哈希不符
	// 时丢弃断点（Reset），由下一线路从 0 重下，保住续传前的
	// 校验回退口径。
	tmp := path + ".new"
	transfer := download.NewFileTransfer(tmp, geoMaxSize, 0o644)
	_, err := download.TryPlan(plan, directForm(f.URL), func(route download.Route, u string) error {
		if err := fetchFileResume(ctx, transfer, u); err != nil {
			return err
		}
		if transfer.Sum() != want {
			_ = transfer.Reset()
			return errors.New("SHA256 校验失败")
		}
		return nil
	})
	if err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// swapGeoFiles moves the staged files in and restarts the kernel if it is
// running; on a failed start the previous files are restored.
func (m *Manager) swapGeoFiles(job geoJob, staged []string) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	m.mu.Lock()
	inst := m.instances[job.kernelType]
	current := inst != nil && inst.binDir == job.binDir
	m.mu.Unlock()
	if !current {
		// Reinstalled meanwhile: the new version brought its own files.
		for _, name := range staged {
			_ = os.Remove(filepath.Join(job.binDir, name+".new"))
		}
		return
	}

	var swapped []string
	hadOrig := map[string]bool{}
	for _, name := range staged {
		path := filepath.Join(job.binDir, name)
		_ = os.Remove(path + ".bak")
		if _, err := os.Stat(path); err == nil {
			hadOrig[name] = true
			if err := os.Rename(path, path+".bak"); err != nil {
				_ = os.Remove(path + ".new")
				continue
			}
		}
		if err := os.Rename(path+".new", path); err != nil {
			if hadOrig[name] {
				_ = os.Rename(path+".bak", path)
			}
			continue
		}
		swapped = append(swapped, name)
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
		// F-032 变体：与 Apply 同口径的持续存活探活，不再单点快照。
		if !m.waitStable(job.kernelType) {
			for _, name := range swapped {
				path := filepath.Join(job.binDir, name)
				if hadOrig[name] {
					_ = os.Rename(path+".bak", path)
				} else {
					// 原文件此前不存在时没有 .bak 可恢复：删掉新文件，
					// 否则坏文件会被静默留下（旧实现此路径失效）。
					_ = os.Remove(path)
				}
			}
			m.stopProcesses(inst)
			m.startProcesses(inst)
			m.logGeo(job.kernelType, "新的分流数据无法启动内核，已恢复原来的文件")
			return
		}
	}
	for _, name := range swapped {
		_ = os.Remove(filepath.Join(job.binDir, name+".bak"))
	}
	m.logGeo(job.kernelType, "分流数据已更新: "+strings.Join(swapped, ", "))
}

func (m *Manager) logGeo(kernelType, msg string) {
	m.mu.Lock()
	inst := m.instances[kernelType]
	m.mu.Unlock()
	if inst != nil {
		inst.logs.add("[flmx] " + msg)
	}
}
