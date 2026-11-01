package socket

// Cross-route resume for the self-upgrade binary (production
// report 2026-11-01): a slow domestic node's direct leg trickled
// inside the shared 240s budget, every later proxy leg then died
// on the expired context — the receipt showed all 10 routes dead
// although the proxies were never really tried, and every leg had
// restarted the 9.4MB body from byte zero anyway. The download
// now keeps the bytes already on disk across legs of one upgrade
// and resumes with Range; this test pins that caliber on the real
// production path (downloadUpgradeBinary): the second route must
// transfer ONLY the missing remainder, and the assembled file
// must hash to the payload.

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
	"time"

	"github.com/go-gost/x/internal/util/download"
)

// dropMidBodyUpgradeServer declares the full Content-Length,
// writes the first dropAfter bytes of payload, then returns — the
// connection closes mid-body and the client sees an unexpected
// EOF, like a route dying mid-transfer.
func dropMidBodyUpgradeServer(payload []byte, dropAfter int, served *atomic.Int64) *httptest.Server {
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

// rangeUpgradeServer serves payload, honoring "Range: bytes=N-"
// with a proper 206 + Content-Range (as GitHub's asset storage
// and the real mirrors do).
func rangeUpgradeServer(payload []byte, served *atomic.Int64) *httptest.Server {
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

func TestUpgradeBinaryResumesAcrossRoutes(t *testing.T) {
	payload := make([]byte, 256*1024)
	for i := range payload {
		payload[i] = byte(i*31 + 7)
	}
	const dropAfter = 100 * 1024
	var aServed, bServed atomic.Int64
	flaky := dropMidBodyUpgradeServer(payload, dropAfter, &aServed)
	defer flaky.Close()
	ranged := rangeUpgradeServer(payload, &bServed)
	defer ranged.Close()

	plan := download.NewStaticPlan([]download.Route{{Prefix: flaky.URL}, {}})
	tmp := filepath.Join(t.TempDir(), "flux_agent.new")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, hash, err := downloadUpgradeBinary(ctx, newUpgradeDownloadClient(""), plan, ranged.URL+"/gost-amd64", tmp, nil)
	if err != nil {
		t.Fatalf("首腿中途断线后应跨线路续传完成: %v", err)
	}
	sum := sha256.Sum256(payload)
	if n != int64(len(payload)) || hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("续传组装结果错误: n=%d hash=%s", n, hash)
	}
	if b, err := os.ReadFile(tmp); err != nil || !bytes.Equal(b, payload) {
		t.Fatalf("落盘内容与原始载荷不符: err=%v len=%d", err, len(b))
	}
	if aServed.Load() != dropAfter {
		t.Fatalf("首腿应传输 %d bytes, 实际 %d", dropAfter, aServed.Load())
	}
	if want := int64(len(payload) - dropAfter); bServed.Load() != want {
		t.Fatalf("次腿应只续传剩余 %d bytes, 实际 %d (等于全量即未续传、从 0 重下)", want, bServed.Load())
	}
}
