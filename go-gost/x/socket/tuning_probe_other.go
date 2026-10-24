//go:build !linux

package socket

import "errors"

// Non-Linux builds have no sock_diag source: the probe reports RTT
// unavailable (and falls back to the default) instead of failing.
func sockDiagTCPRTTs() (tcpRTTSamples, error) {
	return tcpRTTSamples{}, errors.New("sock_diag 仅 Linux 可用")
}
