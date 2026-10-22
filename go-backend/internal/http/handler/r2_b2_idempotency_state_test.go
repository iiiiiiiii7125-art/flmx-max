package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// R2 批 2 · R2X-003 在途预留状态机回归（确定性交错）。
//
// 状态机：absent → in-flight{ownerToken, done, deadline=2min} →
// completed{body, +10min}；失败回 absent 并唤醒等待者；后到者等待
// ≤20s 后回放 / 恰一名 CAS 晋升 / 超时 409；finish 校验 ownerToken。
// 修复前：begin 只查已完成条目，首请求未 finish 时第二请求直接以
// owner 身份返回（executed=2）。

type r2b2BeginResult struct {
	w        http.ResponseWriter
	finish   func()
	replayed bool
	rec      *httptest.ResponseRecorder
}

func r2b2Req(path, key string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	return req
}

func r2b2Begin(h *Handler, userID int64, scope, path, key string) r2b2BeginResult {
	rec := httptest.NewRecorder()
	req := r2b2Req(path, key)
	w, finish, replayed := h.idemBegin(rec, req, userID, scope)
	return r2b2BeginResult{w: w, finish: finish, replayed: replayed, rec: rec}
}

func r2b2BeginAsync(h *Handler, userID int64, scope, path, key string) chan r2b2BeginResult {
	ch := make(chan r2b2BeginResult, 1)
	go func() { ch <- r2b2Begin(h, userID, scope, path, key) }()
	return ch
}

func r2b2ResetIdemStore(t *testing.T) {
	t.Helper()
	idemStore.Lock()
	idemStore.entries = make(map[string]*idemEntry)
	idemStore.Unlock()
	oldWait, oldInflight := idemWaitTimeout, idemInflightTimeout
	t.Cleanup(func() {
		idemWaitTimeout, idemInflightTimeout = oldWait, oldInflight
		idemStore.Lock()
		idemStore.entries = make(map[string]*idemEntry)
		idemStore.Unlock()
	})
}

const (
	r2b2OKBody   = `{"code":0,"msg":"操作成功","ts":1}`
	r2b2OKBody2  = `{"code":0,"msg":"操作成功","ts":2}`
	r2b2FailBody = `{"code":-1,"msg":"校验失败","ts":1}`
)

// 首请求 begin 未 finish 时第二请求 begin：必须等待，首请求成功后
// 回放首结果，执行计数恒 =1（修复前第二请求立即以 owner 返回）。
func TestR2B2_IdemInflightWaiterReplays(t *testing.T) {
	r2b2ResetIdemStore(t)
	h := &Handler{}
	executed := 0

	first := r2b2Begin(h, 7, "forward:create", "/api/v1/forward/create", "state-key-1")
	if first.replayed {
		t.Fatal("first begin must be owner")
	}
	executed++

	waiterCh := r2b2BeginAsync(h, 7, "forward:create", "/api/v1/forward/create", "state-key-1")
	select {
	case res := <-waiterCh:
		t.Fatalf("第二请求在首请求未 finish 时不得返回（修复前缺陷：直接执行）: replayed=%v body=%q", res.replayed, res.rec.Body.String())
	case <-time.After(300 * time.Millisecond):
	}

	_, _ = first.w.Write([]byte(r2b2OKBody))
	first.finish()

	select {
	case res := <-waiterCh:
		if !res.replayed {
			t.Fatal("等待者应在首请求成功后回放，而不是执行")
		}
		if res.rec.Body.String() != r2b2OKBody {
			t.Fatalf("回放 body = %q, want %q", res.rec.Body.String(), r2b2OKBody)
		}
		res.finish()
	case <-time.After(5 * time.Second):
		t.Fatal("等待者在首请求 finish 后未返回")
	}
	if executed != 1 {
		t.Fatalf("executed = %d, want 1", executed)
	}
}

// 首请求失败释放占位：两名等待者中恰一名晋升 owner 执行，另一名
// 继续等待并在晋升者成功后回放。
func TestR2B2_IdemFailureReleasePromotesExactlyOne(t *testing.T) {
	r2b2ResetIdemStore(t)
	h := &Handler{}
	executed := 0

	first := r2b2Begin(h, 7, "forward:create", "/api/v1/forward/create", "state-key-2")
	executed++
	w1 := r2b2BeginAsync(h, 7, "forward:create", "/api/v1/forward/create", "state-key-2")
	w2 := r2b2BeginAsync(h, 7, "forward:create", "/api/v1/forward/create", "state-key-2")
	// 让两名等待者都进入等待。
	time.Sleep(200 * time.Millisecond)

	_, _ = first.w.Write([]byte(r2b2FailBody))
	first.finish() // 失败：释放占位并唤醒

	var promoted r2b2BeginResult
	var otherCh chan r2b2BeginResult
	select {
	case res := <-w1:
		promoted, otherCh = res, w2
	case res := <-w2:
		promoted, otherCh = res, w1
	case <-time.After(5 * time.Second):
		t.Fatal("失败释放后无等待者晋升")
	}
	if promoted.replayed {
		t.Fatal("晋升者应为 owner（replayed=false）")
	}
	executed++
	select {
	case res := <-otherCh:
		t.Fatalf("第二名等待者不得同时晋升: replayed=%v", res.replayed)
	case <-time.After(300 * time.Millisecond):
	}

	_, _ = promoted.w.Write([]byte(r2b2OKBody2))
	promoted.finish()
	select {
	case res := <-otherCh:
		if !res.replayed || res.rec.Body.String() != r2b2OKBody2 {
			t.Fatalf("剩余等待者应回放晋升者结果: replayed=%v body=%q", res.replayed, res.rec.Body.String())
		}
		res.finish()
	case <-time.After(5 * time.Second):
		t.Fatal("剩余等待者未在晋升者 finish 后返回")
	}
	if executed != 2 {
		t.Fatalf("executed = %d, want 2（首失败 + 恰一名晋升）", executed)
	}
}

