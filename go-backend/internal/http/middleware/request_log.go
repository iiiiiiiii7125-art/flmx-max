package middleware

import (
	"bufio"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"go-backend/internal/logredact"
)

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hj.Hijack()
}

// Unwrap lets http.ResponseController reach the underlying writer.
// Without it, per-endpoint SetWriteDeadline relief (R8 T-family, the
// gh_mirror.go precedent form) silently returns "feature not
// supported" behind this wrapper and long responses are truncated at
// the server's global WriteTimeout despite the handler's relief call.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(w.ResponseWriter, r)
}

func (w *statusWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := w.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

func RequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		// A8-01：路径可能含凭据（订阅 token），记录前必须脱敏。
		log.Printf("%s %s -> %d (%s)", r.Method, logredact.Path(r.URL.Path), sw.status, time.Since(start).String())
	})
}
