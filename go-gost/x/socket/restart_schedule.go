package socket

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// --- 节点定时重启 agent 服务（保险丝） ---
//
// 面板是设置的权威源，经 "SetRestartSchedule" 命令下发
// {enabled, time, jitterMinutes}；本文件把设置落成一对 systemd
// 单元（flmx-agent-restart.service + .timer）持久化执行：
//   - timer 的 OnCalendar 按节点本机时间每日触发，
//     RandomizedDelaySec 承担随机抖动；
//   - service 是 oneshot，ExecStart 只做 systemctl restart 本机
//     agent 服务（安装布局名，见 agentServiceNameFor），不整机
//     重启、不依赖面板连接——面板离线、面板被墙都不影响触发；
//   - 单元由设置机械生成、幂等重写（内容不变时不重写、不
//     daemon-reload）；关开关即 disable --now 并删除两件单元。
//
// 校验边界与面板 validateRestartSchedule 逐字一致（面板判＋
// agent 复判）：时刻 "HH:MM"（00:00–23:59），抖动 0–720 分钟。

const (
	restartTimerUnitName            = "flmx-agent-restart.timer"
	restartServiceUnitName          = "flmx-agent-restart.service"
	restartScheduleMaxJitterMinutes = 720
)

var restartScheduleUnitDir = "/etc/systemd/system"

var restartTimePattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// restartScheduleSetting is the wire shape of the panel's
// SetRestartSchedule payload (field names are the contract — do
// not rename).
type restartScheduleSetting struct {
	Enabled       bool   `json:"enabled"`
	Time          string `json:"time"`
	JitterMinutes int    `json:"jitterMinutes"`
}

func validateRestartScheduleSetting(s restartScheduleSetting) error {
	if !restartTimePattern.MatchString(s.Time) {
		return fmt.Errorf("重启时刻须为 HH:MM 格式（00:00–23:59）: %q", s.Time)
	}
	if s.JitterMinutes < 0 || s.JitterMinutes > restartScheduleMaxJitterMinutes {
		return fmt.Errorf("随机抖动须在 0–%d 分钟之间: %d", restartScheduleMaxJitterMinutes, s.JitterMinutes)
	}
	return nil
}

// renderRestartServiceUnit renders the oneshot that restarts only
// the agent service of this install layout.
func renderRestartServiceUnit(agentServiceName string) string {
	return "[Unit]\n" +
		"Description=FLMX scheduled agent restart (panel-set fuse; service only, not a host reboot)\n" +
		"After=network.target\n\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"ExecStart=/bin/systemctl restart " + agentServiceName + "\n"
}

// renderRestartTimerUnit renders the daily timer. The jitter is a
// systemd RandomizedDelaySec window: the trigger fires uniformly
// at random between the calendar time and that time plus the
// window. A zero window omits the line entirely.
func renderRestartTimerUnit(s restartScheduleSetting) string {
	body := "[Unit]\n" +
		"Description=FLMX scheduled agent restart timer\n\n" +
		"[Timer]\n" +
		"OnCalendar=*-*-* " + s.Time + ":00\n"
	if s.JitterMinutes > 0 {
		body += fmt.Sprintf("RandomizedDelaySec=%d\n", s.JitterMinutes*60)
	}
	body += "Persistent=true\n" +
		"Unit=" + restartServiceUnitName + "\n\n" +
		"[Install]\n" +
		"WantedBy=timers.target\n"
	return body
}

// writeUnitIfChanged writes body to path unless the file already
// holds exactly it; it reports whether anything changed.
func writeUnitIfChanged(path, body string) (bool, error) {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == body {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := writeFileAtomic(path, []byte(body), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// applyRestartSchedule installs or removes the timer pair per the
// setting. It is the production entry used by the command handler;
// the unit directory and systemctl go through the package seams
// (restartScheduleUnitDir, tuneCommand) so tests can drive it
// against a scratch directory.
func applyRestartSchedule(ctx context.Context, s restartScheduleSetting) error {
	if err := validateRestartScheduleSetting(s); err != nil {
		return err
	}
	timerPath := filepath.Join(restartScheduleUnitDir, restartTimerUnitName)
	servicePath := filepath.Join(restartScheduleUnitDir, restartServiceUnitName)

	if !s.Enabled {
		// 关闭：停用并删除两件单元。无 systemd 的环境里不可能装过
		// timer，直接成功返回（不让关开关这条路被环境卡住）。
		if InContainer() || !commandExists("systemctl") {
			return nil
		}
		_, _ = tuneCommand(ctx, "systemctl", "disable", "--now", restartTimerUnitName)
		removed := false
		for _, p := range []string{timerPath, servicePath} {
			if err := os.Remove(p); err == nil {
				removed = true
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("删除定时重启单元失败 %s: %w", p, err)
			}
		}
		if removed {
			if _, err := tuneCommand(ctx, "systemctl", "daemon-reload"); err != nil {
				return fmt.Errorf("systemctl daemon-reload 失败: %w", err)
			}
		}
		return nil
	}

	if InContainer() || !commandExists("systemctl") {
		return fmt.Errorf("节点无 systemd，定时重启无法持久化（容器形态不支持）")
	}
	agentService := agentServiceNameFor(resolveAgentBinaryPath())
	changed := false
	for path, body := range map[string]string{
		servicePath: renderRestartServiceUnit(agentService),
		timerPath:   renderRestartTimerUnit(s),
	} {
		c, err := writeUnitIfChanged(path, body)
		if err != nil {
			return fmt.Errorf("写入定时重启单元失败 %s: %w", path, err)
		}
		changed = changed || c
	}
	if changed {
		if _, err := tuneCommand(ctx, "systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("systemctl daemon-reload 失败: %w", err)
		}
	}
	if _, err := tuneCommand(ctx, "systemctl", "enable", "--now", restartTimerUnitName); err != nil {
		return fmt.Errorf("启用定时重启 timer 失败: %w", err)
	}
	return nil
}

// handleSetRestartSchedule handles the panel's SetRestartSchedule
// command: validate (again — the panel already judged), then apply.
// Applying is serialized with the other mutation commands by the
// dispatch layer (isMutationCommand), and the operation itself is
// idempotent, so a reconnect re-push of the same setting is a no-op.
func (w *WebSocketReporter) handleSetRestartSchedule(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化定时重启设置失败: %v", err)
	}
	var s restartScheduleSetting
	if err := json.Unmarshal(jsonData, &s); err != nil {
		return fmt.Errorf("解析定时重启设置失败: %v", err)
	}
	if err := validateRestartScheduleSetting(s); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return applyRestartSchedule(ctx, s)
}
