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

// ForceClosePortConnections kills every established TCP connection whose
// LOCAL port is addr's port (C-04). Pause semantics are "disconnect now,
// uniformly": a connection that keeps draining after a pause is the filed
// bug, not a drain window. The function is synchronous — it returns only
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

	// Primary: ss -K (IPv4 + IPv6, synchronous).
	if ssErr := killWithSS(port); ssErr == nil {
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

// killWithSS kills established TCP sockets whose local port is port via
// `ss -K`. The filter matches both address families. A zero exit with
// no match (nothing established) is success.
func killWithSS(port int) error {
	filter := fmt.Sprintf("( sport = :%d )", port)
	cmd := exec.Command("ss", "-K", "state", "established", filter)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ss -K: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
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
