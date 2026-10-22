package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// X-P2-17（L-11-1，BC-13，裁定 V-07）：创建类 POST 的请求级幂等。
//
// 语义：请求头 Idempotency-Key 存在时，后端按（用户, 键）缓存首次
// 成功结果 10 分钟；同键重复提交直接回放首次结果，不再执行创建。
// 不带键的调用方行为不变。业务唯一键拒绝不采用——同隧道同目标多
// 入口是合法用法（三元组拒绝会误伤），防重只针对同一次提交的重试/
// 双击。仅缓存 code==0 的成功信封：校验/瞬时错误不落缓存，修正后
// 同键重试仍可真正执行。
const idempotencyTTL = 10 * time.Minute

type idemEntry struct {
	body    []byte
	expires time.Time
}

var idemStore = struct {
	sync.Mutex
	entries map[string]idemEntry
}{entries: make(map[string]idemEntry)}

func idemCacheKey(userID int64, key string) string {
	return strconv.FormatInt(userID, 10) + "\x00" + strings.TrimSpace(key)
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

// idemBegin engages idempotency for a create handler. It returns the
// (possibly wrapping) writer to use, a finish func the caller must defer,
// and replayed=true when a cached first result has already been written
// and the caller must return immediately.
func (h *Handler) idemBegin(w http.ResponseWriter, r *http.Request, userID int64) (http.ResponseWriter, func(), bool) {
	key := ""
	if r != nil {
		key = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	}
	if key == "" {
		return w, func() {}, false
	}
	ck := idemCacheKey(userID, key)
	now := time.Now()
	idemStore.Lock()
	if e, ok := idemStore.entries[ck]; ok {
		if now.Before(e.expires) {
			body := append([]byte(nil), e.body...)
			idemStore.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return w, func() {}, true
		}
		delete(idemStore.entries, ck)
	}
	idemStore.Unlock()

	buf := &bytes.Buffer{}
	cw := &idemCaptureWriter{ResponseWriter: w, buf: buf}
	finish := func() {
		body := append([]byte(nil), buf.Bytes()...)
		var env struct {
			Code int `json:"code"`
		}
		if json.Unmarshal(body, &env) != nil || env.Code != 0 {
			return
		}
		idemStore.Lock()
		defer idemStore.Unlock()
		// 惰性清理过期项，防无界增长。
		if len(idemStore.entries) > 4096 {
			now := time.Now()
			for k, e := range idemStore.entries {
				if !now.Before(e.expires) {
					delete(idemStore.entries, k)
				}
			}
		}
		idemStore.entries[ck] = idemEntry{body: body, expires: time.Now().Add(idempotencyTTL)}
	}
	return cw, finish, false
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
