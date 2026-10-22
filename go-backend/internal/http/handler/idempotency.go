package handler

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go-backend/internal/http/response"
)

// X-P2-17（L-11-1，BC-13，裁定 V-07）+ R2 批 2 幂等族收口
// （R2X-003/004/032，BC-R2-06）：创建类 POST 的请求级幂等。
//
// 新增创建端点必须同批接入幂等（idemBegin），
// 不得留待后续批次补接。当前覆盖面 12 个创建 handler（scope 常量）：
//
// forwardCreate "forward:create" （mutations.go）
// nodeCreate "node:create" （mutations.go）
// speedLimitCreate "speed-limit:create" （mutations.go）
// subscriptionSave "subscription:create" （仅 ID==0 新建分支）
// inboundSave "inbound:create" （仅新建分支）
// monitorServiceCreate "monitor-service:create" （monitoring.go）
// inboundRelayCreate "inbound-relay:create" （kernel_relay.go）
// federationShareCreate "federation-share:create"（federation.go）
// remoteSourceSave "remote-source:create" （仅 ID==0 新建分支）
// tunnelCreate "tunnel:create" （mutations.go）
// userCreate "user:create" （mutations.go）
// groupCreate "user-group:create" / "tunnel-group:create"
//
// 明确不接：一切 update/delete/assign/set/install/import/split
// （ID 定位或替换语义；kernelInstall 为单例 upsert）。
//
// 语义：请求头 Idempotency-Key 存在时，后端按（用户, scope, 键）缓存
// 首次成功结果 10 分钟；同键重复提交直接回放首次结果，不再执行创建。
// 不带键的调用方行为不变。业务唯一键拒绝不采用——同隧道同目标多
// 入口是合法用法（三元组拒绝会误伤），防重只针对同一次提交的重试/
// 双击。仅缓存 code==0 的成功信封：校验/瞬时错误不落缓存，修正后
// 同键重试仍可真正执行。
//
// R2X-003 在途预留状态机（absent → in-flight → completed）：
// - idemBegin 原子占位：首请求成为 owner（ownerToken + done chan +
// 2min deadline），立即执行；
// - 后到者命中 in-flight 时等待 done（上限 20s，低于前端 30s 超时）：
// 等到后重查——completed 则回放，失败释放则恰好一名等待者 CAS
// 晋升 owner 执行；等待超时回 409「相同请求正在处理中，请稍后
// 重试」（BC-R2-06），不得回 code=0 假成功；
// - 占位 deadline 过期后后到者可接管（防 owner 泄漏挂死同键）；
// - finish 必须校验 ownerToken 才允许转移：仅 code==0 转 completed
// （+10min 过期），失败删占位并唤醒等待者；被接管的旧 finish
// 不得覆写新 owner 的结果。
//
// R2X-032 缓存键：（userID, scope, key），scope 由调用点显式传动作
// 常量，内部再拼 method+path 作双保险——仅 method+path 不够：
// /subscription/save 以 body ID 区分新建/更新、同一 URL 同一 handler，
// 纯 URL 键会让更新回放创建结果（更新分支本就不接入，见上）。
const idempotencyTTL = 10 * time.Minute

// 测试可注入的时限（生产值：等待上限 20s、在途占位 deadline 2min）。
var (
	idemWaitTimeout     = 20 * time.Second
	idemInflightTimeout = 2 * time.Minute
)

type idemState int

const (
	idemInflight idemState = iota
	idemCompleted
)

type idemEntry struct {
	state     idemState
	owner     string
	done      chan struct{}
	doneClose bool
	deadline  time.Time // in-flight 占位过期时刻（可被接管）
	body      []byte    // completed 的回放 body
	expires   time.Time // completed 过期时刻
}

var idemStore = struct {
	sync.Mutex
	entries map[string]*idemEntry
}{entries: make(map[string]*idemEntry)}

var idemOwnerSeq uint64

func newIdemOwnerToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	// crypto/rand 失败时退化到序号+纳秒（仍保证进程内唯一）。
	return strconv.FormatUint(atomic.AddUint64(&idemOwnerSeq, 1), 36) + "-" +
		strconv.FormatInt(time.Now().UnixNano(), 36)
}

func idemCacheKey(userID int64, scope string, r *http.Request, key string) string {
	method, path := "", ""
	if r != nil {
		method = r.Method
		if r.URL != nil {
			path = r.URL.Path
		}
	}
	return strconv.FormatInt(userID, 10) + "\x00" + scope + "\x00" + method + "\x00" + path + "\x00" + strings.TrimSpace(key)
}

