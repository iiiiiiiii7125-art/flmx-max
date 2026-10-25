package handler

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

var errTestR17B = errors.New("r17b fake send failure")

// R17-B：面板侧 AddService 差集补发幂等守卫回归测试。
//
// 先红后绿：本文件引用的守卫 API（guardedAddService /
// noteAddServiceConfirmed / invalidateAddServiceNames /
// invalidateAddServiceNode / r17AddServiceLogf）在守卫落地前不
// 存在，go test 应以编译失败变红；守卫落地后全绿。
//
// 判定口径（与 add_service_guard.go 注释同文）：
//   - 已确认注册态 = agent 对 AddService/UpdateService 的成功响应
//     建立的确认，无 TTL、无时间窗；
//   - 新鲜否定证据（本次 UpdateService not-found、DeleteService
//     成功、节点离线）立即作废确认，绝不拦一次真正需要的补发；
//   - 同一 (node, 服务名集合) 在途 AddService 只发一次，后到者
//     等待在途结果（join），在途成功则跳过、失败则自行补发；
//   - 超时/失败/无响应不记确认、不跳过（fail-closed）。

func r17bTestServices() []map[string]interface{} {
	return []map[string]interface{}{
		{"name": "6_1_0_tcp", "addr": "127.0.0.1:50001"},
		{"name": "6_1_0_udp", "addr": "127.0.0.1:50001"},
	}
}

func r17bTestNames() []string { return []string{"6_1_0_tcp", "6_1_0_udp"} }

type r17bFakeSender struct {
	mu       sync.Mutex
	calls    int
	err      error
	entered  chan struct{}
	release  chan struct{}
	enteredF bool
}

func (f *r17bFakeSender) send(nodeID int64, services []map[string]interface{}) error {
	f.mu.Lock()
	f.calls++
	if f.entered != nil && !f.enteredF {
		f.enteredF = true
		close(f.entered)
	}
	release := f.release
	err := f.err
	f.mu.Unlock()
	if release != nil {
		<-release
	}
	return err
}

func (f *r17bFakeSender) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type r17bLogRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *r17bLogRecorder) logf(format string, args ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *r17bLogRecorder) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

func r17bNewHandler(t *testing.T, sender *r17bFakeSender) (*Handler, *r17bLogRecorder) {
	t.Helper()
	h := &Handler{}
	if sender != nil {
		h.addSvcSend = sender.send
	}
	rec := &r17bLogRecorder{}
	old := r17AddServiceLogf
	r17AddServiceLogf = rec.logf
	t.Cleanup(func() { r17AddServiceLogf = old })
	return h, rec
}

// 同一服务在 agent 已确认注册态下不再补发。
func TestR17B_GuardSkipsWhenAgentConfirmed(t *testing.T) {
	sender := &r17bFakeSender{}
	h, rec := r17bNewHandler(t, sender)
	h.noteAddServiceConfirmed(7, r17bTestNames())

	skipped, err := h.guardedAddService(7, r17bTestServices(), "r17b-test-confirmed", false)
	if err != nil {
		t.Fatalf("guardedAddService err = %v, want nil", err)
	}
	if !skipped {
		t.Fatalf("guardedAddService skipped = false, want true (agent-confirmed)")
	}
	if got := sender.callCount(); got != 0 {
		t.Fatalf("sender calls = %d, want 0 (must not resend a confirmed service)", got)
	}
	logged := rec.joined()
	for _, want := range []string{"skip-confirmed", "6_1_0_tcp", "6_1_0_udp"} {
		if !strings.Contains(logged, want) {
			t.Errorf("guard log %q missing %q", logged, want)
		}
	}
}

// 未注册态下补发一次。
func TestR17B_GuardSendsOnceWhenUnregistered(t *testing.T) {
	sender := &r17bFakeSender{}
	h, rec := r17bNewHandler(t, sender)

	skipped, err := h.guardedAddService(7, r17bTestServices(), "r17b-test-missing", false)
	if err != nil {
		t.Fatalf("guardedAddService err = %v, want nil", err)
	}
	if skipped {
		t.Fatalf("guardedAddService skipped = true, want false (unregistered must send once)")
	}
	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1", got)
	}
	if logged := rec.joined(); !strings.Contains(logged, "decision=send") || !strings.Contains(logged, "6_1_0_tcp") {
		t.Errorf("guard log must record the send decision with service name, got %q", logged)
	}
}

