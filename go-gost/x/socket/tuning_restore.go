package socket

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Tuning restore and crash recovery (upstream 3.3.43 tuning_queues.go,
// restore half). The deep-scan half of that file — captureTuneQueues,
// tuneQueueReplay, executeDeepTuning, persistTuneShape,
// tuneShapeScript, tuneTrafficBytes — lives in tuning_queues.go
// (batch 2); the two files do not overlap.
//
// FLMX notes vs upstream (flyx): the systemd unit is
// flmx-tuning-qdisc.service (fork rename rule); the backup path stays
// /etc/flux_agent/tuning-backup.json (legacy name, unchanged by
// decision); shape files live under the engine directory next to it.

func restoreTuneQueues(name string) error {
	// D2S (PORT-REVIEW-L1-D2S): a replay script written by a pre-D2
	// agent wedges all three recovery paths (recoverTuneTransaction,
	// the startTuneJob gate, restoreAutomaticTuning) — the agent
	// upgrade alone does not un-wedge a node, because
	// captureTuneQueues only rewrites current-qdisc.sh on the next
	// deep scan and the next deep scan is itself gated on recovery.
	// Validate the on-disk format before executing: a stale script
	// is migrated in place, then executed. restoreTuneQueues is the
	// only executor of these scripts, so this covers all three paths.
	if err := migrateStaleTuneQueueReplay(name); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := tuneCommand(ctx, "bash", filepath.Join(tuneEngineDir(), name))
	return err
}

// migrateStaleTuneQueueReplay upgrades a pre-D2 queue replay script
// to the D2 format in place. Only the mqmajor condition line is
// defective — the old `if [ -z "$mqmajor" ]` skipped the handle
// repair when the kernel recreated the mq root as `mq 0:`, so the
// leaves bound to `parent 0:N`, which real tc rejects — so the
// migration rewrites exactly that line. Regenerating from a live
// `tc qdisc show` would be wrong here: once deep has persisted a
// shaper, the live queues are the shaped ones, not the captured
// original queues the script is supposed to restore.
func migrateStaleTuneQueueReplay(name string) error {
	path := filepath.Join(tuneEngineDir(), name)
	b, err := os.ReadFile(path)
	if err != nil {
		// Missing or unreadable: leave it to the existing execution
		// error path instead of inventing a second failure mode.
		return nil
	}
	s := string(b)
	// Only mq replays carry the probe; shape.sh replays and non-mq
	// replays have no mqmajor line and need no migration.
	if !strings.Contains(s, `mqmajor=$(tc qdisc show`) {
		return nil
	}
	if strings.Contains(s, `|| [ "$mqmajor" = 0 ]`) {
		return nil // already D2 format
	}
	if !strings.Contains(s, `if [ -z "$mqmajor" ]`) {
		return fmt.Errorf("队列恢复脚本 %s 为无法识别的旧格式，已拒绝执行，请先恢复原配置", name)
	}
	// Match on the condition head, not the full `; then` spelling:
	// the old generator wrote `if [ -z "$mqmajor" ]; then`, while a
	// byte-for-byte downgrade of a D2 script leaves
	// `if [ -z "$mqmajor" ] ; then`. Both are the same stale
	// condition; replacing the head covers both (a space before `;`
	// is valid shell).
	migrated := strings.Replace(s, `if [ -z "$mqmajor" ]`, `if [ -z "$mqmajor" ] || [ "$mqmajor" = 0 ]`, 1)
	return writeFileAtomic(path, []byte(migrated), 0o700)
}

type tuneShapeFile struct {
	Path   string
	Data   []byte
	Exists bool
}

func captureTuneShapeFiles() ([]tuneShapeFile, error) {
	var saved []tuneShapeFile
	for _, path := range []string{filepath.Join(tuneEngineDir(), "shape.sh"), tuneShapeUnitPath(), tuneStatePath("tuning-result.json")} {
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		saved = append(saved, tuneShapeFile{Path: path, Data: b, Exists: err == nil})
	}
	return saved, nil
}

func restoreTuneShapeFiles(saved []tuneShapeFile) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, f := range saved {
		if f.Exists {
			mode := os.FileMode(0o600)
			if strings.HasSuffix(f.Path, ".sh") {
				mode = 0o700
			}
			if f.Path == tuneShapeUnitPath() {
				mode = 0o644
			}
			if err := writeFileAtomic(f.Path, f.Data, mode); err != nil {
				return err
			}
		} else {
			if f.Path == tuneShapeUnitPath() {
				if _, err := os.Stat(f.Path); err == nil && !InContainer() {
					if _, err = tuneCommand(ctx, "systemctl", "disable", "--now", "flmx-tuning-qdisc.service"); err != nil {
						return err
					}
				}
			}
			if err := os.Remove(f.Path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	// Reload after removing a unit too, so systemd drops the stale definition.
	if !InContainer() && commandExists("systemctl") {
		_, err := tuneCommand(ctx, "systemctl", "daemon-reload")
		return err
	}
	return nil
}

func recoverTuneTransaction() error {
	path := tuneStatePath("tuning-transaction.json")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var files []tuneShapeFile
	if err = json.Unmarshal(b, &files); err != nil {
		return err
	}
	if err = restoreTuneShapeFiles(files); err != nil {
		return err
	}
	if err = restoreTuneQueues("current-qdisc.sh"); err != nil {
		return err
	}
	return os.Remove(path)
}

var tuneShapeUnitPath = func() string { return "/etc/systemd/system/flmx-tuning-qdisc.service" }

func restoreAutomaticTuning(ctx context.Context) error {
	tuneProgress("restoring", 35, "恢复首次调优前的内核参数与出口队列")
	original := filepath.Join(tuneEngineDir(), "original-qdisc.sh")
	_, queueErr := os.Stat(original)
	if queueErr != nil && !os.IsNotExist(queueErr) {
		return queueErr
	}
	if os.IsNotExist(queueErr) {
		if _, err := os.Stat(filepath.Join(tuneEngineDir(), "shape.sh")); err == nil {
			return fmt.Errorf("找不到整形前的队列备份，已保留现有配置，请检查 Agent 数据目录")
		}
		if _, err := os.Stat(tuneShapeUnitPath()); err == nil {
			return fmt.Errorf("找不到整形前的队列备份，已保留整形服务配置")
		}
	}
	if queueErr == nil {
		var err error
		if err = restoreTuneQueues("original-qdisc.sh"); err != nil {
			return err
		}
		if !InContainer() {
			if _, e := os.Stat(tuneShapeUnitPath()); e == nil {
				if _, err = tuneCommand(ctx, "systemctl", "disable", "--now", "flmx-tuning-qdisc.service"); err != nil {
					return err
				}
				if err = os.Remove(tuneShapeUnitPath()); err != nil {
					return err
				}
				if _, err = tuneCommand(ctx, "systemctl", "daemon-reload"); err != nil {
					return err
				}
			}
		}
	}
	st, err := applyTuning(nil, savedTuneParams())
	if err != nil {
		return err
	}
	if len(st.Warnings) > 0 {
		return fmt.Errorf("部分参数恢复失败：%s", strings.Join(st.Warnings, "；"))
	}
	for _, name := range []string{"shape.sh", "original-qdisc.sh", "current-qdisc.sh", "queue-interface"} {
		if err = os.Remove(filepath.Join(tuneEngineDir(), name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err = os.Remove(tuneStatePath("tuning-result.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	// The original queues are back, so an unfinished scan has nothing to undo.
	if err = os.Remove(tuneStatePath("tuning-transaction.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
