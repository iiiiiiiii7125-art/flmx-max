package nftables

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeRunner struct {
	scripts     []string
	tables      []string
	err         error
	testErr     error
	listJSON    []byte
	listJSONErr error
}

func (f *fakeRunner) ApplyScript(ctx context.Context, cfg SSHConfig, table, script string) error {
	f.scripts = append(f.scripts, script)
	f.tables = append(f.tables, table)
	return f.err
}

func (f *fakeRunner) Test(ctx context.Context, cfg SSHConfig) error {
	return f.testErr
}

func (f *fakeRunner) ListTableJSON(ctx context.Context, cfg SSHConfig, table string) ([]byte, error) {
	f.tables = append(f.tables, table)
	return f.listJSON, f.listJSONErr
}

func TestManagerReconcileAppliesRenderedScript(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewManager(runner)
	plan := NodePlan{
		NodeID: 7,
		Rules:  []Rule{{ForwardID: 42, InPort: 24000, TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp", "udp"}}},
	}

	result, err := manager.Reconcile(context.Background(), SSHConfig{Host: "203.0.113.10", Port: 22, Username: "root"}, plan)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(runner.scripts) != 1 {
		t.Fatalf("expected 1 script, got %d", len(runner.scripts))
	}
	if !strings.Contains(runner.scripts[0], `flmx_e3b0c442 forward:42 dnat tcp`) {
		t.Fatalf("script missing forward comment:\n%s", runner.scripts[0])
	}
	if result.NodeID != 7 || result.Hashes[42] == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestManagerReconcileReturnsRunnerError(t *testing.T) {
	runner := &fakeRunner{err: errors.New("ssh failed")}
	manager := NewManager(runner)

	_, err := manager.Reconcile(context.Background(), SSHConfig{Host: "203.0.113.10", Port: 22, Username: "root"}, NodePlan{NodeID: 7})
	if !errors.Is(err, runner.err) {
		t.Fatalf("expected original runner error, got %v", err)
	}
}

func TestManagerClearAppliesEmptyTable(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewManager(runner)

	if err := manager.Clear(context.Background(), SSHConfig{Host: "203.0.113.10", Port: 22, Username: "root"}); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(runner.scripts) != 1 {
		t.Fatalf("expected 1 script, got %d", len(runner.scripts))
	}
	if strings.Contains(runner.scripts[0], "masquerade comment") {
		t.Fatalf("empty table should not include masquerade:\n%s", runner.scripts[0])
	}
}

func TestManagerTestPassesThroughRunnerError(t *testing.T) {
	runner := &fakeRunner{testErr: errors.New("probe failed")}
	manager := NewManager(runner)

	err := manager.Test(context.Background(), SSHConfig{Host: "203.0.113.10", Port: 22, Username: "root"})
	if !errors.Is(err, runner.testErr) {
		t.Fatalf("expected original runner error, got %v", err)
	}
}

func TestManagerCollectCountersParsesRunnerTableJSON(t *testing.T) {
	runner := &fakeRunner{listJSON: []byte(`{
		"nftables": [
			{"rule": {
				"family": "inet",
				"table": "flmx_e3b0c442",
				"chain": "forward",
				"comment": "flmx_e3b0c442 forward:77 to-target tcp",
				"expr": [
					{"counter": {"packets": 3, "bytes": 2048}}
				]
			}}
		]
	}`)}
	manager := NewManager(runner)

	samples, err := manager.CollectCounters(context.Background(), SSHConfig{Host: "203.0.113.10", Port: 22, Username: "root"})
	if err != nil {
		t.Fatalf("CollectCounters: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d: %+v", len(samples), samples)
	}
	want := CounterSample{
		ForwardID: 77,
		Direction: CounterDirectionToTarget,
		Protocol:  "tcp",
		Bytes:     2048,
		Packets:   3,
	}
	if samples[0] != want {
		t.Fatalf("expected %+v, got %+v", want, samples[0])
	}
}

func TestManagerMethodsRequireInitializedRunner(t *testing.T) {
	cfg := SSHConfig{Host: "203.0.113.10", Port: 22, Username: "root"}
	plan := NodePlan{NodeID: 7}
	expected := errors.New("nftables manager not initialized")

	var nilManager *Manager
	if err := nilManager.Test(context.Background(), cfg); err == nil || err.Error() != expected.Error() {
		t.Fatalf("expected not initialized error from nil manager Test, got %v", err)
	}

	if _, err := nilManager.Reconcile(context.Background(), cfg, plan); err == nil || err.Error() != expected.Error() {
		t.Fatalf("expected not initialized error from nil manager Reconcile, got %v", err)
	}

	if err := nilManager.Clear(context.Background(), cfg); err == nil || err.Error() != expected.Error() {
		t.Fatalf("expected not initialized error from nil manager Clear, got %v", err)
	}

	if _, err := nilManager.CollectCounters(context.Background(), cfg); err == nil || err.Error() != expected.Error() {
		t.Fatalf("expected not initialized error from nil manager CollectCounters, got %v", err)
	}

	manager := &Manager{}
	if err := manager.Test(context.Background(), cfg); err == nil || err.Error() != expected.Error() {
		t.Fatalf("expected not initialized error from Test, got %v", err)
	}

	if _, err := manager.Reconcile(context.Background(), cfg, plan); err == nil || err.Error() != expected.Error() {
		t.Fatalf("expected not initialized error from Reconcile, got %v", err)
	}

	if err := manager.Clear(context.Background(), cfg); err == nil || err.Error() != expected.Error() {
		t.Fatalf("expected not initialized error from Clear, got %v", err)
	}

	if _, err := manager.CollectCounters(context.Background(), cfg); err == nil || err.Error() != expected.Error() {
		t.Fatalf("expected not initialized error from CollectCounters, got %v", err)
	}
}

// P1-1: the Manager applies and collects against its own instance's
// table, and Reconcile follows the plan's instance.
func TestManagerUsesInstanceTable(t *testing.T) {
	runner := &fakeRunner{listJSON: []byte(`{
		"nftables": [
			{"rule": {
				"family": "inet",
				"table": "flmx_16477688",
				"chain": "forward",
				"comment": "flmx_16477688 forward:77 to-target tcp",
				"expr": [
					{"counter": {"packets": 3, "bytes": 2048}}
				]
			}}
		]
	}`)}
	manager := NewManagerWithInstance(runner, "blue")
	cfg := SSHConfig{Host: "203.0.113.10", Port: 22, Username: "root"}

	plan := NodePlan{
		NodeID:     7,
		InstanceID: "blue",
		Rules:      []Rule{{ForwardID: 42, InPort: 24000, TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp"}}},
	}
	if _, err := manager.Reconcile(context.Background(), cfg, plan); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(runner.tables) != 1 || runner.tables[0] != "flmx_16477688" {
		t.Fatalf("Reconcile must apply table flmx_16477688, got %v", runner.tables)
	}
	if !strings.Contains(runner.scripts[0], "table inet flmx_16477688 {") {
		t.Fatalf("Reconcile script must render the instance table:\n%s", runner.scripts[0])
	}

	samples, err := manager.CollectCounters(context.Background(), cfg)
	if err != nil {
		t.Fatalf("CollectCounters: %v", err)
	}
	if len(runner.tables) != 2 || runner.tables[1] != "flmx_16477688" {
		t.Fatalf("CollectCounters must list table flmx_16477688, got %v", runner.tables)
	}
	if len(samples) != 1 || samples[0].ForwardID != 77 {
		t.Fatalf("expected 1 sample from the instance table, got %+v", samples)
	}

	if err := manager.Clear(context.Background(), cfg); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(runner.tables) != 3 || runner.tables[2] != "flmx_16477688" {
		t.Fatalf("Clear must apply the instance table, got %v", runner.tables)
	}
	if !strings.Contains(runner.scripts[1], "table inet flmx_16477688 {") {
		t.Fatalf("Clear script must render the instance table:\n%s", runner.scripts[1])
	}
}
