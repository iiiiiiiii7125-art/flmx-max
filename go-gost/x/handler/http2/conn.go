package http2

import (
	"errors"
	"io"
	"net/http"
)

type flushWriter struct {
	w io.Writer
}

// streamReadWriter is the Transport endpoint for an HTTP/2 CONNECT
// stream: the wrapped (limiter/stats) stream I/O, plus the request
// body's Close re-exposed as the endpoint's own. The stats/traffic
// wrappers embed io.ReadWriter and hide every other method of what
// they wrap, so without this outer layer Transport can neither arm
// nor close the endpoint (audit P1-01).
//
// The endpoint deliberately has no SetReadDeadline/SetWriteDeadline.
// Go's HTTP/2 stream deadlines are destructive: a fired read
// deadline breaks the request-body pipe with ErrDeadlineExceeded
// (http2stream.onReadTimeout in net/http), and the break is sticky —
// the wake cannot be re-armed and resumed the way a conn deadline
// can. Arming one would kill (or spin on the broken pipe) a session
// whose other direction is still flowing. The FIX-1 no-progress
// bound therefore reaches this endpoint through its sibling: the
// dialed upstream conn is deadline-armable, and a stalled session
// is torn down from that side. Teardown sequencing: the sibling's
// armed read deadline fires -> the session is judged stalled ->
// teardown closes both endpoints -> Close here breaks the body
// pipe -> the copy goroutine parked on Read wakes with the body
// error and reports ErrNoProgress instead of parking for the life
// of the HTTP/2 connection.
type streamReadWriter struct {
	io.ReadWriter
	body io.Closer
}

// Close closes the request body, unblocking any Read parked on it.
func (rw *streamReadWriter) Close() error {
	return rw.body.Close()
}

func (fw flushWriter) Write(p []byte) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			if s, ok := r.(string); ok {
				err = errors.New(s)
				return
			}
			err = r.(error)
		}
	}()

	n, err = fw.w.Write(p)
	if err != nil {
		// log.Log("flush writer:", err)
		return
	}
	if f, ok := fw.w.(http.Flusher); ok {
		f.Flush()
	}
	return
}
