package socket

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRestartScheduleSetting(t *testing.T) {
	valid := []restartScheduleSetting{
		{Enabled: true, Time: "04:00", JitterMinutes: 30},
		{Enabled: true, Time: "00:00", JitterMinutes: 0},
		{Enabled: false, Time: "23:59", JitterMinutes: 720},
		{Enabled: true, Time: "19:05", JitterMinutes: 1},
		// 模式面：显式 daily 与旧面板的空 mode 同义；interval 的
		// 边界值（15 / 10080）与典型值合法。
		{Enabled: true, Time: "04:00", JitterMinutes: 30, Mode: "daily"},
		{Enabled: true, Time: "04:00", JitterMinutes: 30, Mode: "interval", IntervalMinutes: 15},
		{Enabled: true, Time: "04:00", JitterMinutes: 30, Mode: "interval", IntervalMinutes: 120},
		{Enabled: true, Time: "04:00", JitterMinutes: 720, Mode: "interval", IntervalMinutes: 10080},
		// daily 模式不消费间隔字段，越界值不构成拒绝理由（面板侧
		// 同口径）。
		{Enabled: true, Time: "04:00", JitterMinutes: 30, Mode: "daily", IntervalMinutes: 0},
	}
	for _, s := range valid {
		if err := validateRestartScheduleSetting(s); err != nil {
			t.Fatalf("expected valid %+v, got %v", s, err)
		}
	}
	invalid := []restartScheduleSetting{
		{Enabled: true, Time: "24:00", JitterMinutes: 30},
		{Enabled: true, Time: "4:00", JitterMinutes: 30},
		{Enabled: true, Time: "04:60", JitterMinutes: 30},
		{Enabled: true, Time: "", JitterMinutes: 30},
		{Enabled: true, Time: "04:00:00", JitterMinutes: 30},
		{Enabled: true, Time: "04:00", JitterMinutes: -1},
		{Enabled: true, Time: "04:00", JitterMinutes: 721},
		{Enabled: true, Time: "04:00", JitterMinutes: 30, Mode: "weekly"},
		{Enabled: true, Time: "04:00", JitterMinutes: 30, Mode: "interval", IntervalMinutes: 0},
		{Enabled: true, Time: "04:00", JitterMinutes: 30, Mode: "interval", IntervalMinutes: 14},
		{Enabled: true, Time: "04:00", JitterMinutes: 30, Mode: "interval", IntervalMinutes: 10081},
	}
	for _, s := range invalid {
		if err := validateRestartScheduleSetting(s); err == nil {
			t.Fatalf("expected invalid %+v to be rejected", s)
		}
	}
}

func TestRenderRestartTimerUnit(t *testing.T) {
	body := renderRestartTimerUnit(restartScheduleSetting{Enabled: true, Time: "04:30", JitterMinutes: 30})
	for _, want := range []string{
		"OnCalendar=*-*-* 04:30:00",
		"RandomizedDelaySec=1800",
		"Persistent=true",
		"Unit=flmx-agent-restart.service",
		"WantedBy=timers.target",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("timer unit missing %q:\n%s", want, body)
		}
	}
	zero := renderRestartTimerUnit(restartScheduleSetting{Enabled: true, Time: "00:00", JitterMinutes: 0})
	if strings.Contains(zero, "RandomizedDelaySec") {
		t.Fatalf("zero jitter must omit RandomizedDelaySec:\n%s", zero)
	}
	if !strings.Contains(zero, "OnCalendar=*-*-* 00:00:00") {
		t.Fatalf("timer unit missing midnight calendar:\n%s", zero)
	}
	// 显式 daily 与空 mode（旧面板）渲染逐字一致。
	daily := renderRestartTimerUnit(restartScheduleSetting{Enabled: true, Time: "04:30", JitterMinutes: 30, Mode: "daily"})
	if daily != body {
		t.Fatalf("explicit daily mode must render the pre-mode unit:\n%s\n---\n%s", daily, body)
	}
	// interval 模式：单调触发、无 OnCalendar；抖动照旧以
	// RandomizedDelaySec 呈现为上限延迟。
	iv := renderRestartTimerUnit(restartScheduleSetting{
		Enabled: true, Time: "04:30", JitterMinutes: 10, Mode: "interval", IntervalMinutes: 120,
	})
	for _, want := range []string{
		"OnBootSec=120min",
		"OnUnitActiveSec=120min",
		"RandomizedDelaySec=600",
		"Persistent=true",
		"Unit=flmx-agent-restart.service",
		"WantedBy=timers.target",
	} {
		if !strings.Contains(iv, want) {
			t.Fatalf("interval timer unit missing %q:\n%s", want, iv)
		}
	}
	if strings.Contains(iv, "OnCalendar") {
		t.Fatalf("interval timer must not carry a calendar trigger:\n%s", iv)
	}
	ivZero := renderRestartTimerUnit(restartScheduleSetting{
		Enabled: true, Time: "04:30", JitterMinutes: 0, Mode: "interval", IntervalMinutes: 60,
	})
	if strings.Contains(ivZero, "RandomizedDelaySec") {
		t.Fatalf("zero jitter must omit RandomizedDelaySec in interval mode:\n%s", ivZero)
	}
	if !strings.Contains(ivZero, "OnBootSec=60min") || !strings.Contains(ivZero, "OnUnitActiveSec=60min") {
		t.Fatalf("interval timer missing monotonic triggers:\n%s", ivZero)
	}
}

