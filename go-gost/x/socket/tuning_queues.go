package socket

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Deep optimization (upstream 3.3.43 tuning_queues.go, deep-scan half,
// batch 2): capture the egress queues, scan for the policer knee via
// the vendored tcpfit engine, and persist an HTB/fq shaper when the
// knee is reliable. The restore half of that upstream file —
// restoreTuneQueues, capture/restoreTuneShapeFiles,
// recoverTuneTransaction, restoreAutomaticTuning — lives in
// tuning_restore.go (batch 1); the two files do not overlap.
//
// FLMX notes vs upstream (flyx): the systemd unit is
// flmx-tuning-qdisc.service; the FLYX_SWEEP_ wire markers emitted by
// the vendored engine are kept verbatim. The backup path stays
// /etc/flux_agent/tuning-backup.json (legacy name, unchanged by
// decision); shape files live under the engine directory next to it.

func tuneShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// tc's display forms ("limit 10240p", "quantum 3028b", pfifo_fast's priomap)
// are not accepted back as options, so only the queue kinds and their
// placement are replayed; the kernel supplies the defaults. Refuse external
// classful shapers and filters instead of destroying their topology.
func tuneQueueReplay(iface, out string) (string, error) {
	var lines [][]string
	root := ""
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[0] != "qdisc" {
			continue
		}
		if f[1] == "ingress" || f[1] == "clsact" {
			continue
		}
		if f[3] == "root" {
			root = f[1]
		}
		switch f[1] {
		case "mq", "fq", "fq_codel", "pfifo_fast", "noqueue":
		default:
			return "", fmt.Errorf("网卡 %s 使用外部 %s 队列，无法保证完整恢复，已停止深度优化", iface, f[1])
		}
		lines = append(lines, f)
	}
	if root == "" {
		return "", fmt.Errorf("无法读取网卡原始队列")
	}
	script := "#!/bin/sh\nset -eu\n# Original queues, captured before FLMX deep tuning.\ntc qdisc del dev " + tuneShellQuote(iface) + " root 2>/dev/null || true\n"
	// Hardware mq roots with handle 0 are recreated by the kernel on delete.
	// Query the resulting major handle so leaves bind to the actual queues.
	if root == "mq" {
		script += "mqmajor=$(tc qdisc show dev " + tuneShellQuote(iface) + " | awk '$1==\"qdisc\" && $2==\"mq\" {for(i=1;i<=NF;i++) if($i==\"root\") {sub(/:$/,\"\",$3); print $3; exit}}')\n"
		// D2 (PORT-3343-BATCH2-D1D2-FIX.md; verify report
		// ~/workspace/port-3343/PORT-3343-BATCH2-VERIFY-REPORT.md §4.2):
		// the kernel recreates a deleted mq root with handle 0, so
		// the probe above reports mqmajor=0 — non-empty, so the
		// upstream `-z` test alone skipped the repair and the leaf
		// lines bound to `parent 0:N`, which real tc rejects
		// ("Failed to find specified qdisc."). Treat 0 like "no
		// handle": replace the root with handle 1: mq first, then
		// bind the leaves to 1:N. Deliberate FLMX deviation from
		// upstream tuning_queues.go:49-50 (same tcpfit
		// qdisc_set_mq_leaves handling as vendored tcpfit.sh:1983
		// and :2008); vendored tcpfit.sh itself is untouched. The
		// only other mqmajor consumer in this file (the leaf-parent
		// rewrite below) needs no change: after this branch mqmajor
		// is 1 or the kernel's actual non-zero handle.
		script += "if [ -z \"$mqmajor\" ] || [ \"$mqmajor\" = 0 ]; then tc qdisc replace dev " + tuneShellQuote(iface) + " root handle 1: mq; mqmajor=1; fi\n"
	}
	for _, f := range lines {
		if f[1] == "noqueue" || f[1] == "mq" {
			continue
		}
		args := []string{"tc", "qdisc", "replace", "dev", iface}
		for i := 3; i < len(f); i++ {
			if f[i] == "refcnt" {
				i++
				continue
			}
			if f[i] == "root" || f[i] == "parent" {
				args = append(args, f[i])
				if f[i] == "parent" && i+1 < len(f) {
					i++
					args = append(args, f[i])
				}
				break
			}
		}
		if f[2] != "0:" {
			args = append(args, "handle", f[2])
		}
		args = append(args, f[1])
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = tuneShellQuote(a)
			if root == "mq" && i > 0 && args[i-1] == "parent" {
				_, minor, ok := strings.Cut(a, ":")
				if !ok {
					return "", fmt.Errorf("无法解析 mq 叶子队列")
				}
				quoted[i] = "\"${mqmajor}:\"" + tuneShellQuote(minor)
			}
		}
		script += strings.Join(quoted, " ") + "\n"
	}
	return script, nil
}

