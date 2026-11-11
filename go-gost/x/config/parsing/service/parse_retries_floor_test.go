package service

import (
	"context"
	"net"
	"sync"
	"testing"

	corechain "github.com/go-gost/core/chain"
	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/listener"
	"github.com/go-gost/core/metadata"
	"github.com/go-gost/x/config"
	"github.com/go-gost/x/registry"
)

// AUDIT-R217 C-01: the parse-time retries floor (D4) as a permanent
// unit gate. AUDIT-R217-C knife 2 showed the floor is real but had
// no unit nail — removing max(cfg.Handler.Retries, 2) turned no
// pre-existing test red. This is that probe, promoted: a fake
// handler captures the Router ParseService builds for it, and the
// effective Retries are asserted for written values below, at and
// above the floor (the floor only raises, never lowers).

type floorFakeHandler struct{}

func (floorFakeHandler) Init(metadata.Metadata) error { return nil }
func (floorFakeHandler) Handle(context.Context, net.Conn, ...handler.HandleOption) error {
	return nil
}

type floorFakeListener struct{}

func (floorFakeListener) Init(metadata.Metadata) error { return nil }
func (floorFakeListener) Accept() (net.Conn, error)    { return nil, net.ErrClosed }
func (floorFakeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1")}
}
func (floorFakeListener) Close() error { return nil }

var (
	floorRegisterOnce sync.Once
	floorCaptured     corechain.Router
)

func registerFloorFakes() {
	floorRegisterOnce.Do(func() {
		_ = registry.HandlerRegistry().Register("r217floor", func(opts ...handler.Option) handler.Handler {
			o := &handler.Options{}
			for _, opt := range opts {
				if opt != nil {
					opt(o)
				}
			}
			floorCaptured = o.Router
			return floorFakeHandler{}
		})
		_ = registry.ListenerRegistry().Register("r217floor", func(opts ...listener.Option) listener.Listener {
			return floorFakeListener{}
		})
	})
}

func TestParseServiceHandlerRetriesFloor(t *testing.T) {
	registerFloorFakes()
	for _, tc := range []struct {
		written int
		want    int
	}{
		{0, 2}, // nothing written: floor supplies the F-215-C minimum
		{1, 2}, // below the floor: raised
		{2, 2}, // at the floor: unchanged
		{5, 5}, // above the floor: never lowered
	} {
		floorCaptured = nil
		cfg := &config.ServiceConfig{
			Name:     "r217floor",
			Addr:     "127.0.0.1:0",
			Listener: &config.ListenerConfig{Type: "r217floor"},
			Handler:  &config.HandlerConfig{Type: "r217floor", Retries: tc.written},
		}
		s, err := ParseService(cfg)
		if err != nil {
			t.Fatalf("cfg.Handler.Retries=%d: ParseService: %v", tc.written, err)
		}
		s.Close()
		if floorCaptured == nil {
			t.Fatalf("cfg.Handler.Retries=%d: handler factory captured no router", tc.written)
		}
		r, ok := floorCaptured.(interface {
			Options() *corechain.RouterOptions
		})
		if !ok {
			t.Fatalf("captured router %T has no Options()", floorCaptured)
		}
		if got := r.Options().Retries; got != tc.want {
			t.Fatalf("cfg.Handler.Retries=%d -> effective handler router Retries=%d, want %d (parse-time floor 2, raise-only)",
				tc.written, got, tc.want)
		}
	}
}