func TestRenderRestartServiceUnit(t *testing.T) {
	body := renderRestartServiceUnit("flmx_agent")
	if !strings.Contains(body, "ExecStart=/bin/systemctl restart flmx_agent") {
		t.Fatalf("service unit must restart the agent service only:\n%s", body)
	}
	if strings.Contains(body, "reboot") && !strings.Contains(body, "not a host reboot") {
		t.Fatalf("service unit must not reboot the host:\n%s", body)
	}
	legacy := renderRestartServiceUnit("flux_agent")
	if !strings.Contains(legacy, "ExecStart=/bin/systemctl restart flux_agent") {
		t.Fatalf("legacy layout must restart its own service name:\n%s", legacy)
	}
}

// stubRestartScheduleEnv points the unit dir at a scratch directory,
// fakes systemctl through the tuneCommand seam, and forces the
// not-in-container branch. It returns the recorded systemctl calls.
func stubRestartScheduleEnv(t *testing.T) *[][2]string {
	t.Helper()
	dir := t.TempDir()
	oldDir := restartScheduleUnitDir
	restartScheduleUnitDir = dir
	t.Cleanup(func() { restartScheduleUnitDir = oldDir })

	oldCmd, oldLook := tuneCommand, tuneLookPath
	calls := &[][2]string{}
	tuneCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		*calls = append(*calls, [2]string{name, strings.Join(args, " ")})
		return "", nil
	}
	tuneLookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	t.Cleanup(func() { tuneCommand, tuneLookPath = oldCmd, oldLook })

	oldMarker, oldCgroup := containerMarkerPath, containerCgroupPath
	containerMarkerPath = filepath.Join(dir, "no-dockerenv")
	containerCgroupPath = filepath.Join(dir, "no-cgroup")
	t.Cleanup(func() { containerMarkerPath, containerCgroupPath = oldMarker, oldCgroup })
	return calls
}

func countCall(calls [][2]string, want string) int {
	n := 0
	for _, c := range calls {
		if c[0] == "systemctl" && c[1] == want {
			n++
		}
	}
	return n
}

func TestApplyRestartScheduleEnableIdempotentThenDisable(t *testing.T) {
	calls := stubRestartScheduleEnv(t)
	ctx := context.Background()
	s := restartScheduleSetting{Enabled: true, Time: "03:15", JitterMinutes: 45}

	if err := applyRestartSchedule(ctx, s); err != nil {
		t.Fatalf("enable: %v", err)
	}
	timerPath := filepath.Join(restartScheduleUnitDir, restartTimerUnitName)
	servicePath := filepath.Join(restartScheduleUnitDir, restartServiceUnitName)
	timerBody, err := os.ReadFile(timerPath)
	if err != nil {
		t.Fatalf("timer unit not written: %v", err)
	}
	if string(timerBody) != renderRestartTimerUnit(s) {
		t.Fatalf("timer unit content mismatch:\n%s", timerBody)
	}
	if _, err := os.Stat(servicePath); err != nil {
		t.Fatalf("service unit not written: %v", err)
	}
	if countCall(*calls, "daemon-reload") != 1 || countCall(*calls, "enable --now "+restartTimerUnitName) != 1 {
		t.Fatalf("expected one daemon-reload and one enable, got %v", *calls)
	}

	// Re-applying the same setting (the panel's reconnect re-push)
	// must not rewrite files or daemon-reload again.
	*calls = nil
	if err := applyRestartSchedule(ctx, s); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if countCall(*calls, "daemon-reload") != 0 {
		t.Fatalf("idempotent re-apply must skip daemon-reload, got %v", *calls)
	}

	// Changing the setting rewrites and reloads exactly once more.
	*calls = nil
	s2 := restartScheduleSetting{Enabled: true, Time: "05:00", JitterMinutes: 0}
	if err := applyRestartSchedule(ctx, s2); err != nil {
		t.Fatalf("change: %v", err)
	}
	if countCall(*calls, "daemon-reload") != 1 {
		t.Fatalf("changed setting must daemon-reload once, got %v", *calls)
	}

	// Disabling removes both units and disables the timer.
	*calls = nil
	if err := applyRestartSchedule(ctx, restartScheduleSetting{Enabled: false, Time: "05:00"}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	for _, p := range []string{timerPath, servicePath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("unit %s must be removed after disable", p)
		}
	}
	if countCall(*calls, "disable --now "+restartTimerUnitName) != 1 {
		t.Fatalf("expected timer disable, got %v", *calls)
	}

	// Disabling again (nothing installed) is a clean no-op.
	if err := applyRestartSchedule(ctx, restartScheduleSetting{Enabled: false, Time: "05:00"}); err != nil {
		t.Fatalf("disable twice: %v", err)
	}
}