// closeIdemDoneLocked 唤醒等待者；同一 entry 只关一次。调用方持锁。
func closeIdemDoneLocked(e *idemEntry) {
	if e != nil && e.done != nil && !e.doneClose {
		e.doneClose = true
		close(e.done)
	}
}

// sweepIdemLocked 清理过期项（惰性，防无界增长）。调用方持锁。
func sweepIdemLocked(now time.Time) {
	for k, e := range idemStore.entries {
		switch e.state {
		case idemCompleted:
			if !now.Before(e.expires) {
				delete(idemStore.entries, k)
			}
		case idemInflight:
			if !now.Before(e.deadline) {
				closeIdemDoneLocked(e)
				delete(idemStore.entries, k)
			}
		}
	}
}

// idemCaptureWriter tees the handler's response body into a buffer so a
// successful first result can be replayed verbatim (including its ts).
type idemCaptureWriter struct {
	http.ResponseWriter
	buf *bytes.Buffer
}

func (w *idemCaptureWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	return w.ResponseWriter.Write(p)
}

// idemBegin engages idempotency for a create handler (scope = 调用点动作
// 常量，如 "forward:create")。It returns the (possibly wrapping) writer
// to use, a finish func the caller must defer, and replayed=true when a
// cached first result (or a 409 in-flight notice) has already been
// written and the caller must return immediately.
func (h *Handler) idemBegin(w http.ResponseWriter, r *http.Request, userID int64, scope string) (http.ResponseWriter, func(), bool) {
	key := ""
	if r != nil {
		key = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	}
	if key == "" {
		return w, func() {}, false
	}
	ck := idemCacheKey(userID, scope, r, key)
	owner := newIdemOwnerToken()
	waitDeadline := time.Now().Add(idemWaitTimeout)

	for {
		idemStore.Lock()
		now := time.Now()
		e, ok := idemStore.entries[ck]
		if ok && e.state == idemCompleted && !now.Before(e.expires) {
			delete(idemStore.entries, ck)
			ok = false
		}
		if ok && e.state == idemInflight && !now.Before(e.deadline) {
			// 占位已过期：接管（唤醒旧等待者去等新占位），旧 owner 的
			// finish 将因 entry/owner 不匹配而失效，不得覆写。
			closeIdemDoneLocked(e)
			delete(idemStore.entries, ck)
			ok = false
		}
		if ok && e.state == idemCompleted {
			body := append([]byte(nil), e.body...)
			idemStore.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return w, func() {}, true
		}
		if !ok {
			// 原子占位：成为 owner。
			if len(idemStore.entries) > 4096 {
				sweepIdemLocked(now)
			}
			entry := &idemEntry{
				state:    idemInflight,
				owner:    owner,
				done:     make(chan struct{}),
				deadline: now.Add(idemInflightTimeout),
			}
			idemStore.entries[ck] = entry
			idemStore.Unlock()

			buf := &bytes.Buffer{}
			cw := &idemCaptureWriter{ResponseWriter: w, buf: buf}
			finish := func() {
				body := append([]byte(nil), buf.Bytes()...)
				var env struct {
					Code int `json:"code"`
				}
				success := json.Unmarshal(body, &env) == nil && env.Code == 0
				idemStore.Lock()
				defer idemStore.Unlock()
				cur, exists := idemStore.entries[ck]
				if !exists || cur != entry || cur.owner != owner || cur.state != idemInflight {
					// 已被接管/释放：旧 finish 不得覆写新 owner。
					return
				}
				if success {
					cur.state = idemCompleted
					cur.body = body
					cur.expires = time.Now().Add(idempotencyTTL)
				} else {
					// 失败不落缓存：释放占位，修正后同键重试可真正执行。
					delete(idemStore.entries, ck)
				}
				closeIdemDoneLocked(cur)
			}
			return cw, finish, false
		}
		// in-flight 且未过期：等待 owner 完成（或占位 deadline 到期可接管）。
		done := e.done
		remaining := time.Until(waitDeadline)
		if remaining <= 0 {
			idemStore.Unlock()
			response.WriteJSON(w, response.Err(409, "相同请求正在处理中，请稍后重试"))
			return w, func() {}, true
		}
		waitFor := remaining
		if d := time.Until(e.deadline); d < waitFor {
			waitFor = d
		}
		if waitFor < 0 {
			waitFor = 0
		}
		idemStore.Unlock()
		timer := time.NewTimer(waitFor)
		select {
		case <-done:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// idemUserID best-effort 解析请求用户（管理员端点本身不解析 token 时
// 用它拼缓存键；解析失败回 0，该端点由中间件保证仅管理员可达）。
func idemUserID(r *http.Request) int64 {
	if r == nil {
		return 0
	}
	userID, _, err := userRoleFromRequest(r)
	if err != nil {
		return 0
	}
	return userID
}
