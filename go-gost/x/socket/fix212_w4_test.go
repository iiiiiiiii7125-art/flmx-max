package socket

// Fix-wave 2.1.2 lane 4 (agent side) regression gates:
//   - F-A01: persist failure inside a mutation command must roll
//     back runtime + in-memory config (three states converge).
//   - F-A02: update commands judge payload shape explicitly; the
//     bare shape is rejected naming the wrapper shape.
//   - F-A03/F-A04: string durations are normalized for every
//     time.Duration field, and an unparsable value fails the whole
//     command naming the field path (never silently kept).
//   - F-A05: the not-found contract — Delete* idempotent success,
//     Pause/Resume strict with the exact "not found" substring.
//   - B-01/B-03/B-05/B-06: dead wire fields removed, fractional-ms
//     latency, ping result stamping from the command envelope.
//   - B-07: UdpPing runs under the command budget ctx.
//   - C4-G2: uninstall ack ordering, final-state persistence and
//     re-report; duplicate dispatch (same issuance key) never
//     re-executes, a new issuance key always executes.
//   - S1-F01 (agent side): limiter identity is the name alone in
//     both registries; delete-by-name is exact.

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corelogger "github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	xlogger "github.com/go-gost/x/logger"
	"github.com/go-gost/x/registry"
)

var w4LoggerOnce sync.Once

// w4Setup installs the nop default logger the parsers require
// (other test files set it too; isolated -run invocations of this
// file must not depend on them).
func w4Setup() {
	w4LoggerOnce.Do(func() { corelogger.SetDefault(xlogger.Nop()) })
}

// forcePersistFailure points config persistence at an unwritable
// location so config.OnUpdate fails after its in-memory mutation —
// the exact F-A01 failure mode (LIVE three-state split).
func forcePersistFailure(t *testing.T) {
	t.Helper()
	oldPath := config.PersistPath()
	config.SetPersistPath(filepath.Join(t.TempDir(), "no-such-dir", "gost.json"))
	config.EnablePersist()
	t.Cleanup(func() { config.SetPersistPath(oldPath) })
}

func resetConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	w4Setup()
	orig := config.Global()
	config.Set(cfg)
	t.Cleanup(func() { config.Set(orig) })
}

func TestFix212DeleteChainPersistFailureRollsBack(t *testing.T) {
	resetConfig(t, &config.Config{})
	name := "w4-chain-rollback"
	t.Cleanup(func() { registry.ChainRegistry().Unregister(name) })
	if err := createChain(createChainRequest{Data: config.ChainConfig{Name: name}}); err != nil {
		t.Fatalf("setup createChain: %v", err)
	}
	forcePersistFailure(t)
	if err := deleteChain(deleteChainRequest{Chain: name}); err == nil {
		t.Fatal("deleteChain must fail when persist fails")
	}
	if !registry.ChainRegistry().IsRegistered(name) {
		t.Fatal("runtime chain must be restored after persist failure")
	}
	if !chainInConfig(config.Global(), name) {
		t.Fatal("in-memory config must still list the chain after persist failure")
	}
}

func TestFix212CreateChainPersistFailureRollsBack(t *testing.T) {
	resetConfig(t, &config.Config{})
	name := "w4-chain-create-rollback"
	t.Cleanup(func() { registry.ChainRegistry().Unregister(name) })
	forcePersistFailure(t)
	if err := createChain(createChainRequest{Data: config.ChainConfig{Name: name}}); err == nil {
		t.Fatal("createChain must fail when persist fails")
	}
	if registry.ChainRegistry().IsRegistered(name) {
		t.Fatal("runtime chain must be unregistered after persist failure")
	}
	if chainInConfig(config.Global(), name) {
		t.Fatal("in-memory config must not keep the chain after persist failure")
	}
}