// fix/restart-interval：间隔模式经 apply 全程落地（单元内容＝
// 渲染结果）、与 daily 互切时原子换写（同一对单元文件、不并存
// 第二个 timer）、关闭删除。
func TestApplyRestartScheduleIntervalAndModeSwitch(t *testing.T) {
	calls := stubRestartScheduleEnv(t)
	ctx := context.Background()
	timerPath := filepath.Join(restartScheduleUnitDir, restartTimerUnitName)

	iv := restartScheduleSetting{Enabled: true, Time: "04:00", JitterMinutes: 10, Mode: "interval", IntervalMinutes: 120}
	if err := applyRestartSchedule(ctx, iv); err != nil {
		t.Fatalf("enable interval: %v", err)
	}
	body, err := os.ReadFile(timerPath)
	if err != nil {
		t.Fatalf("timer unit not written: %v", err)
	}
	if string(body) != renderRestartTimerUnit(iv) {
		t.Fatalf("interval timer content mismatch:\n%s", body)
	}
	if countCall(*calls, "enable --now "+restartTimerUnitName) != 1 {
		t.Fatalf("expected one timer enable, got %v", *calls)
	}
	// 目录里只能有这一对单元（模式切换不留影子 timer）。
	entries, err := os.ReadDir(restartScheduleUnitDir)
	if err != nil {
		t.Fatalf("read unit dir: %v", err)
	}
	unitFiles := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "flmx-agent-restart.") {
			unitFiles++
		}
	}
	if unitFiles != 2 {
		t.Fatalf("expected exactly the service+timer pair, found %d unit files", unitFiles)
	}

	// 切回 daily：同一文件被换写为日历形态、reload 一次。
	*calls = nil
	daily := restartScheduleSetting{Enabled: true, Time: "03:15", JitterMinutes: 45}
	if err := applyRestartSchedule(ctx, daily); err != nil {
		t.Fatalf("switch to daily: %v", err)
	}
	body, err = os.ReadFile(timerPath)
	if err != nil {
		t.Fatalf("timer unit missing after switch: %v", err)
	}
	if string(body) != renderRestartTimerUnit(daily) || strings.Contains(string(body), "OnUnitActiveSec") {
		t.Fatalf("daily switch must replace the timer content:\n%s", body)
	}
	if countCall(*calls, "daemon-reload") != 1 {
		t.Fatalf("mode switch must daemon-reload once, got %v", *calls)
	}

	// 关闭：两件单元删除（与 daily 路径同一出口）。
	if err := applyRestartSchedule(ctx, restartScheduleSetting{Enabled: false, Time: "03:15"}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := os.Stat(timerPath); !os.IsNotExist(err) {
		t.Fatalf("timer unit must be removed after disable")
	}
}

func TestHandleSetRestartScheduleRejectsInvalid(t *testing.T) {
	stubRestartScheduleEnv(t)
	w := &WebSocketReporter{}
	if err := w.handleSetRestartSchedule(map[string]interface{}{
		"enabled": true, "time": "25:00", "jitterMinutes": 30,
	}); err == nil {
		t.Fatal("invalid time must be rejected")
	}
	if err := w.handleSetRestartSchedule(map[string]interface{}{
		"enabled": true, "time": "04:00", "jitterMinutes": 9999,
	}); err == nil {
		t.Fatal("out-of-range jitter must be rejected")
	}
	if err := w.handleSetRestartSchedule(map[string]interface{}{
		"enabled": true, "time": "04:00", "jitterMinutes": 30,
		"mode": "interval", "intervalMinutes": 5,
	}); err == nil {
		t.Fatal("below-floor interval must be rejected")
	}
	if err := w.handleSetRestartSchedule(map[string]interface{}{
		"enabled": true, "time": "04:00", "jitterMinutes": 30,
		"mode": "interval", "intervalMinutes": 120,
	}); err != nil {
		t.Fatalf("valid interval setting must apply: %v", err)
	}
	if err := w.handleSetRestartSchedule(map[string]interface{}{
		"enabled": false, "time": "04:00", "jitterMinutes": 30,
	}); err != nil {
		t.Fatalf("valid disable must apply: %v", err)
	}
}