// 补发后复核发现已存在时不再重复补发：第一次发送成功即记确认，
// 第二次（无新鲜缺失证据）直接跳过。
func TestR17B_GuardRecheckSkipsAfterSend(t *testing.T) {
	sender := &r17bFakeSender{}
	h, _ := r17bNewHandler(t, sender)

	if skipped, err := h.guardedAddService(7, r17bTestServices(), "r17b-test-first", false); err != nil || skipped {
		t.Fatalf("first guardedAddService skipped=%v err=%v, want skipped=false err=nil", skipped, err)
	}
	if skipped, err := h.guardedAddService(7, r17bTestServices(), "r17b-test-recheck", false); err != nil || !skipped {
		t.Fatalf("recheck guardedAddService skipped=%v err=%v, want skipped=true err=nil", skipped, err)
	}
	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1 (recheck must not resend)", got)
	}
}

// 新鲜否定证据压过旧确认：UpdateService 刚报 not-found 时，
// 即使确认集里有该服务，也必须补发（fail-closed）。
func TestR17B_GuardFreshNotFoundInvalidatesConfirmed(t *testing.T) {
	sender := &r17bFakeSender{}
	h, _ := r17bNewHandler(t, sender)
	h.noteAddServiceConfirmed(7, r17bTestNames())

	skipped, err := h.guardedAddService(7, r17bTestServices(), "r17b-test-notfound", true)
	if err != nil {
		t.Fatalf("guardedAddService err = %v, want nil", err)
	}
	if skipped {
		t.Fatalf("guardedAddService skipped = true with fresh not-found, want false (must resend)")
	}
	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1", got)
	}
}

// 并发重复补发只发一次：第二路等待在途结果（join），在途成功则跳过。
func TestR17B_GuardConcurrentDuplicateJoinsInFlight(t *testing.T) {
	sender := &r17bFakeSender{entered: make(chan struct{}), release: make(chan struct{})}
	h, _ := r17bNewHandler(t, sender)

	firstDone := make(chan struct{})
	var firstSkipped bool
	var firstErr error
	go func() {
		defer close(firstDone)
		firstSkipped, firstErr = h.guardedAddService(7, r17bTestServices(), "r17b-test-race-first", false)
	}()
	select {
	case <-sender.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first AddService was not issued within 5s")
	}

	secondDone := make(chan struct{})
	var secondSkipped bool
	var secondErr error
	go func() {
		defer close(secondDone)
		secondSkipped, secondErr = h.guardedAddService(7, r17bTestServices(), "r17b-test-race-second", false)
	}()
	// 第二路必须在 join，不另发：给它一点时间，sender 调用数仍为 1。
	time.Sleep(200 * time.Millisecond)
	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls during in-flight join = %d, want 1 (duplicate must wait, not send)", got)
	}
	close(sender.release)

	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first guardedAddService did not return")
	}
	select {
	case <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("second guardedAddService did not return after in-flight completed")
	}
	if firstErr != nil || firstSkipped {
		t.Errorf("first call skipped=%v err=%v, want skipped=false err=nil", firstSkipped, firstErr)
	}
	if secondErr != nil || !secondSkipped {
		t.Errorf("second call skipped=%v err=%v, want skipped=true err=nil (joined in-flight success)", secondSkipped, secondErr)
	}
	if got := sender.callCount(); got != 1 {
		t.Errorf("sender calls = %d, want 1 (concurrent duplicate AddService must collapse to one send)", got)
	}
}

