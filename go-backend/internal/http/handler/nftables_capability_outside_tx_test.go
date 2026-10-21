package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimenft "go-backend/internal/runtime/nftables"
)

// A3-01 回归：nftables SSH 能力校验（manager.Test）不得在 DB 事务或
// 端口分配锁内执行。旧实现把 Test 放在隧道创建的验证/提交事务内与
// 隧道更新的持锁验证段内：SQLite MaxOpenConns(1) 下事务独占唯一连接，
// SSH 慢/超时期间全站 DB 读写排队。本测试用阻塞型 fake manager：
// 当 Test 被调用且未释放时，并发发起一个无关的 DB 写，必须能完成。

type blockingNftablesManager struct {
	testCalls   atomic.Int64
	testEntered chan struct{}
	testRelease chan struct{}
	enteredOnce sync.Once
}

func newBlockingNftablesManager() *blockingNftablesManager {
	return &blockingNftablesManager{
		testEntered: make(chan struct{}),
		testRelease: make(chan struct{}),
	}
}

func (m *blockingNftablesManager) Test(_ context.Context, _ runtimenft.SSHConfig) error {
	m.testCalls.Add(1)
	m.enteredOnce.Do(func() { close(m.testEntered) })
	<-m.testRelease
	return nil
}

func (m *blockingNftablesManager) Reconcile(_ context.Context, cfg runtimenft.SSHConfig, plan runtimenft.NodePlan) (runtimenft.ApplyResult, error) {
	return runtimenft.ApplyResult{NodeID: plan.NodeID, Script: "table inet flvx {}", Hashes: runtimenft.PlanHashes(plan)}, nil
}

func (m *blockingNftablesManager) Clear(_ context.Context, _ runtimenft.SSHConfig) error {
	return nil
}

func (m *blockingNftablesManager) CollectCounters(_ context.Context, _ runtimenft.SSHConfig) ([]runtimenft.CounterSample, error) {
	return nil, nil
}

// runWithBlockedNftTest 在 manager.Test 阻塞期间验证并发 DB 写不被阻塞，
// 然后释放 Test 并等待请求完成。返回 manager.Test 的总调用次数。
func runWithBlockedNftTest(t *testing.T, h *Handler, manager *blockingNftablesManager, invoke func()) int64 {
	t.Helper()
	released := false
	release := func() {
		if !released {
			released = true
			close(manager.testRelease)
		}
	}
	defer release()

	done := make(chan struct{})
	go func() {
		defer close(done)
		invoke()
	}()

	select {
	case <-manager.testEntered:
	case <-time.After(15 * time.Second):
		t.Fatal("nftables capability Test was never invoked")
	}

	writeDone := make(chan error, 1)
	go func() {
		now := time.Now().UnixMilli()
		_, err := h.repo.CreateUser("a301-probe", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now)
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("concurrent DB write failed while capability Test blocked: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent DB write blocked while nftables capability Test in flight: Test is running inside a DB transaction / port lock (A3-01)")
	}

	release()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("request did not finish after capability Test released")
	}
	return manager.testCalls.Load()
}

func TestA301TunnelCreateRunsNftablesCapabilityTestOutsideTx(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	manager := newBlockingNftablesManager()
	h.nftablesManager = newSerializedNftManager(manager)

	res := httptest.NewRecorder()
	body := bytes.NewReader([]byte(`{
		"name":"a301-create",
		"type":1,
		"flow":1,
		"trafficRatio":1,
		"status":1,
		"inNodeId":[{"nodeId":` + int64JSON(fixture.nodeID) + `,"protocol":"tls"}]
	}`))
	calls := runWithBlockedNftTest(t, h, manager, func() {
		h.tunnelCreate(res, httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/create", body))
	})
	assertNftablesSuccessWithBody(t, res)
	if calls != 1 {
		t.Fatalf("expected capability Test exactly once per create, got %d", calls)
	}
}

func TestA301TunnelUpdateRunsNftablesCapabilityTestOutsideTx(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "a301-update", fixture.nodeID)
	manager := newBlockingNftablesManager()
	h.nftablesManager = newSerializedNftManager(manager)

	res := httptest.NewRecorder()
	body := bytes.NewReader([]byte(`{
		"id":` + int64JSON(tunnelID) + `,
		"name":"a301-update",
		"type":1,
		"flow":1,
		"trafficRatio":1,
		"status":1,
		"inNodeId":[{"nodeId":` + int64JSON(fixture.nodeID) + `,"protocol":"tls"}]
	}`))
	calls := runWithBlockedNftTest(t, h, manager, func() {
		h.tunnelUpdate(res, httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/update", body))
	})
	assertNftablesSuccessWithBody(t, res)
	if calls != 1 {
		t.Fatalf("expected capability Test exactly once per update, got %d", calls)
	}
}

func int64JSON(v int64) string {
	return strconv.FormatInt(v, 10)
}
