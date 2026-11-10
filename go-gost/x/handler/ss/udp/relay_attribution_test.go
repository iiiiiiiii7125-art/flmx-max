package ss

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/go-gost/core/handler"
	xlogger "github.com/go-gost/x/logger"
	xrecorder "github.com/go-gost/x/recorder"
)

// F2-19 attribution: window off + foreign timeout => verbatim err;
// window armed + fully stalled => errSSRelayIdleTimeout.

func TestSSRelayAttributionWindowOffForeignTimeoutVerbatim(t *testing.T) {
	setSSRelayWindow(t, -time.Second)
	h := &ssuHandler{options: handler.Options{}}
	dead := &ssForeignTimeoutPacketConn{}
	pc2, _ := wireSSIdlePair()
	done := make(chan error, 1)
	go func() {
		done <- h.relayPacket(context.Background(), dead, pc2, &xrecorder.HandlerRecorderObject{}, xlogger.Nop())
	}()
	select {
	case err := <-done:
		if errors.Is(err, errSSRelayIdleTimeout) {
			t.Fatalf("relayPacket err = %v, want foreign timeout verbatim, not idle", err)
		}
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("relayPacket err = %v, want original deadline-exceeded", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relayPacket never returned")
	}
	if !dead.closed.Load() || !pc2.isClosed() {
		t.Fatal("unwind must still close both endpoints")
	}
}

func TestSSRelayAttributionWindowArmedStalledIsIdle(t *testing.T) {
	setSSRelayWindow(t, 300*time.Millisecond)
	h := &ssuHandler{options: handler.Options{}}
	pc1, _ := wireSSIdlePair()
	pc2, _ := wireSSIdlePair()
	done := make(chan error, 1)
	go func() {
		done <- h.relayPacket(context.Background(), pc1, pc2, &xrecorder.HandlerRecorderObject{}, xlogger.Nop())
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errSSRelayIdleTimeout) {
			t.Fatalf("relayPacket err = %v, want errSSRelayIdleTimeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relayPacket never returned for stalled association")
	}
}