// 占位过期被接管后，旧 owner 的 finish 不得覆写新 owner 的结果。
func TestR2B2_IdemStaleFinishCannotOverwrite(t *testing.T) {
	r2b2ResetIdemStore(t)
	idemInflightTimeout = 80 * time.Millisecond
	h := &Handler{}

	oldOwner := r2b2Begin(h, 7, "forward:create", "/api/v1/forward/create", "state-key-3")
	if oldOwner.replayed {
		t.Fatal("first begin must be owner")
	}
	time.Sleep(150 * time.Millisecond) // 占位 deadline 已过

	newOwner := r2b2Begin(h, 7, "forward:create", "/api/v1/forward/create", "state-key-3")
	if newOwner.replayed {
		t.Fatal("占位过期后第二请求应接管为 owner")
	}

	_, _ = oldOwner.w.Write([]byte(r2b2OKBody))
	oldOwner.finish() // 旧 finish：ownerToken 已失效，必须是 no-op

	_, _ = newOwner.w.Write([]byte(r2b2OKBody2))
	newOwner.finish()

	third := r2b2Begin(h, 7, "forward:create", "/api/v1/forward/create", "state-key-3")
	if !third.replayed || third.rec.Body.String() != r2b2OKBody2 {
		t.Fatalf("回放应为新 owner 结果: replayed=%v body=%q", third.replayed, third.rec.Body.String())
	}
	third.finish()
}

// 等待超过上限（生产 20s，测试注入短值）→ 409「相同请求正在处理中」，
// 不得执行、不得回 code=0 假成功。
func TestR2B2_IdemWaitTimeout409(t *testing.T) {
	r2b2ResetIdemStore(t)
	idemWaitTimeout = 250 * time.Millisecond
	h := &Handler{}

	first := r2b2Begin(h, 7, "forward:create", "/api/v1/forward/create", "state-key-4")
	if first.replayed {
		t.Fatal("first begin must be owner")
	}
	second := r2b2Begin(h, 7, "forward:create", "/api/v1/forward/create", "state-key-4")
	if !second.replayed {
		t.Fatal("超时等待者不得成为 owner 执行")
	}
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(second.rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("409 回执不是合法信封: %v (%q)", err, second.rec.Body.String())
	}
	if env.Code != 409 {
		t.Fatalf("超时回执 code = %d, want 409 (body=%s)", env.Code, second.rec.Body.String())
	}
	second.finish()
	_, _ = first.w.Write([]byte(r2b2OKBody))
	first.finish()
}

// scope 维度（R2X-032 单元面）：同键同路径、scope 不同 → 各自执行；
// 同 scope 同键、路径不同（method+path 双保险）→ 各自执行；完全同
// (user, scope, method+path, key) → 回放。
func TestR2B2_IdemScopeAndPathDimensions(t *testing.T) {
	r2b2ResetIdemStore(t)
	h := &Handler{}

	a := r2b2Begin(h, 7, "forward:create", "/api/v1/forward/create", "state-key-5")
	_, _ = a.w.Write([]byte(r2b2OKBody))
	a.finish()

	b := r2b2Begin(h, 7, "node:create", "/api/v1/forward/create", "state-key-5")
	if b.replayed {
		t.Fatal("同键异 scope 不得回放")
	}
	_, _ = b.w.Write([]byte(r2b2OKBody2))
	b.finish()

	c := r2b2Begin(h, 7, "forward:create", "/api/v1/node/create", "state-key-5")
	if c.replayed {
		t.Fatal("同键同 scope 异路径不得回放（method+path 双保险）")
	}
	_, _ = c.w.Write([]byte(r2b2OKBody2))
	c.finish()

	d := r2b2Begin(h, 7, "forward:create", "/api/v1/forward/create", "state-key-5")
	if !d.replayed || d.rec.Body.String() != r2b2OKBody {
		t.Fatalf("完全同键应回放首次结果: replayed=%v body=%q", d.replayed, d.rec.Body.String())
	}
	d.finish()

	e := r2b2Begin(h, 8, "forward:create", "/api/v1/forward/create", "state-key-5")
	if e.replayed {
		t.Fatal("异用户同键不得回放")
	}
	_, _ = e.w.Write([]byte(r2b2FailBody))
	e.finish()
}
