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
		"enabled": false, "time": "04:00", "jitterMinutes": 30,
	}); err != nil {
		t.Fatalf("valid disable must apply: %v", err)
	}
}