func captureTuneQueues(ctx context.Context, d *tuneDetection) error {
	iface, err := tuneInterface(ctx, tunePeer{host: d.Peer, port: d.Port, family: d.Family})
	if err != nil {
		return err
	}
	if iface != d.Interface {
		return fmt.Errorf("测速出口已变化，请重新执行自动调优")
	}
	ifacePath := filepath.Join(tuneEngineDir(), "queue-interface")
	if old, e := os.ReadFile(ifacePath); e == nil && string(old) != iface {
		return fmt.Errorf("出口已变化，请先恢复原配置，再对新出口调优")
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	filters, err := tuneCommand(ctx, "tc", "filter", "show", "dev", iface, "root")
	if err != nil {
		return err
	}
	if strings.TrimSpace(filters) != "" {
		return fmt.Errorf("出口存在自定义流量分类，已停止深度优化以保留原配置")
	}
	out, err := tuneCommand(ctx, "tc", "qdisc", "show", "dev", iface)
	if err != nil {
		return err
	}
	var replay string
	if strings.Contains(out, "qdisc htb ") && d.ShapeMbps > 0 {
		b, e := os.ReadFile(filepath.Join(tuneEngineDir(), "shape.sh"))
		if e != nil {
			return fmt.Errorf("找不到原整形配置，已停止深度优化")
		}
		replay = string(b)
	} else {
		replay, err = tuneQueueReplay(iface, out)
		if err != nil {
			return err
		}
	}
	if err = writeFileAtomic(filepath.Join(tuneEngineDir(), "current-qdisc.sh"), []byte(replay), 0o700); err != nil {
		return err
	}
	original := filepath.Join(tuneEngineDir(), "original-qdisc.sh")
	if _, err = os.Stat(original); os.IsNotExist(err) {
		if err = writeFileAtomic(ifacePath, []byte(iface), 0o600); err != nil {
			return err
		}
		return writeFileAtomic(original, []byte(replay), 0o700)
	} else if err != nil {
		return err
	}
	return nil
}

var tuneReadTraffic = tuneTrafficBytes

// tuneTrafficBaseline is how long the interface's own traffic is sampled
// before a scan, so forwarding traffic is not charged to the scan budget.
var tuneTrafficBaseline = 5 * time.Second

func tuneTrafficBytes(iface string) (uint64, error) {
	var total uint64
	for _, name := range []string{"rx_bytes", "tx_bytes"} {
		b, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "statistics", name))
		if err != nil {
			return 0, err
		}
		n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// tuneDeepManualKey carries an operator-supplied shaper rate (Mbps)
// for a deep job through the job context: absent/0 means "scan for
// the knee with the engine" (the default); > 0 means "skip the sweep
// and persist this rate, then run the same post-shape verification
// as a scanned knee". It is request-scoped (set once by
// startTuneJob for the deep job), never persisted into
// tuning-result.json, and never read by the auto/restore paths.
// The value itself is the operator's own measurement (e.g. a
// multi-stream sweep done outside the panel); the agent verifies it
// with the same single/parallel post-shape checks as a scanned
// knee, and restoreAutomaticTuning still undoes it.
type tuneDeepManualKey struct{}

func tuneDeepManualMbps(ctx context.Context) int {
	if v, ok := ctx.Value(tuneDeepManualKey{}).(int); ok {
		return v
	}
	return 0
}

func executeDeepTuning(ctx context.Context, d *tuneDetection, maxGB float64) (err error) {
	if d == nil {
		return fmt.Errorf("请先完成自动调优")
	}
	// F-05 (PORT-REVIEW-L2-E01): on links whose policer knee sits
	// above what the engine's single-stream unshaped probe can reach,
	// the sweep always reports NO_KNEE and persistTuneShape is never
	// reached. A manual rate skips the sweep and drives the same
	// persist + post-shape verification below; the sweep path is
	// unchanged.
	manualRate := tuneDeepManualMbps(ctx)
	if manualRate < 0 || manualRate > 100000 {
		return fmt.Errorf("手动整形速率需在 1–100000 Mbps")
	}
	if err = captureTuneQueues(ctx, d); err != nil {
		return err
	}
	committed := false
	// keepSummary saves d's summary again after the files are rolled back.
	keepSummary := false
	files, err := captureTuneShapeFiles()
	if err != nil {
		return err
	}
	if err = saveTuneJSON(tuneStatePath("tuning-transaction.json"), files); err != nil {
		return err
	}
	defer func() {
		recovered := true
		if !committed {
			if e := restoreTuneShapeFiles(files); e != nil {
				recovered = false
				err = fmt.Errorf("%v；整形持久配置恢复失败：%w", err, e)
			}
			if e := restoreTuneQueues("current-qdisc.sh"); e != nil {
				recovered = false
				err = fmt.Errorf("%v；原队列恢复失败：%w", err, e)
			}
			if recovered && keepSummary {
				if e := saveTuneJSON(tuneStatePath("tuning-result.json"), d); e != nil {
					err = fmt.Errorf("%v；保存扫描结果失败：%w", err, e)
				}
			}
		}
		if recovered {
			if e := os.Remove(tuneStatePath("tuning-transaction.json")); e != nil && !os.IsNotExist(e) {
				err = fmt.Errorf("清理恢复记录失败：%w", e)
			}
		}
	}()
	tuneProgress("scanning", 32, "测量出口现有流量，避免把转发流量计入测速预算")
	start, err := tuneReadTraffic(d.Interface)
	if err != nil {
		return err
	}
	var background float64 // bytes per second
	if tuneTrafficBaseline > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(tuneTrafficBaseline):
		}
		n, e := tuneReadTraffic(d.Interface)
		if e != nil {
			return e
		}
		if n >= start {
			background = float64(n-start) / tuneTrafficBaseline.Seconds()
		}
		start = n
	}
	began := time.Now()
	scanCtx, cancel := context.WithCancelCause(ctx)
	trafficDone := make(chan struct{})
	defer func() { cancel(nil); <-trafficDone }()
	go func() {
		defer close(trafficDone)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-scanCtx.Done():
				return
			case <-ticker.C:
				n, e := tuneReadTraffic(d.Interface)
				if e != nil {
					cancel(fmt.Errorf("无法计量测速流量，已停止深度优化"))
					return
				}
				used := float64(n-start) - background*time.Since(began).Seconds()
				if n >= start && used > maxGB*1024*1024*1024 {
					cancel(fmt.Errorf("已达到本次 %.1f GB 流量预算，已停止扫描并恢复原队列", maxGB))
					return
				}
			}
		}
	}()
	// The traffic meter above still guards the manual path
	// (persist + two 10 s verification samples).
	var rate, knee int
	if manualRate > 0 {
		rate = manualRate
		tuneProgress("shaping", 75, fmt.Sprintf("按手动整形值 %d Mbps 应用出向整形并验证（跳过拐点扫描）", rate))
	} else {
		tuneProgress("scanning", 35, fmt.Sprintf("扫描限速器拐点，流量预算 %.1f GB；可能需要数分钟", maxGB))
		out, scanErr := callTuneEngine(scanCtx, "sweep", d, strconv.Itoa(d.BandwidthMbps), strconv.FormatFloat(maxGB, 'f', 1, 64))
		if scanCtx.Err() != nil {
			return context.Cause(scanCtx)
		}
		values := map[string]string{}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "FLYX_SWEEP_") {
				kv := strings.SplitN(strings.TrimPrefix(line, "FLYX_SWEEP_"), "=", 2)
				if len(kv) == 2 {
					values[kv[0]] = kv[1]
				}
			}
		}
		rate, _ = strconv.Atoi(values["RECOMMEND"])
		knee, _ = strconv.Atoi(values["KNEE"])
		if values["INCONCLUSIVE"] == "1" || values["OUT_OF_RANGE"] == "1" || values["CANCELLED"] == "1" || values["ABOVE_CAP"] != "" {
			d.DeepSummary = "本次未能可靠判断限速器拐点，保留原整形配置"
			keepSummary = true
			return fmt.Errorf("%s；可稍后重试，详情见任务日志", d.DeepSummary)
		}
		if values["NO_KNEE"] == "1" {
			// F-05 (c): say what the probe actually proved — the
			// single stream reached this rate with no loss jump —
			// and point at the manual path, instead of reporting
			// "no policer" when the knee may sit above the
			// single-stream ceiling.
			if unshaped := values["UNSHAPED"]; unshaped != "" {
				d.DeepSummary = fmt.Sprintf("单流不限速探测达 %s Mbps 且无丢包跳变，未发现明确限速器拐点；拐点可能高于单流探测上限，可填入实测拐点手动应用整形，保留原队列与整形配置", unshaped)
			} else {
				d.DeepSummary = "本次未发现明确限速器拐点，保留原队列与整形配置"
			}
			if err = restoreTuneQueues("current-qdisc.sh"); err != nil {
				return err
			}
			if err = saveTuneJSON(tuneStatePath("tuning-result.json"), d); err != nil {
				return err
			}
			committed = true
			return nil
		}
		if scanErr != nil || rate <= 0 || knee <= 0 || rate > knee || rate > 100000 {
			return fmt.Errorf("拐点扫描未取得可靠结果，未更改整形配置：%v", scanErr)
		}
		tuneProgress("shaping", 75, fmt.Sprintf("已定位拐点 %d Mbps，应用 %d Mbps 出向整形并验证", knee, rate))
	}
	if err = persistTuneShape(scanCtx, d, rate); err != nil {
		return err
	}
	// Verify before committing the new shaper. Failure restores its predecessor.
	d.Single, err = tuneMeasure(scanCtx, d, 1)
	if err == nil {
		d.Parallel, err = tuneMeasure(scanCtx, d, 4)
	}
	if err != nil {
		return fmt.Errorf("整形后验证失败，已回退原队列：%w", err)
	}
	baseline := float64(d.BandwidthMbps)
	if d.Before != nil {
		baseline = d.Before.Mbps
	}
	if d.Parallel.Mbps < math.Min(float64(rate), baseline)*0.7 || d.Parallel.LossPercent > 1 {
		return fmt.Errorf("整形后验证吞吐偏低或重传偏高，已回退原队列；可在对端空闲时重试")
	}
	d.ShapeMbps = rate
	if manualRate > 0 {
		// No knee was measured on this run; do not report one.
		d.DeepSummary = fmt.Sprintf("手动整形 %d Mbps（未扫描拐点，整形后验证通过）", rate)
	} else {
		d.DeepSummary = fmt.Sprintf("拐点 %d Mbps，出向整形 %d Mbps", knee, rate)
	}
	if err = saveTuneJSON(tuneStatePath("tuning-result.json"), d); err != nil {
		return err
	}
	committed = true
	return nil
}

