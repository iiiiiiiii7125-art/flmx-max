package service

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/metadata"
	xlogger "github.com/go-gost/x/logger"
)

// D2-01 regression (run with -race): needWrap was a plain bool
// written by SetProtocolBlock on the command path and read by
// every accepted connection's goroutine, while its three sibling
// switches were already atomic. Toggling the protocol block
// concurrently with a stream of accepted connections raced on it.
// The sibling switches use atomics; needWrap must too.

type raceToggleHandler struct{}

func (raceToggleHandler) Init(metadata.Metadata) error { return nil }

func (raceToggleHandler) Handle(context.Context, net.Conn, ...handler.HandleOption) error {
	return nil
}

func TestSetProtocolBlockConcurrentWithServeAccepts(t *testing.T) {
	defer SetProtocolBlock(0, 0, 0)

	ln := &fakePanicListener{
		connCh: make(chan net.Conn, 64),
		closed: make(chan struct{}),
	}
	svc := &defaultService{
		name:     "svc-needwrap-race",
		listener: ln,
		handler:  raceToggleHandler{},
		status:   &Status{},
		options:  options{logger: xlogger.NewLogger()},
	}
	go svc.Serve()
	defer ln.Close()

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Command path: toggle the protocol block as fast as possible.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			SetProtocolBlock(1, 0, 0)
			SetProtocolBlock(0, 0, 0)
		}
	}()

	// Connection path: keep the accept loop fed so the per-connection
	// goroutines keep reading the wrapper switch.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			server, client := net.Pipe()
			select {
			case ln.connCh <- server:
			case <-stop:
				server.Close()
				client.Close()
				return
			}
			client.Close()
		}
	}()

	time.Sleep(1500 * time.Millisecond)
	close(stop)
	wg.Wait()
}
