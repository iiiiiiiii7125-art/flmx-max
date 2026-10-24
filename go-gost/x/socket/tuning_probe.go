package socket

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// One-click line probe (TuneProbe), added for the 3.3.42 tuning port.
// Upstream 3.3.42 has no auto-detection: bandwidth and RTT are manual
// inputs. This probe only produces SUGGESTED values for the tuning
// inputs; it never applies anything.
//
// Read-only guard: probeTune must not call applyTuning, tuneWrite,
// writeFileAtomic, or touch tuneConfPath / tuneBackupPath /
// tuneParamsPath / tuneBBRModule. It only reads kernel socket state
// (sock_diag) and, when explicitly asked, downloads from a caller
// supplied URL. A value only becomes effective when the user later
// confirms it with an explicit TuneApply, which runs the existing
// Apply chain unchanged. The regression test TestTuneProbeDoesNotWrite
// snapshots all four write surfaces before/after a probe.

type tuneProbeRequest struct {
	// WithSpeed is the contract field (frontend: {action:"probe",
	// withSpeed}): explicitly opt into the short speed test. Off by
	// default. MeasureBandwidth is the same switch under its earlier
	// name, kept as an alias so older callers keep working; either
	// being true enables the test.
	WithSpeed        bool   `json:"withSpeed"`
	MeasureBandwidth bool   `json:"measureBandwidth"`
	SpeedtestURL     string `json:"speedtestURL"`
	ReferenceHost    string `json:"referenceHost"`
}

type tuneProbeResult struct {
	// Suggested is what the tuning inputs should be filled with. Each
	// leg falls back independently to the standing default
	// (1000 Mbps / 150 ms, T-5) when its own measurement is
	// unavailable; the fallback is always explained in Warnings and
	// in the per-leg Source fields, never silently substituted.
	Suggested tuneParams `json:"suggested"`
	// RTTMedianMs is the measured median RTT in ms (clamped to the
	// valid tune range), or nil when unavailable. This is the
	// contract field name (frontend reads rttMedianMs); the earlier
	// rttMs name was renamed to match, not kept as a duplicate.
	RTTMedianMs    *int   `json:"rttMedianMs,omitempty"`
	RTTSamples     int    `json:"rttSamples"`
	RTTAvailable   bool   `json:"rttAvailable"`
	RTTSource      string `json:"rttSource"` // "tcp_info_median" | "unavailable"
	BandwidthMbps  *int   `json:"bandwidthMbps,omitempty"`
	BandwidthAvail bool   `json:"bandwidthAvailable"`
	BandwidthSrc   string `json:"bandwidthSource"` // "speedtest" | "not_requested" | "unavailable"
	// PanelRTTMs is a REFERENCE value only: the TCP-connect RTT
	// median to the panel. The agent fills ReferenceHost from its
	// live WebSocket connection to the panel (see routeCommand) when
	// the caller did not supply one, so the frontend contract
	// ({id, action:"probe", withSpeed}, no host field) still gets a
	// panelRttMs while connected; an explicit ReferenceHost wins.
	// Absent when neither is available — never fabricated from the
	// probe call's own duration.
	PanelRTTMs    *int     `json:"panelRttMs,omitempty"`
	PanelRTTAvail bool     `json:"panelRttAvailable"`
	Warnings      []string `json:"warnings,omitempty"`
}

// Seams for tests (and for non-Linux builds, where the sock_diag
// sampler reports unavailable instead of failing the probe).
var (
	tuneProbeTCPRTTs     = sockDiagTCPRTTs
	tuneProbeSpeedtest   = probeSpeedtestDownload
	tuneProbeReferenceRT = probeReferenceConnectRTT
)

const (
	probeSpeedtestTimeout  = 10 * time.Second
	probeSpeedtestMaxBytes = 64 << 20
	probeReferenceTimeout  = 2 * time.Second
	probeReferenceTries    = 3
)

// tuneProbeMedianRTTMs reduces raw kernel RTT samples (µs, from
// TCP_INFO) to a median in ms. Samples <= 0 carry no measurement yet
// and are dropped; no usable sample means unavailable, never 0 ms.
func tuneProbeMedianRTTMs(samples []uint32) (ms int, samplesUsed int, ok bool) {
	var vals []uint32
	for _, v := range samples {
		if v > 0 {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return 0, 0, false
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	var medUs uint64
	if len(vals)%2 == 1 {
		medUs = uint64(vals[len(vals)/2])
	} else {
		medUs = (uint64(vals[len(vals)/2-1]) + uint64(vals[len(vals)/2])) / 2
	}
	// µs -> ms, rounded half up; a sub-millisecond median is still a
	// real measurement, so it floors at 1 ms rather than 0.
	ms = int((medUs + 500) / 1000)
	if ms < 1 {
		ms = 1
	}
	return ms, len(vals), true
}

func clampProbeValue(v, lo, hi int) (int, bool) {
	if v < lo {
		return lo, true
	}
	if v > hi {
		return hi, true
	}
	return v, false
}

// probeSpeedtestDownload measures download throughput against url for
// at most probeSpeedtestTimeout / probeSpeedtestMaxBytes, whichever
// comes first. It runs synchronously inside the TuneProbe command (no
// background job, no resident worker) and is only invoked when the
// caller explicitly set withSpeed with a URL.
func probeSpeedtestDownload(ctx context.Context, url string) (bytes int64, elapsed time.Duration, err error) {
	ctx, cancel := context.WithTimeout(ctx, probeSpeedtestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, err
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("测速地址返回 HTTP %d", resp.StatusCode)
	}
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, probeSpeedtestMaxBytes))
	elapsed = time.Since(start)
	if err != nil {
		return 0, 0, err
	}
	if n == 0 || elapsed <= 0 {
		return 0, 0, fmt.Errorf("测速无有效数据")
	}
	return n, elapsed, nil
}

