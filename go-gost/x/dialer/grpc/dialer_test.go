package grpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"testing"
	"time"

	xnet "github.com/go-gost/core/common/net"
	coredialer "github.com/go-gost/core/dialer"
	pb "github.com/go-gost/x/internal/util/grpc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

type stdNetDialer struct{}

func (stdNetDialer) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	var nd net.Dialer
	return nd.DialContext(ctx, network, addr)
}

var _ xnet.Dialer = stdNetDialer{}

func insertClient(t *testing.T, d *grpcDialer, addr string) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient(%s): %v", addr, err)
	}
	d.clients[addr] = &grpcClient{client: pb.NewGostTunelClientX(cc), conn: cc}
	return cc
}

// TestRetireClosesClientConns verifies P1-08: Retire (reached via
// Chain.Retire) detaches every cached client and really closes the
// ClientConns, and the retired dialer refuses new dials.
func TestRetireClosesClientConns(t *testing.T) {
	d := NewDialer().(*grpcDialer)
	var conns []*grpc.ClientConn
	for i := 0; i < 3; i++ {
		conns = append(conns, insertClient(t, d, fmt.Sprintf("127.0.0.1:%d", 40000+i)))
	}

	d.Retire()

	if got := len(d.clients); got != 0 {
		t.Fatalf("clients not detached: len = %d, want 0", got)
	}
	for i, cc := range conns {
		if state := cc.GetState(); state != connectivity.Shutdown {
			t.Fatalf("conn %d state = %s, want Shutdown after Retire", i, state)
		}
	}
	if _, err := d.Dial(context.Background(), "127.0.0.1:40001"); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Dial after Retire err = %v, want net.ErrClosed", err)
	}
}

// TestGenerationChurnDoesNotLeak simulates repeated chain generations:
// each generation caches several ClientConns and is then retired.
// Goroutine count must settle back instead of growing per generation.
func TestGenerationChurnDoesNotLeak(t *testing.T) {
	generation := func() {
		d := NewDialer().(*grpcDialer)
		for i := 0; i < 4; i++ {
			insertClient(t, d, fmt.Sprintf("127.0.0.1:%d", 41000+i))
		}
		d.Retire()
	}

	generation() // warm-up
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()

	for i := 0; i < 5; i++ {
		generation()
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		runtime.GC()
		if delta := runtime.NumGoroutine() - before; delta <= 4 {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("goroutines grew across generations: before=%d now=%d", before, runtime.NumGoroutine())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestDialReplacesShutdownClient verifies delete-on-dead: a cached
// client whose ClientConn has shut down is dropped and replaced on
// the next Dial instead of being reused forever.
func TestDialReplacesShutdownClient(t *testing.T) {
	d := NewDialer().(*grpcDialer)
	dead := insertClient(t, d, "127.0.0.1:1")
	dead.Close()

	// No server is listening, so Dial must fail — but it must have
	// replaced the dead entry with a fresh ClientConn on the way.
	_, _ = d.Dial(context.Background(), "127.0.0.1:1", coredialer.NetDialerDialOption(stdNetDialer{}))

	entry, ok := d.clients["127.0.0.1:1"]
	if !ok {
		t.Fatal("no client cached after Dial")
	}
	if entry.conn == dead {
		t.Fatal("shutdown ClientConn was reused, want replacement")
	}
	d.Retire()
}

// BenchmarkClientLookup measures exactly the work Dial performs on a
// cache hit beyond the pre-fix code: mutex + map lookup + the new
// ClientConn state check.
func BenchmarkClientLookup(b *testing.B) {
	d := &grpcDialer{clients: make(map[string]*grpcClient)}
	cc, err := grpc.NewClient("127.0.0.1:1", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		b.Fatal(err)
	}
	d.clients["127.0.0.1:1"] = &grpcClient{client: pb.NewGostTunelClientX(cc), conn: cc}
	defer cc.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.clientMutex.Lock()
		entry := d.clients["127.0.0.1:1"]
		_ = entry.conn.GetState() == connectivity.Shutdown
		d.clientMutex.Unlock()
	}
}
