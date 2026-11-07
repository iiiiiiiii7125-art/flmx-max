package net

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// Performance gate for FIX-1 (transport no-progress timeout): the
// fixed Transport must forward at the same throughput and latency
// as the pre-fix implementation. legacyTransport below is the
// pre-fix implementation verbatim (io.CopyBuffer based), kept
// test-only as the comparison baseline; it is not used by product
// code.

func legacyCopyDirection(dst, src io.ReadWriter) error {
	err := CopyBuffer(dst, src, bufferSize)
	if err == nil || err == io.EOF {
		if cw, ok := dst.(closeWriter); ok {
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

func legacyTransport(rw1, rw2 io.ReadWriter) error {
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = legacyCopyDirection(rw1, rw2)
	}()
	go func() {
		defer wg.Done()
		errs[1] = legacyCopyDirection(rw2, rw1)
	}()
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// echoBackend echoes everything back and closes when its peer
// half-closes/closes.
func echoBackend(c net.Conn) {
	_, _ = io.Copy(c, c)
	c.Close()
}

// runThroughput pumps total bytes through the transport impl in an
// echo topology and returns the elapsed time.
func runThroughput(b *testing.B, impl func(io.ReadWriter, io.ReadWriter) error, total int) {
	b.Helper()
	clientSide, transportToClient := tcpPair(b)
	backendSide, transportToBackend := tcpPair(b)
	defer clientSide.Close()
	defer backendSide.Close()
	go echoBackend(backendSide)

	done := make(chan error, 1)
	go func() { done <- impl(transportToClient, transportToBackend) }()

	chunk := bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64 KiB
	go func() {
		for sent := 0; sent < total; {
			n := len(chunk)
			if rem := total - sent; rem < n {
				n = rem
			}
			if _, err := clientSide.Write(chunk[:n]); err != nil {
				return
			}
			sent += n
		}
		clientSide.(*net.TCPConn).CloseWrite()
	}()

	buf := make([]byte, 64*1024)
	for received := 0; received < total; {
		n, err := clientSide.Read(buf)
		received += n
		if err != nil {
			b.Fatalf("client read after %d/%d bytes: %v", received, total, err)
		}
	}
	if err := <-done; err != nil {
		b.Fatalf("transport: %v", err)
	}
	transportToClient.Close()
	transportToBackend.Close()
}

func BenchmarkTransportThroughputFixed(b *testing.B) {
	const total = 32 << 20 // 32 MiB per op
	b.SetBytes(total)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		runThroughput(b, Transport, total)
	}
}

func BenchmarkTransportThroughputLegacy(b *testing.B) {
	const total = 32 << 20 // 32 MiB per op
	b.SetBytes(total)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		runThroughput(b, legacyTransport, total)
	}
}

// runRoundTrip performs sequential small request/response exchanges
// through the transport impl and returns the per-round-trip latency.
func runRoundTrip(b *testing.B, impl func(io.ReadWriter, io.ReadWriter) error) {
	b.Helper()
	clientSide, transportToClient := tcpPair(b)
	backendSide, transportToBackend := tcpPair(b)
	defer clientSide.Close()
	defer backendSide.Close()
	go echoBackend(backendSide)

	done := make(chan error, 1)
	go func() { done <- impl(transportToClient, transportToBackend) }()

	msg := bytes.Repeat([]byte("p"), 64)
	buf := make([]byte, 64)
	clientSide.SetDeadline(time.Now().Add(30 * time.Second))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := clientSide.Write(msg); err != nil {
			b.Fatalf("client write: %v", err)
		}
		if _, err := io.ReadFull(clientSide, buf); err != nil {
			b.Fatalf("client read: %v", err)
		}
	}
	b.StopTimer()
	clientSide.Close()
	backendSide.Close()
	<-done
	transportToClient.Close()
	transportToBackend.Close()
}

func BenchmarkTransportRoundTripFixed(b *testing.B) {
	runRoundTrip(b, Transport)
}

func BenchmarkTransportRoundTripLegacy(b *testing.B) {
	runRoundTrip(b, legacyTransport)
}