func TestFix212UpdateChainPersistFailureRollsBack(t *testing.T) {
	resetConfig(t, &config.Config{})
	name := "w4-chain-update-rollback"
	t.Cleanup(func() { registry.ChainRegistry().Unregister(name) })
	if err := createChain(createChainRequest{Data: config.ChainConfig{Name: name}}); err != nil {
		t.Fatalf("setup createChain: %v", err)
	}
	forcePersistFailure(t)
	err := updateChain(updateChainRequest{Chain: name, Data: config.ChainConfig{Name: name, Hops: []*config.HopConfig{{Name: "hop-x"}}}})
	if err == nil {
		t.Fatal("updateChain must fail when persist fails")
	}
	if !registry.ChainRegistry().IsRegistered(name) {
		t.Fatal("runtime chain must be restored after persist failure")
	}
	cfg := config.Global()
	if len(cfg.Chains) != 1 || len(cfg.Chains[0].Hops) != 0 {
		t.Fatalf("in-memory config must be the pre-update chain, got %+v", cfg.Chains)
	}
}

func TestFix212LimiterPersistFailureRollsBack(t *testing.T) {
	resetConfig(t, &config.Config{})
	name := "w4-limiter-rollback"
	t.Cleanup(func() {
		registry.TrafficLimiterRegistry().Unregister(name)
		registry.ConnLimiterRegistry().Unregister(name)
	})
	if err := createLimiter(createLimiterRequest{Data: config.LimiterConfig{Name: name, Limits: []string{"10MB"}}}); err != nil {
		t.Fatalf("setup createLimiter: %v", err)
	}
	if err := createConnLimiter(createLimiterRequest{Data: config.LimiterConfig{Name: name, Limits: []string{"5"}}}); err != nil {
		t.Fatalf("setup createConnLimiter: %v", err)
	}
	forcePersistFailure(t)

	if err := deleteLimiter(deleteLimiterRequest{Limiter: name}); err == nil {
		t.Fatal("deleteLimiter must fail when persist fails")
	}
	if !registry.TrafficLimiterRegistry().IsRegistered(name) {
		t.Fatal("traffic limiter must be restored after persist failure")
	}
	if !limiterInConfig(config.Global(), name, false) {
		t.Fatal("config must still list the traffic limiter")
	}

	if err := deleteConnLimiter(deleteLimiterRequest{Limiter: name}); err == nil {
		t.Fatal("deleteConnLimiter must fail when persist fails")
	}
	if !registry.ConnLimiterRegistry().IsRegistered(name) {
		t.Fatal("conn limiter must be restored after persist failure")
	}
	if !limiterInConfig(config.Global(), name, true) {
		t.Fatal("config must still list the conn limiter")
	}

	// Create-side rollback on the conn registry.
	name2 := "w4-climiter-create-rollback"
	t.Cleanup(func() { registry.ConnLimiterRegistry().Unregister(name2) })
	if err := createConnLimiter(createLimiterRequest{Data: config.LimiterConfig{Name: name2}}); err == nil {
		t.Fatal("createConnLimiter must fail when persist fails")
	}
	if registry.ConnLimiterRegistry().IsRegistered(name2) {
		t.Fatal("conn limiter must be unregistered after persist failure")
	}
}

func TestFix212UpdateLimiterPersistFailureRollsBack(t *testing.T) {
	resetConfig(t, &config.Config{})
	name := "w4-limiter-update-rollback"
	t.Cleanup(func() { registry.TrafficLimiterRegistry().Unregister(name) })
	if err := createLimiter(createLimiterRequest{Data: config.LimiterConfig{Name: name, Limits: []string{"10MB"}}}); err != nil {
		t.Fatalf("setup createLimiter: %v", err)
	}
	forcePersistFailure(t)
	if err := updateLimiter(updateLimiterRequest{Limiter: name, Data: config.LimiterConfig{Name: name, Limits: []string{"99MB"}}}); err == nil {
		t.Fatal("updateLimiter must fail when persist fails")
	}
	if !registry.TrafficLimiterRegistry().IsRegistered(name) {
		t.Fatal("traffic limiter must remain registered after persist failure")
	}
	cfg := config.Global()
	if len(cfg.Limiters) != 1 || len(cfg.Limiters[0].Limits) != 1 || cfg.Limiters[0].Limits[0] != "10MB" {
		t.Fatalf("config must keep the pre-update limiter, got %+v", cfg.Limiters)
	}
}