// D4 (PORT-3343-BATCH2-D4-FIX.md): the root line used to be
// `tc qdisc replace ... root handle 1: htb`. The kernel refuses
// replace on a live HTB root that already has children, and
// persistTuneShape runs this script right after the engine installed
// exactly such a root, so every deep run that found a knee failed at
// the persist step. Delete the root first — tolerating a missing or
// kernel-default root, which plain del cannot remove — and add it
// back: the same del+add sequence apply_test_shaper already uses.
// The class/leaf lines stay `replace`: replace is add-or-replace and
// works whether or not the objects exist yet. Deliberate FLMX
// deviation from upstream tuneShapeScript (upstream :405).
//
// D4F (PORT-REVIEW-L1-D4F): that unconditional `del` turned a
// fail-closed behaviour into a destructive one — the old `replace`
// failed (rc=2) on a foreign HTB root and left it alone, while the
// D4 script deletes whatever root is in place. shape.sh is also run
// by two paths with no Go-side guard (the boot systemd unit and the
// container-start re-issue in recoverAutomaticTuningOnStart), so the
// guard lives in the script itself and covers all three paths:
//   - kernel-default roots (handle 0, leaf-only kinds: mq/noqueue/
//     pfifo_fast/pfifo/fq/fq_codel) carry no shaping topology and
//     may be deleted;
//   - an HTB root is only deleted when it is this tuning's own
//     shaper — handle 1: with class 1:10 and the fq leaf parent
//     1:10, exactly what apply_test_shaper installs;
//   - anything else (cake/tbf/foreign HTB/...) is kept and the
//     script exits 1, so the caller fails closed instead of
//     destroying the foreign configuration.
func tuneShapeScript(iface string, rate int) string {
	q := tuneShellQuote(iface)
	r := strconv.Itoa(rate) + "mbit"
	burst := rate * 500
	if burst < 32768 {
		burst = 32768
	}
	guard := "# FLMX 整形护栏（D4F）：只删内核默认根或本调优装上的 HTB 根，外来整形保留并报错。\n" +
		"show=$(tc qdisc show dev " + q + ")\n" +
		"rootinfo=$(printf '%s\\n' \"$show\" | awk '$1==\"qdisc\" && $4==\"root\" {sub(/:$/,\"\",$3); print $2, $3; exit}')\n" +
		"rootkind=${rootinfo%% *}\n" +
		"roothandle=${rootinfo##* }\n" +
		"case \"$rootkind\" in\n" +
		"  \"\") ;;\n" +
		"  mq|noqueue|pfifo_fast|pfifo|fq|fq_codel)\n" +
		"    if [ \"$roothandle\" != \"0\" ]; then echo \"出口已有非默认的 $rootkind 整形（handle $roothandle），已保留原配置\" >&2; exit 1; fi\n" +
		"    ;;\n" +
		"  htb)\n" +
		"    if [ \"$roothandle\" != \"1\" ]; then echo \"出口已有非 FLMX 的 HTB 整形（handle $roothandle），已保留原配置\" >&2; exit 1; fi\n" +
		"    classes=$(tc class show dev " + q + ")\n" +
		"    case \"$classes\" in *\"class htb 1:10\"*) ;; *) echo \"出口已有非 FLMX 的 HTB 整形（非本调优装上的 1:10 类），已保留原配置\" >&2; exit 1 ;; esac\n" +
		"    case \"$show\" in *\"parent 1:10\"*) ;; *) echo \"出口已有非 FLMX 的 HTB 整形（缺少本调优的 1:10 叶队列），已保留原配置\" >&2; exit 1 ;; esac\n" +
		"    ;;\n" +
		"  *) echo \"出口已有非 FLMX 的 $rootkind 整形，已保留原配置\" >&2; exit 1 ;;\n" +
		"esac\n"
	return "#!/bin/sh\nset -eu\n" + guard + fmt.Sprintf("tc qdisc del dev %s root || true\ntc qdisc add dev %s root handle 1: htb default 10\ntc class replace dev %s parent 1: classid 1:10 htb rate %s ceil %s burst %d cburst %d quantum 1514\ntc qdisc replace dev %s parent 1:10 handle 10: fq limit 40960 flow_limit 8192 maxrate %s\n", q, q, q, r, r, burst, burst, q, r)
}

func persistTuneShape(ctx context.Context, d *tuneDetection, rate int) error {
	_, err := callTuneEngine(ctx, "shape", d, strconv.Itoa(rate))
	if err != nil {
		return err
	}
	script := filepath.Join(tuneEngineDir(), "shape.sh")
	if err = writeFileAtomic(script, []byte(tuneShapeScript(d.Interface, rate)), 0o700); err != nil {
		return err
	}
	// Replace the engine's test shaper so the verified queue is the persisted one.
	if _, err = tuneCommand(ctx, "bash", script); err != nil {
		return err
	}
	if InContainer() {
		return nil
	}
	if !commandExists("systemctl") {
		return fmt.Errorf("缺少 systemd，无法持久保存整形配置")
	}
	unit := "[Unit]\nDescription=FLMX egress tuning\nAfter=network-online.target\nWants=network-online.target\n[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=" + script + "\n[Install]\nWantedBy=multi-user.target\n"
	if err = writeFileAtomic(tuneShapeUnitPath(), []byte(unit), 0o644); err != nil {
		return err
	}
	if _, err = tuneCommand(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	_, err = tuneCommand(ctx, "systemctl", "enable", "flmx-tuning-qdisc.service")
	return err
}
