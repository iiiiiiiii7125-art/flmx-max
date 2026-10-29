package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// R8 T-family mechanism regression: per-endpoint write-deadline relief
// (http.ResponseController.SetWriteDeadline, the gh_mirror.go precedent
// form) must survive the RequestLog statusWriter wrapper. Without an
// Unwrap() on statusWriter the controller call returns "feature not
// supported", the relief silently does nothing, and any handler working
// past the server's global WriteTimeout has its response truncated.
func TestRequestLogWriteDeadlinePassthrough(t *testing.T) {
	build := func(extend bool) *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
			if extend {
				_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
			}
			time.Sleep(2 * time.Second)
			_, _ = w.Write([]byte("done"))
		})
		srv := httptest.NewUnstartedServer(RequestLog(mux))
		srv.Config.WriteTimeout = time.Second
		srv.Start()
		return srv
	}

	t.Run("extended deadline delivers past WriteTimeout", func(t *testing.T) {
		srv := build(true)
		defer srv.Close()
		resp, err := http.Get(srv.URL + "/slow")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(body) != "done" {
			t.Fatalf("body = %q, want %q (write deadline relief must survive RequestLog)", string(body), "done")
		}
	})

	t.Run("without extension the same work is truncated", func(t *testing.T) {
		srv := build(false)
		defer srv.Close()
		resp, err := http.Get(srv.URL + "/slow")
		if err != nil {
			return // connection cut at the write deadline: truncation proven
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if string(body) == "done" {
			t.Fatalf("control case unexpectedly delivered the full body; WriteTimeout did not bind")
		}
	})
}
