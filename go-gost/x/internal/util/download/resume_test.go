package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// Test harness for cross-route resumable file downloads
// (FileTransfer): stub servers model the three route behaviors
// that matter — a route dying mid-body, a Range-capable route
// (GitHub asset storage and real mirrors), a route ignoring
// Range, and a route answering 206 with a lying Content-Range.
// Byte counters on every stub make "resumed vs restarted from
// zero" directly observable.

func resumePayload(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte(i*31 + 7)
	}
	return p
}

// dropBodyServer declares the full length, writes the first
// dropAfter bytes, then returns (connection closes mid-body).
func dropBodyServer(payload []byte, dropAfter int, served *atomic.Int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.WriteHeader(http.StatusOK)
		part := payload[:dropAfter]
		if _, err := w.Write(part); err != nil {
			return
		}
		served.Add(int64(len(part)))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
}

// rangeCapableServer honors "Range: bytes=N-" with 206 +
// Content-Range; without a Range header it serves the full body.
func rangeCapableServer(payload []byte, served *atomic.Int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := 0
		if rh := r.Header.Get("Range"); strings.HasPrefix(rh, "bytes=") {
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rh, "bytes="), "-")); err == nil {
				start = n
			}
		}
		if start > 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)-start))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			w.WriteHeader(http.StatusOK)
		}
		body := payload[start:]
		if _, err := w.Write(body); err != nil {
			return
		}
		served.Add(int64(len(body)))
	}))
}

// noRangeServer always answers 200 with the full body, ignoring
// any Range header (a mirror that cannot resume).
func noRangeServer(payload []byte, served *atomic.Int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(payload); err != nil {
			return
		}
		served.Add(int64(len(payload)))
	}))
}

// lyingRangeServer answers 206 but with a Content-Range starting
// at 0 regardless of the requested offset, serving the body from
// byte zero — a broken resume implementation whose bytes must
// never be appended onto a persisted prefix.
func lyingRangeServer(payload []byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Range"), "bytes=") {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(payload)-1, len(payload)))
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			w.WriteHeader(http.StatusOK)
		}
		_, _ = w.Write(payload)
	}))
}

func transferAttempt(tr *FileTransfer) func(Route, string) error {
	return func(route Route, url string) error {
		return tr.Attempt(context.Background(), nil, http.DefaultClient.Do, url, nil)
	}
}

func assertTransferContent(t *testing.T, tr *FileTransfer, path string, payload []byte) {
	t.Helper()
	sum := sha256.Sum256(payload)
	if tr.Size() != int64(len(payload)) || tr.Sum() != hex.EncodeToString(sum[:]) {
		t.Fatalf("组装结果错误: size=%d sum=%s", tr.Size(), tr.Sum())
	}
	if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, payload) {
		t.Fatalf("落盘内容不符: err=%v len=%d", err, len(b))
	}
}

// 首腿中途断线 -> 次腿（支持 Range）只续传剩余字节：总传输量
// 等于文件本身，远小于「每腿从 0」的基线（断线点 + 全量）。
func TestFileTransferResumeAcrossRoutesKeepsBytes(t *testing.T) {
	payload := resumePayload(256 * 1024)
	const dropAfter = 100 * 1024
	var aServed, bServed atomic.Int64
	flaky := dropBodyServer(payload, dropAfter, &aServed)
	defer flaky.Close()
	good := rangeCapableServer(payload, &bServed)
	defer good.Close()

	path := filepath.Join(t.TempDir(), "f.bin")
	tr := NewFileTransfer(path, 1<<20, 0o600)
	plan := NewStaticPlan([]Route{{Prefix: flaky.URL}, {}})
	if _, err := TryPlan(plan, good.URL+"/file", transferAttempt(tr)); err != nil {
		t.Fatalf("跨线路续传应完成: %v", err)
	}
	assertTransferContent(t, tr, path, payload)
	if aServed.Load() != dropAfter {
		t.Fatalf("首腿应传输 %d bytes, 实际 %d", dropAfter, aServed.Load())
	}
	if want := int64(len(payload) - dropAfter); bServed.Load() != want {
		t.Fatalf("次腿应只续传剩余 %d bytes, 实际 %d", want, bServed.Load())
	}
	if total := aServed.Load() + bServed.Load(); total != int64(len(payload)) {
		t.Fatalf("总传输量应等于文件大小 %d, 实际 %d (从 0 基线为 %d)", len(payload), total, dropAfter+len(payload))
	}
}

// 次腿不支持 Range（回 200 全量）-> 该腿截断重下，最终内容
// 仍完整正确。
func TestFileTransferFullResponseRestartsLeg(t *testing.T) {
	payload := resumePayload(256 * 1024)
	const dropAfter = 100 * 1024
	var aServed, bServed atomic.Int64
	flaky := dropBodyServer(payload, dropAfter, &aServed)
	defer flaky.Close()
	plain := noRangeServer(payload, &bServed)
	defer plain.Close()

	path := filepath.Join(t.TempDir(), "f.bin")
	tr := NewFileTransfer(path, 1<<20, 0o600)
	plan := NewStaticPlan([]Route{{Prefix: flaky.URL}, {}})
	if _, err := TryPlan(plan, plain.URL+"/file", transferAttempt(tr)); err != nil {
		t.Fatalf("200 全量腿应截断重下并完成: %v", err)
	}
	assertTransferContent(t, tr, path, payload)
	if bServed.Load() != int64(len(payload)) {
		t.Fatalf("不支持 Range 的次腿应全量重下 %d bytes, 实际 %d", len(payload), bServed.Load())
	}
}

// 206 的 Content-Range 起点与断点不符 -> 该腿作废、断点状态
// 不被污染，第三条线路从原断点续传完成。
func TestFileTransferBadContentRangeDiscardsLeg(t *testing.T) {
	payload := resumePayload(256 * 1024)
	const dropAfter = 100 * 1024
	var aServed, cServed atomic.Int64
	flaky := dropBodyServer(payload, dropAfter, &aServed)
	defer flaky.Close()
	lying := lyingRangeServer(payload)
	defer lying.Close()
	good := rangeCapableServer(payload, &cServed)
	defer good.Close()

	path := filepath.Join(t.TempDir(), "f.bin")
	tr := NewFileTransfer(path, 1<<20, 0o600)
	plan := NewStaticPlan([]Route{{Prefix: flaky.URL}, {Prefix: lying.URL}, {}})
	if _, err := TryPlan(plan, good.URL+"/file", transferAttempt(tr)); err != nil {
		t.Fatalf("坏 Content-Range 腿作废后续传应完成: %v", err)
	}
	assertTransferContent(t, tr, path, payload)
	if want := int64(len(payload) - dropAfter); cServed.Load() != want {
		t.Fatalf("第三腿应从原断点续传 %d bytes, 实际 %d (断点被污染则会偏移)", want, cServed.Load())
	}
}