func TestFix212DeleteServicesPersistFailureRollsBack(t *testing.T) {
	name := "w4-service-rollback"
	svcCfg := &config.ServiceConfig{Name: name, Addr: "127.0.0.1:0"}
	resetConfig(t, &config.Config{Services: []*config.ServiceConfig{svcCfg}})
	stub := &recordingService{}
	registry.ServiceRegistry().Unregister(name)
	t.Cleanup(func() { registry.ServiceRegistry().Unregister(name) })
	if err := registry.ServiceRegistry().Register(name, stub); err != nil {
		t.Fatalf("register stub: %v", err)
	}
	forcePersistFailure(t)
	if err := deleteServices(deleteServicesRequest{Services: []string{name}}); err == nil {
		t.Fatal("deleteServices must fail when persist fails")
	}
	cfg := config.Global()
	found := false
	for _, s := range cfg.Services {
		if s != nil && s.Name == name {
			found = true
		}
	}
	if !found {
		t.Fatal("in-memory config must still list the service after persist failure")
	}
	if registry.ServiceRegistry().Get(name) == nil {
		t.Fatal("service runtime must be restored (re-parsed) after persist failure")
	}
}

// --- F-A02 ---

func TestFix212DecodeUpdatePayloadShapes(t *testing.T) {
	name, raw, err := decodeUpdatePayload([]byte(`{"limiter":"l1","data":{"name":"l1"}}`), "limiter")
	if err != nil || name != "l1" || !strings.Contains(string(raw), `"l1"`) {
		t.Fatalf("wrapper shape: name=%q raw=%s err=%v", name, raw, err)
	}
	if _, _, err := decodeUpdatePayload([]byte(`{"name":"l1","limits":["1MB"]}`), "limiter"); err == nil {
		t.Fatal("bare shape must be rejected")
	} else if !strings.Contains(err.Error(), "包装形状") {
		t.Fatalf("rejection must name the required wrapper shape, got: %v", err)
	}
}

func TestFix212HandleUpdateLimiterBareShapeRejected(t *testing.T) {
	w := &WebSocketReporter{}
	err := w.handleUpdateLimiter(map[string]interface{}{"name": "w4-bare", "limits": []interface{}{"1MB"}})
	if err == nil {
		t.Fatal("bare-shape UpdateLimiters must be rejected, not silently applied")
	}
	if registry.TrafficLimiterRegistry().IsRegistered("") {
		t.Fatal("an empty-named limiter must never be registered")
	}
}

func TestFix212UpdateLimiterEmptyNameRejected(t *testing.T) {
	if err := updateLimiter(updateLimiterRequest{}); err == nil {
		t.Fatal("updateLimiter with no name anywhere must be rejected")
	}
	if err := updateConnLimiter(updateLimiterRequest{}); err == nil {
		t.Fatal("updateConnLimiter with no name anywhere must be rejected")
	}
}

// --- F-A03 / F-A04 ---

