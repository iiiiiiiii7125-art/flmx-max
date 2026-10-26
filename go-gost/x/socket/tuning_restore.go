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
// restore half). Batch 1 ports only what restoreAuto and the startup
// recovery need; the deep-scan half of that file — captureTuneQueues,
// tuneQueueReplay, executeDeepTuning, persistTuneShape,
// tuneShapeScript, tuneTrafficBytes — lands with batch 2.
//
// FLMX notes vs upstream (flyx): the systemd unit is
// flmx-tuning-qdisc.service (fork rename rule); the backup path stays
// /etc/flux_agent/tuning-backup.json (legacy name, unchanged by
// decision); shape files live under the engine directory next to it.

func restoreTuneQueues(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := tuneCommand(ctx, "bash", filepath.Join(tuneEngineDir(), name))
	return err
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
