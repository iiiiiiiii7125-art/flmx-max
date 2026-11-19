package relay

import (
	"os"
	"strings"
	"time"

	mdata "github.com/go-gost/core/metadata"
	mdutil "github.com/go-gost/x/metadata/util"
)

// UDP egress pacing (Fix-P2).
//
// Drop point: the sing-box SS2022 inbound's packetOutbound queue
// (capacity 64, one loopUDPOut goroutine, non-blocking writes
// that silently drop when full). A 200-packet zero-interval
// burst reaches it in ~3ms and is lost from packet 65 on.
// Pacing the exit leg's writes to the dialed target spreads the
// burst — and the echo replies it generates — so the queue
// drains faster than packets arrive.
//
// Scope: handleConnect only. A relay service with a chain runs
// handleForward (middle leg); a relay service without a chain is
// the terminal leg, and handleConnect dials the final target
// directly. Pacing therefore touches only the exit UDP leg:
// TCP legs, middle legs and the return path (reads are never
// paced) keep today's behaviour, as do session TTL and PROXY
// handling.

// defaultUDPEgressPacing spreads a 200-packet burst over 5ms
// (200 x 25us). It applies when nothing configures the interval.
const defaultUDPEgressPacing = 25 * time.Microsecond

// udpEgressPacingEnv is a node-local override of the pacing
// interval, read at handler Init: "25us", "50us", or "0" to
// disable. It wins over the service metadata below, so a node
// can be tuned or unpaced without a panel republish.
const udpEgressPacingEnv = "FLMX_UDP_EGRESS_PACING"

// resolveUDPEgressPacing resolves the pacing interval:
// environment override > service metadata "udpEgressPacing" >
// default. An explicitly configured 0 disables pacing.
func resolveUDPEgressPacing(md mdata.Metadata) time.Duration {
	interval := defaultUDPEgressPacing
	if md != nil && md.IsExists("udpEgressPacing") {
		interval = mdutil.GetDuration(md, "udpEgressPacing")
	}
	if v := strings.TrimSpace(os.Getenv(udpEgressPacingEnv)); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			interval = d
		}
	}
	return interval
}