func TestFix212DurationPreprocessAllFields(t *testing.T) {
	w := &WebSocketReporter{}
	out, err := w.preprocessDurationFields([]byte(`{"name":"l","reload":"2s","selector":{"failTimeout":"5s"},"hops":[{"reload":"1s"}],"metadata":{"timeout":"keep-me"}}`))
	if err != nil {
		t.Fatalf("preprocess: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["reload"] != float64(int64(2*time.Second)) {
		t.Fatalf("reload not converted: %v", m["reload"])
	}
	if m["selector"].(map[string]interface{})["failTimeout"] != float64(int64(5*time.Second)) {
		t.Fatalf("failTimeout not converted: %v", m["selector"])
	}
	if m["hops"].([]interface{})[0].(map[string]interface{})["reload"] != float64(int64(time.Second)) {
		t.Fatalf("hop reload not converted: %v", m["hops"])
	}
	if m["metadata"].(map[string]interface{})["timeout"] != "keep-me" {
		t.Fatalf("metadata subtree must be exempt, got %v", m["metadata"])
	}
}

func TestFix212DurationPreprocessBadValueNamesField(t *testing.T) {
	w := &WebSocketReporter{}
	_, err := w.preprocessDurationFields([]byte(`{"name":"l","reload":"not-a-duration"}`))
	if err == nil {
		t.Fatal("unparsable duration must fail the whole payload")
	}
	if !strings.Contains(err.Error(), "reload") {
		t.Fatalf("error must name the field path, got: %v", err)
	}
}

func TestFix212HandleAddLimiterStringReload(t *testing.T) {
	resetConfig(t, &config.Config{})
	name := "w4-limiter-string-reload"
	t.Cleanup(func() { registry.TrafficLimiterRegistry().Unregister(name) })
	w := &WebSocketReporter{}
	err := w.handleAddLimiter(map[string]interface{}{"name": name, "limits": []interface{}{"10MB"}, "reload": "2s"})
	if err != nil {
		t.Fatalf("string reload must be accepted after normalization: %v", err)
	}
	cfg := config.Global()
	if len(cfg.Limiters) != 1 || cfg.Limiters[0].Reload != 2*time.Second {
		t.Fatalf("typed Reload must be 2s, got %+v", cfg.Limiters)
	}
}

// --- F-A05 ---

func TestFix212NotFoundContract(t *testing.T) {
	resetConfig(t, &config.Config{})
	// Delete* on missing objects: idempotent success.
	if err := deleteChain(deleteChainRequest{Chain: "w4-missing"}); err != nil {
		t.Fatalf("deleteChain(missing) must succeed: %v", err)
	}
	if err := deleteLimiter(deleteLimiterRequest{Limiter: "w4-missing"}); err != nil {
		t.Fatalf("deleteLimiter(missing) must succeed: %v", err)
	}
	if err := deleteConnLimiter(deleteLimiterRequest{Limiter: "w4-missing"}); err != nil {
		t.Fatalf("deleteConnLimiter(missing) must succeed: %v", err)
	}
	if err := deleteServices(deleteServicesRequest{Services: []string{"w4-missing"}}); err != nil {
		t.Fatalf("deleteServices(missing) must succeed: %v", err)
	}
	// Pause/Resume on missing objects: strict, exact substring.
	if err := pauseServices(pauseServicesRequest{Services: []string{"w4-missing"}}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("pauseServices(missing) must fail with 'not found', got: %v", err)
	}
	if err := resumeServices(resumeServicesRequest{Services: []string{"w4-missing"}}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("resumeServices(missing) must fail with 'not found', got: %v", err)
	}
}

// --- B-01 / B-03 / B-05 / B-06 ---

func TestFix212WireShapeDeadFields(t *testing.T) {
	b, err := json.Marshal(ServiceMonitorCheckResult{MonitorID: 1, Success: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "statusCode") {
		t.Fatalf("SMC result must not carry statusCode: %s", b)
	}
	b, err = json.Marshal(TcpPingRequest{IP: "1.1.1.1", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "requestId") {
		t.Fatalf("TcpPingRequest must not carry requestId: %s", b)
	}
	res := stampPingResult(TcpPingResponse{ErrorMessage: "boom"}, "req-9")
	if res.RequestId != "req-9" || res.Message != "boom" {
		t.Fatalf("stampPingResult: %+v", res)
	}
	if got := latencyMsFloat(1500 * time.Microsecond); got != 1.5 {
		t.Fatalf("latencyMsFloat(1.5ms) = %v", got)
	}
	if got := latencyMsFloat(300 * time.Microsecond); got != 0.3 {
		t.Fatalf("sub-ms latency must not truncate to 0, got %v", got)
	}
	// B-04 (TuneProbe half): the retired suggested vocabulary is
	// no longer serialized.
	b, err = json.Marshal(tuneProbeResult{Suggested: tuneParams{BandwidthMbps: 1000, RTTMs: 150}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "suggested") {
		t.Fatalf("probe result must not carry suggested: %s", b)
	}
}

// --- B-07 ---

func TestFix212UdpPingHonorsCancelledCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, _, err := udpPingHost(ctx, "8.8.8.8", 53, 10, 5000)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ctx must surface as the result, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("udpPingHost ignored the cancelled ctx and ran the full series")
	}
	w := &WebSocketReporter{}
	if _, err := w.handleUdpPing(ctx, map[string]interface{}{"ip": "8.8.8.8", "port": 53, "count": 2, "timeout": 500}); err == nil {
		t.Fatal("handleUdpPing with cancelled ctx must return a command error")
	}
}

// --- C4-G2 ---

func stubUninstallStatePath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "flmx_agent")
	if err := os.WriteFile(bin, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := uninstallStatePath
	uninstallStatePath = func() (string, error) { return filepath.Join(dir, "agent-uninstall.state.json"), nil }
	t.Cleanup(func() { uninstallStatePath = orig })
	return dir
}

func TestFix212UninstallDuplicateDispatchOnlyReReports(t *testing.T) {
	w4Setup()
	stubUninstallStatePath(t)
	saveUninstallState(&uninstallState{Key: "k1", RequestID: "k1", State: "succeeded", Reported: true})
	if uninstallStarted.Load() {
		t.Skip("uninstall already started by an earlier test in this process")
	}
	w := &WebSocketReporter{}
	// Same issuance key (k1) as the stored final state: duplicate.
	data, err := w.handleUninstallAgent(nil, "k1")
	if err != nil {
		t.Fatalf("duplicate dispatch ack: %v", err)
	}
	m, ok := data.(map[string]interface{})
	if !ok || m["alreadyFinal"] != true || m["state"] != "succeeded" {
		t.Fatalf("duplicate dispatch must report the stored final state, got %+v", data)
	}
	if uninstallStarted.Load() {
		t.Fatal("duplicate dispatch must not start a second execution")
	}
}

// C4-G2 幂等键作用域（R9-06 定源后补的同族成员）：已存终局的
// 键与新签发的键不同时，新签发必须执行——旧终局不得吞掉面板
// 的一次新卸载签发（重试须发新 issued_at）。
func TestFix212UninstallNewKeyExecutesAfterStoredFinal(t *testing.T) {
	w4Setup()
	dir := stubUninstallStatePath(t)
	saveUninstallState(&uninstallState{Key: "k1", RequestID: "k1", State: "succeeded", Reported: true})
	// Script-form uninstall requires the install layout
	// <dir>/<name>/<name> (buildAgentUninstallScript rejects any
	// other directory shape); mirror the sibling execution test.
	// (stubUninstallStatePath already owns dir/flmx_agent as a
	// file, so nest the install tree one level deeper.)
	installDir := filepath.Join(dir, "home", "flmx_agent")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(installDir, "flmx_agent")
	if err := os.WriteFile(binPath, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubUninstallHooks(t, binPath, false)
	origAckWait, origAttempts := uninstallAckWaitTimeout, uninstallReportAttempts
	uninstallAckWaitTimeout = 50 * time.Millisecond
	uninstallReportAttempts = 1
	t.Cleanup(func() { uninstallAckWaitTimeout, uninstallReportAttempts = origAckWait, origAttempts })
	uninstallStarted.Store(false)
	t.Cleanup(func() { uninstallStarted.Store(false) })

	w := &WebSocketReporter{}
	// Defensive-compat leg (R2-ISSUEDAT): an RFC3339 string still
	// parses, but the key is canonicalized to decimal UnixMilli.
	data, err := w.handleUninstallAgent(map[string]interface{}{"issuedAt": "2026-11-10T01:00:00Z"}, "req-new")
	if err != nil {
		t.Fatalf("handleUninstallAgent: %v", err)
	}
	if m := data.(map[string]interface{}); m["state"] != "acked" || m["alreadyFinal"] == true {
		t.Fatalf("new issuance key must start a fresh uninstall, got %+v", data)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if st, ok := loadUninstallState(); ok && st.State == "succeeded" {
			if st.Key != "1794272400000" {
				t.Fatalf("final state key = %q, want the new issuance key in decimal UnixMilli", st.Key)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("new-key uninstall did not reach a final state")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFix212UninstallExecutesAfterAckTimeoutAndPersistsFinal(t *testing.T) {
	w4Setup()
	root := t.TempDir()
	installDir := filepath.Join(root, "flmx_agent")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(installDir, "flmx_agent")
	if err := os.WriteFile(binPath, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := stubUninstallHooks(t, binPath, false)
	_ = s
	origAckWait, origAttempts := uninstallAckWaitTimeout, uninstallReportAttempts
	uninstallAckWaitTimeout = 50 * time.Millisecond
	uninstallReportAttempts = 1
	t.Cleanup(func() { uninstallAckWaitTimeout, uninstallReportAttempts = origAckWait, origAttempts })
	uninstallStarted.Store(false)
	t.Cleanup(func() { uninstallStarted.Store(false) })

	w := &WebSocketReporter{}
	// Production shape (R2-ISSUEDAT): the panel dispatches a JSON
	// number (UnixMilli), never an RFC3339 string.
	data, err := w.handleUninstallAgent(map[string]interface{}{"issuedAt": int64(1794268800000)}, "req-g2")
	if err != nil {
		t.Fatalf("handleUninstallAgent: %v", err)
	}
	if m := data.(map[string]interface{}); m["uninstallKey"] != "1794268800000" || m["state"] != "acked" {
		t.Fatalf("ack data: %+v", data)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if st, ok := loadUninstallState(); ok && (st.State == "succeeded" || st.State == "failed") {
			if st.State != "succeeded" {
				t.Fatalf("final state = %q (failedStep %q, steps %+v)", st.State, st.FailedStep, st.Steps)
			}
			if st.Key != "1794268800000" {
				t.Fatalf("idempotency key = %q", st.Key)
			}
			if len(st.Steps) == 0 {
				t.Fatal("final state must carry the judged steps")
			}
			if st.Reported {
				t.Fatal("no connection exists in this test; Reported must stay false for the reconnect re-send")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("uninstall did not reach a final state")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFix212UninstallAckFlushGatesExecution(t *testing.T) {
	w4Setup()
	root := t.TempDir()
	installDir := filepath.Join(root, "flmx_agent")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(installDir, "flmx_agent")
	if err := os.WriteFile(binPath, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubUninstallHooks(t, binPath, false)
	origAckWait, origAttempts := uninstallAckWaitTimeout, uninstallReportAttempts
	uninstallAckWaitTimeout = 30 * time.Second // long: only the ack signal may release execution
	uninstallReportAttempts = 1
	t.Cleanup(func() { uninstallAckWaitTimeout, uninstallReportAttempts = origAckWait, origAttempts })
	uninstallStarted.Store(false)
	t.Cleanup(func() { uninstallStarted.Store(false) })

	w := &WebSocketReporter{}
	if _, err := w.handleUninstallAgent(nil, "req-ack"); err != nil {
		t.Fatalf("handleUninstallAgent: %v", err)
	}
	// Execution must still be parked on the ack waiter.
	if st, ok := loadUninstallState(); !ok || st.State != "acked" {
		t.Fatalf("before ack flush the state must stay acked, got %+v (ok=%v)", st, ok)
	}
	start := time.Now()
	w.signalUninstallAck("req-ack", true)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if st, ok := loadUninstallState(); ok && st.State == "succeeded" {
			if !st.AckFlushed {
				t.Fatal("AckFlushed must be recorded true when the ack signal released execution")
			}
			if time.Since(start) > 10*time.Second {
				t.Fatal("execution ignored the ack signal and waited for the timeout")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("uninstall did not complete after the ack signal")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFix212FlushConvertsStaleExecutingToFailed(t *testing.T) {
	w4Setup()
	stubUninstallStatePath(t)
	uninstallStarted.Store(false)
	origAttempts := uninstallReportAttempts
	uninstallReportAttempts = 1
	t.Cleanup(func() { uninstallReportAttempts = origAttempts })
	saveUninstallState(&uninstallState{Key: "k9", State: "executing"})
	w := &WebSocketReporter{}
	w.flushPendingUninstallResult()
	st, ok := loadUninstallState()
	if !ok {
		t.Fatal("state file must exist")
	}
	if st.State != "failed" || st.FailedStep != "interrupted" {
		t.Fatalf("stale executing must converge to failed/interrupted, got %+v", st)
	}
}

// --- S1-F01 agent side ---

func TestFix212LimiterIdentityIsNameOnly(t *testing.T) {
	resetConfig(t, &config.Config{})
	name := "w4-shared-name"
	t.Cleanup(func() {
		registry.TrafficLimiterRegistry().Unregister(name)
		registry.ConnLimiterRegistry().Unregister(name)
	})
	// The same name lives independently in both registries: the
	// agent has no node/tunnel dimension on limiter identity.
	if err := createLimiter(createLimiterRequest{Data: config.LimiterConfig{Name: name}}); err != nil {
		t.Fatal(err)
	}
	if err := createConnLimiter(createLimiterRequest{Data: config.LimiterConfig{Name: name}}); err != nil {
		t.Fatal(err)
	}
	if err := deleteLimiter(deleteLimiterRequest{Limiter: name}); err != nil {
		t.Fatal(err)
	}
	if registry.TrafficLimiterRegistry().IsRegistered(name) {
		t.Fatal("traffic limiter must be gone after delete-by-name")
	}
	if !registry.ConnLimiterRegistry().IsRegistered(name) {
		t.Fatal("conn limiter of the same name must be untouched by the traffic delete")
	}
}

// --- smoke version comparison ---

func TestFix212AgentVersionsEqual(t *testing.T) {
	if !agentVersionsEqual("2.1.2", "v2.1.2") || !agentVersionsEqual("v2.1.2", "2.1.2") {
		t.Fatal("leading v must be tolerated on either side")
	}
	if agentVersionsEqual("2.1.1", "2.1.2") {
		t.Fatal("different versions must not compare equal")
	}
}

// --- C4-low Go verify self-report ---

// The handshake must carry the running binary's SHA-256 (binSha)
// so the panel's verify ring can compare the claimed version
// against the actual running asset instead of trusting the
// self-reported version string alone. Pins the producer side;
// panel-side consumption (ExpectedSHA comparison) is the backend
// half of the same item.
func TestFix212RunningBinarySHAIsWellFormed(t *testing.T) {
	sha := runningBinarySHA()
	if len(sha) != 64 {
		t.Fatalf("runningBinarySHA must be a 64-char hex digest, got %q", sha)
	}
	for _, c := range sha {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			t.Fatalf("runningBinarySHA must be lowercase hex, got %q", sha)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want, err := hashFileSHA256(exe)
	if err != nil {
		t.Fatal(err)
	}
	if sha != want {
		t.Fatalf("runningBinarySHA %q != recomputed digest %q", sha, want)
	}
	cands := buildWebSocketCandidates("panel.example.com:443", "s3cr3t", "2.1.3", 1, 0, 1, "wss")
	if len(cands) == 0 {
		t.Fatal("no handshake candidates")
	}
	u, err := url.Parse(cands[0] + "&binSha=" + url.QueryEscape(sha))
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("binSha") != sha {
		t.Fatalf("binSha query round-trip failed: %q", u.String())
	}
}