// probeReferenceConnectRTT measures TCP-connect RTT to host
// (host:port) over probeReferenceTries attempts and returns the median
// in ms. Connect time, not application RTT, and labelled as a
// reference value in the result.
func probeReferenceConnectRTT(host string) (int, error) {
	var samples []int
	for i := 0; i < probeReferenceTries; i++ {
		start := time.Now()
		conn, err := net.DialTimeout("tcp", host, probeReferenceTimeout)
		if err != nil {
			continue
		}
		ms := int(time.Since(start).Milliseconds())
		_ = conn.Close()
		if ms < 1 {
			ms = 1
		}
		samples = append(samples, ms)
	}
	if len(samples) == 0 {
		return 0, fmt.Errorf("无法连接 %s", host)
	}
	sort.Ints(samples)
	if len(samples)%2 == 1 {
		return samples[len(samples)/2], nil
	}
	return (samples[len(samples)/2-1] + samples[len(samples)/2]) / 2, nil
}

// probeTune is the read-only probe body. It never writes: see the
// package comment at the top of this file.
func probeTune(req tuneProbeRequest) (tuneProbeResult, error) {
	res := tuneProbeResult{
		Suggested:    defaultTuneParams,
		RTTSource:    "unavailable",
		BandwidthSrc: "not_requested",
	}

	// Leg 1: RTT = median TCP_INFO rtt over the node's established
	// TCP connections (sock_diag, see tuning_probe_linux.go).
	if samples, err := tuneProbeTCPRTTs(); err != nil {
		res.Warnings = append(res.Warnings, "现有 TCP 连接内核 RTT 不可用（sock_diag 取数失败），延迟回落默认值 150ms")
	} else if ms, used, ok := tuneProbeMedianRTTMs(samples); !ok {
		res.Warnings = append(res.Warnings, "节点暂无可测 RTT 的已建立 TCP 连接，延迟回落默认值 150ms")
	} else {
		res.RTTSamples = used
		clamped, wasClamped := clampProbeValue(ms, 1, 2000)
		if wasClamped {
			res.Warnings = append(res.Warnings, fmt.Sprintf("实测 RTT 中位数 %dms 超出调优范围，已钳制到 %dms", ms, clamped))
		}
		res.RTTMedianMs = &clamped
		res.RTTAvailable = true
		res.RTTSource = "tcp_info_median"
		res.Suggested.RTTMs = clamped
	}

	// Leg 2: bandwidth = optional short speed test. Default is NOT
	// to run it; only an explicit withSpeed request (contract field;
	// measureBandwidth is the same switch under its earlier name)
	// runs it, and only against a caller-supplied http(s) URL.
	if req.WithSpeed || req.MeasureBandwidth {
		url := strings.TrimSpace(req.SpeedtestURL)
		switch {
		case url == "":
			res.BandwidthSrc = "unavailable"
			res.Warnings = append(res.Warnings, "已请求测速但未提供测速地址，带宽回落默认值 1000Mbps")
		case !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://"):
			return tuneProbeResult{}, fmt.Errorf("测速地址须为 http(s) 链接")
		default:
			n, elapsed, err := tuneProbeSpeedtest(context.Background(), url)
			if err != nil {
				res.BandwidthSrc = "unavailable"
				res.Warnings = append(res.Warnings, fmt.Sprintf("测速失败（%v），带宽回落默认值 1000Mbps", err))
			} else {
				mbps := int(float64(n) * 8 / elapsed.Seconds() / 1e6)
				clamped, wasClamped := clampProbeValue(mbps, 1, 100000)
				if wasClamped {
					res.Warnings = append(res.Warnings, fmt.Sprintf("实测带宽 %dMbps 超出调优范围，已钳制到 %dMbps", mbps, clamped))
				}
				res.BandwidthMbps = &clamped
				res.BandwidthAvail = true
				res.BandwidthSrc = "speedtest"
				res.Suggested.BandwidthMbps = clamped
			}
		}
	}

	// Leg 3 (optional reference): TCP-connect RTT to the reference
	// host. The caller may supply one explicitly; otherwise the
	// agent's command router fills it from the live WebSocket
	// connection to the panel (see routeCommand), so the frontend
	// contract needs no host field. Absent when neither is
	// available; a failed reference is simply unavailable, it never
	// changes the suggested line.
	if strings.TrimSpace(req.ReferenceHost) != "" {
		if ms, err := tuneProbeReferenceRT(strings.TrimSpace(req.ReferenceHost)); err == nil {
			res.PanelRTTMs = &ms
			res.PanelRTTAvail = true
		}
	}

	return res, nil
}
