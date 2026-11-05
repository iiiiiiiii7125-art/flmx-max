package net

import (
	"errors"
	"io"
	"sync"

	"github.com/go-gost/core/common/bufpool"
)

var errHalfCloseNotSupported = errors.New("half-close not supported by the underlying connection")

const (
	bufferSize = 64 * 1024
)

type closeWriter interface {
	CloseWrite() error
}

// Transport copies data between rw1 and rw2 in both directions.
//
// Half-close semantics (D3-03): when one direction reaches its end,
// the destination's write side is half-closed (CloseWrite, where the
// transport supports it) so the peer observes the end of that stream,
// and the reverse direction keeps copying until it ends too.
// Transport returns only once both directions are done. On a hard
// (non-EOF) failure both endpoints are closed to unblock the sibling
// copy, and the error is returned.
//
// Previously the first direction to finish — including a clean client
// shutdown(WR) — returned from Transport at once, and the handlers'
// deferred closes then discarded whatever the reverse direction still
// had in flight: a half-closing client lost the tail of the response.
//
// Bounds: a direction whose peer never finishes ends by the peer's
// own close or by the connection deadlines the owning service
// configures — the same bound that already governed the reverse copy,
// which always ran until the peer closed or errored. Transports
// without a CloseWrite keep flowing in the reverse direction until
// the peer finishes; they cannot signal end-of-stream early.
func Transport(rw1, rw2 io.ReadWriter) error {
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = copyDirection(rw1, rw2)
	}()
	go func() {
		defer wg.Done()
		errs[1] = copyDirection(rw2, rw1)
	}()
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// copyDirection copies src -> dst until src is exhausted or fails.
// A clean end propagates a half-close to dst; a hard failure closes
// both endpoints so the sibling direction cannot park forever on a
// dead peer, and returns the error.
func copyDirection(dst, src io.ReadWriter) error {
	err := CopyBuffer(dst, src, bufferSize)
	if err == nil || err == io.EOF {
		if cw, ok := dst.(closeWriter); ok {
			// Best effort: the reverse direction's completion does
			// not depend on this succeeding.
			_ = cw.CloseWrite()
		}
		return nil
	}
	if c, ok := dst.(io.Closer); ok {
		c.Close()
	}
	if c, ok := src.(io.Closer); ok {
		c.Close()
	}
	return err
}

func CopyBuffer(dst io.Writer, src io.Reader, bufSize int) error {
	buf := bufpool.Get(bufSize)
	defer bufpool.Put(buf)

	_, err := io.CopyBuffer(dst, src, buf)
	return err
}
