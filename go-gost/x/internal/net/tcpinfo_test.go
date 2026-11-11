//go:build linux

package net

import (
	"errors"
	"io"
	"syscall"
	"testing"
	"time"
)

// The edge judge's core signal on a real loopback connection:
// Bytes_acked advances while the peer consumes, and freezes
// with Snd_wnd == 0 and bytes standing unacked once the peer
// stops reading and both buffers fill (F3 §4.1).
func TestTCPInfoLoopbackConsumption(t *testing.T) {
	reader, writer := tcpPair(t)
	defer reader.Close()
	defer writer.Close()

	info, err := tcpInfoOf(writer)
	if err != nil {
		t.Fatalf("tcpInfoOf: %v", err)
	}
	if info.State != tcpStateEstablished {
		t.Fatalf("state = %d, want ESTABLISHED (%d)", info.State, tcpStateEstablished)
	}
	probe := tcpInfoProbe(writer)
	if probe == nil {
		t.Fatal("tcpInfoProbe returned nil for a raw TCP conn")
	}

	// Phase 1: the peer reads everything — Bytes_acked must
	// catch up with Bytes_sent.
	payload := make([]byte, 64*1024)
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatalf("read: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		info, err = probe()
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if info.BytesAcked >= uint64(len(payload)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Bytes_acked = %d after 3s, want >= %d (peer consumed everything)", info.BytesAcked, len(payload))
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Phase 2: the peer stops reading. Fill both buffers until
	// the writer blocks, then Bytes_acked must freeze with a
	// shut window and bytes standing unacked.
	writer.SetWriteDeadline(time.Now().Add(5 * time.Second))
	chunk := make([]byte, 64*1024)
	for {
		if _, err := writer.Write(chunk); err != nil {
			break // deadline hit: buffers are full
		}
	}
	time.Sleep(200 * time.Millisecond) // let final ACKs settle
	first, err := probe()
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	second, err := probe()
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if second.BytesAcked != first.BytesAcked {
		t.Fatalf("Bytes_acked advanced with a non-reading peer: %d -> %d", first.BytesAcked, second.BytesAcked)
	}
	if second.SndWnd != 0 {
		t.Fatalf("Snd_wnd = %d, want 0 with a full receive buffer", second.SndWnd)
	}
	if second.Unacked == 0 && second.NotsentBytes == 0 {
		t.Fatalf("nothing outstanding (unacked=%d notsent=%d) with full buffers", second.Unacked, second.NotsentBytes)
	}
}

// A conn whose chain exposes no SyscallConn resolves to a nil
// probe: tracking stays armed but the edge judge never fires
// (fail-safe for mux and wrapped stacks, F3 §3).
func TestTCPInfoProbeNilForOpaqueConn(t *testing.T) {
	_, raw := tcpPair(t)
	defer raw.Close()
	opaque := &memRW{RW: raw}
	if p := tcpInfoProbe(opaque); p != nil {
		t.Fatal("tcpInfoProbe returned a probe for an opaque wrapper, want nil")
	}
	if _, err := tcpInfoOf(opaque); !errors.Is(err, errTCPInfoUnavailable) {
		t.Fatalf("tcpInfoOf(opaque) err = %v, want errTCPInfoUnavailable", err)
	}
}

// Reaper teardown is abortive (F2): with the peer's data fully
// read, a linger-0 close must surface at the peer as a reset —
// the control case (plain close) must surface as a clean EOF,
// proving the test discriminates RST from FIN.
func TestAbortiveCloseSendsRST(t *testing.T) {
	// Control: plain close -> peer reads EOF.
	peer1, end1 := tcpPair(t)
	defer peer1.Close()
	if _, err := end1.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := io.ReadFull(peer1, make([]byte, 5)); err != nil {
		t.Fatalf("peer read: %v", err)
	}
	end1.Close()
	if _, err := peer1.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("plain close: peer read err = %v, want io.EOF (FIN)", err)
	}

	// Abortive close of the same shape -> peer reads ECONNRESET.
	peer2, end2 := tcpPair(t)
	defer peer2.Close()
	if _, err := end2.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := io.ReadFull(peer2, make([]byte, 5)); err != nil {
		t.Fatalf("peer read: %v", err)
	}
	abortiveClose(end2)
	_, err := peer2.Read(make([]byte, 1))
	if err == nil || err == io.EOF {
		t.Fatalf("abortive close: peer read err = %v, want ECONNRESET, not FIN/EOF", err)
	}
	if !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("abortive close: peer read err = %v, want ECONNRESET", err)
	}
}
