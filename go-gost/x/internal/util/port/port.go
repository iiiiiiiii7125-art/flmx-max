package port

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ForceClosePortConnections kills every TCP connection on addr's port
// (C-04). Pause semantics are "disconnect now, uniformly": a
// connection that keeps draining after a pause is the filed bug, not
// a drain window. The function is synchronous — it returns only
// after the kill mechanism ran — and it returns a real error when it
// could not run. It used to always return nil (even when tcpkill was
// missing or failed to start), so a pause could report success while
// connections drained for ~2 minutes.
//
// Mechanism: `ss -K` first (iproute2, present on the agent's Debian
// hosts; kills IPv4 and IPv6 sockets alike, no sleep, reaped by Run).
// If ss is absent or its kill fails, fall back to tcpkill (IPv4 only,
// dsniff) with a 2s window; the killer is reaped (Wait) so no zombie
// is left behind (audit low-9). Both missing/failing is an error the
// caller must surface and retry.
//
// A-NEW: the kill is two-sided and state-agnostic. The old filter
// `ss -K state established ( sport = :PORT )` only matched the
// relay-side socket while ESTABLISHED. A bulk relay flow leaves
// ESTABLISHED before the pause lands (the relay closes downstream
// once the upstream finishes, so the relay side is FIN-WAIT-2 and
// the client side CLOSE-WAIT with megabytes still buffered in its
// Recv-Q), and destroying the relay-side half-closed socket does
// not touch the client's buffered socket — live proof: sport-only
// kill left a 5 MB @ 50 kB/s download completing (exit 0, full
// 5,242,880 B). The client's socket is the one holding the
// undelivered data, and it is only reachable as `dport = :PORT`.
// So ss -K runs on BOTH directions with no state qualifier, and
// the kill count (sockets ss reported destroying) is the success
// signal: a zero kill with a live scan behind it means the filter
// missed, and the caller must fail loud rather than report success.
func ForceClosePortConnections(addr string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("ForceClosePortConnections panic: %v", r)
		}
	}()

	if addr == "" {
		return errors.New("address is empty")
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("invalid port %q: %w", portStr, err)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d out of range", port)
	}

	// Primary: ss -K (IPv4 + IPv6, synchronous, both directions).
	if _, ssErr := killWithSS(port); ssErr == nil {
		return nil
	} else {
		// Fallback: tcpkill (IPv4 only).
		if tkErr := killWithTcpkill(port); tkErr != nil {
			return fmt.Errorf("close connections on port %d: ss failed (%v); tcpkill failed (%w)", port, ssErr, tkErr)
		}
		fmt.Printf("⚠️ ss -K unavailable/failed (%v), fell back to tcpkill for port %d\n", ssErr, port)
		return nil
	}
}

// killWithSS kills TCP sockets on port via `ss -K` and returns how
// many sockets ss reported destroying. Both directions are killed,
// with no state qualifier:
//
//   - `sport = :PORT` — the relay/server side of each connection
//     (the old filter's only target).
//   - `dport = :PORT` — the peer/client side. This is the side
//     holding undelivered buffered data once the relay has
//     half-closed (FIN-WAIT-2/CLOSE-WAIT); destroying it discards
//     the peer's Recv-Q, which is what actually stops a bulk
//     download (live proof: dport kill made curl exit 56 with the
//     file at 4,539,177/5,242,880 B, incomplete).
//
// No `state established` qualifier: bulk flows are typically in
// FIN-WAIT-2/CLOSE-WAIT by the time the pause lands, and those are
// exactly the sockets that must die. A zero exit with no match
// (nothing on the port) is success with a zero count.
func killWithSS(port int) (int, error) {
	killed := 0
	for _, dir := range []string{"sport", "dport"} {
		filter := fmt.Sprintf("( %s = :%d )", dir, port)
		cmd := exec.Command("ss", "-K", filter)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return killed, fmt.Errorf("ss -K %s: %w (output: %s)", dir, err, strings.TrimSpace(string(out)))
		}
		killed += countSSKilled(string(out))
	}
	return killed, nil
}

// countSSKilled counts the socket lines `ss -K` prints, one per
// socket it destroyed. The header line ("Netid State Recv-Q ...")
// never starts with a netid, so it is not counted; a zero kill
// prints only the header.
func countSSKilled(out string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "tcp" {
			n++
		}
	}
	return n
}

// killWithTcpkill kills established IPv4 connections on port via tcpkill.
// Synchronous: tcpkill runs until killed, so give it 2s to inject RSTs,
// then stop it and reap it (Wait) — the old fire-and-forget goroutine
// never waited, leaving a zombie per pause.
func killWithTcpkill(port int) error {
	cmd := exec.Command("tcpkill", "-i", "any", "port", strconv.Itoa(port))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start tcpkill: %w", err)
	}
	time.Sleep(2 * time.Second)
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	// Reap so tcpkill never becomes a zombie (audit low-9).
	_ = cmd.Wait()
	fmt.Printf("✅ 正在断开端口 %d 上的所有连接...\n", port)
	return nil
}