// 发送失败不记确认：下一次仍补发（fail-closed），不因失败留下假确认。
func TestR17B_GuardFailedSendDoesNotConfirm(t *testing.T) {
	sender := &r17bFakeSender{err: errTestR17B}
	h, _ := r17bNewHandler(t, sender)

	if skipped, err := h.guardedAddService(7, r17bTestServices(), "r17b-test-fail", false); err == nil || skipped {
		t.Fatalf("failed guardedAddService skipped=%v err=%v, want skipped=false err!=nil", skipped, err)
	}
	sender.mu.Lock()
	sender.err = nil
	sender.mu.Unlock()
	if skipped, err := h.guardedAddService(7, r17bTestServices(), "r17b-test-retry", false); err != nil || skipped {
		t.Fatalf("retry guardedAddService skipped=%v err=%v, want skipped=false err=nil", skipped, err)
	}
	if got := sender.callCount(); got != 2 {
		t.Fatalf("sender calls = %d, want 2 (failed send must not confirm, retry must send)", got)
	}
}

// DeleteService 成功与节点离线作废确认。
func TestR17B_GuardInvalidateOnDeleteAndOffline(t *testing.T) {
	sender := &r17bFakeSender{}
	h, _ := r17bNewHandler(t, sender)

	h.noteAddServiceConfirmed(7, r17bTestNames())
	h.invalidateAddServiceNames(7, r17bTestNames())
	if skipped, err := h.guardedAddService(7, r17bTestServices(), "r17b-test-after-delete", false); err != nil || skipped {
		t.Fatalf("after delete-invalidate guardedAddService skipped=%v err=%v, want skipped=false err=nil", skipped, err)
	}

	h.noteAddServiceConfirmed(7, r17bTestNames())
	h.invalidateAddServiceNode(7)
	if skipped, err := h.guardedAddService(7, r17bTestServices(), "r17b-test-after-offline", false); err != nil || skipped {
		t.Fatalf("after offline-invalidate guardedAddService skipped=%v err=%v, want skipped=false err=nil", skipped, err)
	}
	if got := sender.callCount(); got != 2 {
		t.Fatalf("sender calls = %d, want 2", got)
	}
}

// 服务名取不到时无法去重，fail-closed：直接发，不跳过。
func TestR17B_GuardEmptyNamesFailClosed(t *testing.T) {
	sender := &r17bFakeSender{}
	h, _ := r17bNewHandler(t, sender)
	nameless := []map[string]interface{}{{"addr": "127.0.0.1:50001"}}

	skipped, err := h.guardedAddService(7, nameless, "r17b-test-noname", false)
	if err != nil || skipped {
		t.Fatalf("nameless guardedAddService skipped=%v err=%v, want skipped=false err=nil", skipped, err)
	}
	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1", got)
	}
}

// 确认集按节点隔离：节点 7 的确认不得让节点 8 跳过补发。
func TestR17B_GuardConfirmedIsPerNode(t *testing.T) {
	sender := &r17bFakeSender{}
	h, _ := r17bNewHandler(t, sender)
	h.noteAddServiceConfirmed(7, r17bTestNames())

	if skipped, err := h.guardedAddService(8, r17bTestServices(), "r17b-test-other-node", false); err != nil || skipped {
		t.Fatalf("other-node guardedAddService skipped=%v err=%v, want skipped=false err=nil", skipped, err)
	}
	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1", got)
	}
}

// R3-GATE-F3 / R4B6 回归：setup 期 UpdateService 成功留下的确认，
// 不得让一次刚失败的 UpdateService 后的补发被跳过——失败文本不是
// not-found（bind 失败、超时等）时同样必须补发，否则调用方会把
// 失败误判为成功。回退路径因此一律传 freshAbsence=true。
func TestR17B_GuardFailedUpdateInvalidatesConfirmedEvenWithoutNotFound(t *testing.T) {
	sender := &r17bFakeSender{}
	h, _ := r17bNewHandler(t, sender)
	// setup 期：UpdateService 成功，服务已确认。
	h.noteAddServiceConfirmed(7, r17bTestNames())
	// 切换期：UpdateService 以 bind 错误失败（非 not-found 文本），
	// 回退 AddService 必须发出，不得用旧确认跳过。
	if skipped, err := h.guardedAddService(7, r17bTestServices(), "update-fallback", true); err != nil || skipped {
		t.Fatalf("bind-failed fallback guardedAddService skipped=%v err=%v, want skipped=false err=nil", skipped, err)
	}
	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1 (stale confirmation must not suppress a retry after failure)", got)
	}
}
