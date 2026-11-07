package socket

import (
	"context"
	"fmt"
	"io"
	"log"
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
	RTTMedianMs  *int   `json:"rttMedianMs,omitempty"`
	RTTSamples   int    `json:"rttSamples"`
	RTTAvailable bool   `json:"rttAvailable"`
	RTTSource    string `json:"rttSource"` // "tcp_info_median" | "unavailable"
	// RTT distribution over the same sock_diag samples the median
	// came from: the sampler returns per-connection kernel RTTs, so
	// min / p95 / max are real per-sample measurements (computed by
	// tuneProbeRTTDistributionMs), absent when the median is
	// unavailable. Unlike RTTMedianMs they are NOT clamped to the
	// tune range — they are display values only and never feed
	// Suggested. This leg is a snapshot of already-established
	// connections: it has no send counter and no sample ordering,
	// so it cannot yield a loss rate or a jitter; those two come
	// from the reference probe series (PanelRTT* fields below).
	RTTMinMs       *int   `json:"rttMinMs,omitempty"`
	RTTP95Ms       *int   `json:"rttP95Ms,omitempty"`
	RTTMaxMs       *int   `json:"rttMaxMs,omitempty"`
	BandwidthMbps  *int   `json:"bandwidthMbps,omitempty"`
	BandwidthAvail bool   `json:"bandwidthAvailable"`
	BandwidthSrc   string `json:"bandwidthSource"` // "speedtest" | "not_requested" | "unavailable"
	// PanelRTTMs is a REFERENCE value only: the median TCP-connect
	// RTT of the reference probe series (see the PanelRTT* series
	// fields below). The agent fills ReferenceHost from its live
	// WebSocket connection to the panel (see routeCommand) when the
	// caller did not supply one, so the frontend contract
	// ({id, action:"probe", withSpeed}, no host field) still gets a
	// panelRttMs while connected; an explicit ReferenceHost wins.
	// Absent when the series produced no successful sample — never
	// fabricated from the probe call's own duration.
	PanelRTTMs    *int `json:"panelRttMs,omitempty"`
	PanelRTTAvail bool `json:"panelRttAvailable"`
	// Reference-leg probe series (FIX-PROBE): one probe round runs
	// probeSeriesCount sequential TCP connects to the reference
	// host and reports the series outcome in the fields below. This
	// is the only leg that can measure loss and jitter. Sent /
	// Received count the attempts; LossRate = (Sent-Received)/Sent
	// is present whenever the series ran — 1 on a total-loss round,
	// an explicit measurement, never a fabricated 0 ms / 0 % stand-
	// in for "unmeasurable". JitterMs is the mean absolute
	// difference between consecutive successful samples' RTTs, in
	// send order (jitter口径 = 相邻差均值). Min / P95 / Max are
	// over the successful samples (nearest-rank p95). Pointer
	// fields are absent when not measurable: median / min / max
	// need >= 1 success, p95 and jitter need >= 2; when the
	// reference leg did not run at all Sent stays 0 and LossRate
	// is absent.
	PanelRTTSent     int      `json:"panelRttSent"`
	PanelRTTReceived int      `json:"panelRttReceived"`
	PanelRTTLossRate *float64 `json:"panelRttLossRate,omitempty"`
	PanelRTTMinMs    *int     `json:"panelRttMinMs,omitempty"`
	PanelRTTP95Ms    *int     `json:"panelRttP95Ms,omitempty"`
	PanelRTTMaxMs    *int     `json:"panelRttMaxMs,omitempty"`
	PanelRTTJitterMs *float64 `json:"panelRttJitterMs,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
}

// tcpRTTSamples is the sock_diag RTT leg result: the raw per-socket RTT
// samples plus, per family (index 0 = IPv4, 1 = IPv6), the dump error
// when that family's dump failed. A single failed family must not be
// silent: the caller logs it and warns that the median covers only the
// surviving family (148-P3-5).
type tcpRTTSamples struct {
	RTTs         []uint32
	FamilyErrors [2]error
}

// sockDiagFamilyNames indexes tcpRTTSamples.FamilyErrors.
var sockDiagFamilyNames = [2]string{"IPv4", "IPv6"}

// Seams for tests (and for non-Linux builds, where the sock_diag
// sampler reports unavailable instead of failing the probe).
var (
	tuneProbeTCPRTTs     = sockDiagTCPRTTs
	tuneProbeSpeedtest   = probeSpeedtestDownload
	tuneProbeReferenceRT = probeReferenceSeries
)

const (
	probeSpeedtestTimeout  = 10 * time.Second
	probeSpeedtestMaxBytes = 64 << 20
	probeReferenceTimeout  = 2 * time.Second
	// One reference probe round is probeSeriesCount sequential
	// connects spaced probeSeriesInterval apart: enough samples
	// for a loss rate, a jitter and a p95, still a light diagnostic
	// (10 short connects), not a load test.
	probeSeriesCount    = 10
	probeSeriesInterval = 100 * time.Millisecond
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
	// R7-C-02: judge the target before any dial (hard-deny always,
	// non-public rejected by default); the client re-judges every
	// redirect hop and pins the dial — see outbound_guard.go.
	if err := validateProbeSpeedtestURL(ctx, url); err != nil {
		return 0, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, err
	}
	start := time.Now()
	resp, err := probeSpeedtestClient().Do(req)
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

// probeSeriesStats is the outcome of one reference probe round:
// probeSeriesCount sequential TCP connects to the reference host.
type probeSeriesStats struct {
	Sent     int
	Received int
	// RTTsMs holds the connect RTT of each successful attempt, in
	// send order (failed attempts leave no entry). Jitter is
	// defined over consecutive entries of this slice.
	RTTsMs []int
}

func (s probeSeriesStats) sortedRTTs() []int {
	cp := append([]int(nil), s.RTTsMs...)
	sort.Ints(cp)
	return cp
}

// medianMs is the median connect RTT over the successful samples,
// with the same even-count averaging as the sock_diag leg. ok is
// false when no attempt succeeded.
func (s probeSeriesStats) medianMs() (int, bool) {
	if len(s.RTTsMs) == 0 {
		return 0, false
	}
	vals := s.sortedRTTs()
	if len(vals)%2 == 1 {
		return vals[len(vals)/2], true
	}
	return (vals[len(vals)/2-1] + vals[len(vals)/2]) / 2, true
}

// lossRate is (sent-received)/sent in [0,1]. ok is false only when
// the series never ran (sent == 0); a total-loss round yields
// exactly 1, which is a measurement, not an "unavailable" marker.
func (s probeSeriesStats) lossRate() (float64, bool) {
	if s.Sent == 0 {
		return 0, false
	}
	return float64(s.Sent-s.Received) / float64(s.Sent), true
}

// jitterMs is the mean absolute difference between consecutive
// successful samples' RTTs, in send order (相邻差均值). It needs
// at least two successful samples; fewer means not measurable.
func (s probeSeriesStats) jitterMs() (float64, bool) {
	if len(s.RTTsMs) < 2 {
		return 0, false
	}
	sum := 0
	for i := 1; i < len(s.RTTsMs); i++ {
		d := s.RTTsMs[i] - s.RTTsMs[i-1]
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return float64(sum) / float64(len(s.RTTsMs)-1), true
}

// distributionMs returns min / p95 / max over the successful
// samples; p95 is nearest-rank over the sorted samples. ok is
// false when no attempt succeeded. (Callers expose p95 only with
// >= 2 samples — with one sample it would just restate it.)
func (s probeSeriesStats) distributionMs() (minMs, p95Ms, maxMs int, ok bool) {
	if len(s.RTTsMs) == 0 {
		return 0, 0, 0, false
	}
	vals := s.sortedRTTs()
	rank := (95*len(vals) + 99) / 100 // ceil(0.95*n), 1-based
	if rank < 1 {
		rank = 1
	}
	return vals[0], vals[rank-1], vals[len(vals)-1], true
}

// probeReferenceSeries runs one reference probe round against host
// (host:port): probeSeriesCount sequential TCP connects, spaced
// probeSeriesInterval apart, and reports the full series outcome
// (sent / received / per-sample RTTs). Dial failures are counted
// in the series, not returned as errors; an error means the target
// itself was judged invalid before any dial. Connect time, not
// application RTT, and labelled as a reference value in the result.
func probeReferenceSeries(host string) (probeSeriesStats, error) {
	// R7-C-02: judge before dialing (hard-deny always, non-public
	// rejected by default, port range checked) and dial the
	// validated IPs, not the name — see outbound_guard.go.
	_, port, ips, err := validateProbeReferenceHost(context.Background(), host)
	if err != nil {
		return probeSeriesStats{}, err
	}
	dialer := &net.Dialer{Timeout: probeReferenceTimeout}
	var st probeSeriesStats
	for i := 0; i < probeSeriesCount; i++ {
		if i > 0 {
			time.Sleep(probeSeriesInterval)
		}
		st.Sent++
		start := time.Now()
		var conn net.Conn
		var derr error
		for _, ip := range ips {
			dialIP := ip
			if ip4 := ip.To4(); ip4 != nil {
				dialIP = ip4
			}
			conn, derr = dialer.Dial("tcp", net.JoinHostPort(dialIP.String(), port))
			if derr == nil {
				break
			}
		}
		if derr != nil {
			continue
		}
		ms := int(time.Since(start).Milliseconds())
		_ = conn.Close()
		if ms < 1 {
			ms = 1
		}
		st.Received++
		st.RTTsMs = append(st.RTTsMs, ms)
	}
	return st, nil
}

// probeReferenceConnectRTT is the median-only view of
// probeReferenceSeries for callers that need a single reference
// value; an error means the target was judged invalid or no
// attempt in the series succeeded.
func probeReferenceConnectRTT(host string) (int, error) {
	st, err := probeReferenceSeries(host)
	if err != nil {
		return 0, err
	}
	med, ok := st.medianMs()
	if !ok {
		return 0, fmt.Errorf("无法连接 %s", host)
	}
	return med, nil
}

// tuneProbeRTTDistributionMs reduces the raw sock_diag RTT samples
// (µs) to min / p95 / max in ms, with the same >0 validity filter
// and half-up rounding (1 ms floor) as tuneProbeMedianRTTMs. p95
// is nearest-rank over the sorted samples. ok is false when no
// usable sample exists.
func tuneProbeRTTDistributionMs(samples []uint32) (minMs, p95Ms, maxMs int, ok bool) {
	var vals []uint32
	for _, v := range samples {
		if v > 0 {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return 0, 0, 0, false
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	toMs := func(us uint32) int {
		ms := int((uint64(us) + 500) / 1000)
		if ms < 1 {
			ms = 1
		}
		return ms
	}
	rank := (95*len(vals) + 99) / 100 // ceil(0.95*n), 1-based
	if rank < 1 {
		rank = 1
	}
	return toMs(vals[0]), toMs(vals[rank-1]), toMs(vals[len(vals)-1]), true
}

// probeTune is the read-only probe body. It never writes: see the
// package comment at the top of this file.
func probeTune(req tuneProbeRequest) (tuneProbeResult, error) {
	return probeTuneContext(context.Background(), req)
}

// probeTuneContext is probeTune under the caller's command budget
// (D1-06): the speedtest leg stops when the budget does instead of
// running on for a panel that already timed out.
func probeTuneContext(ctx context.Context, req tuneProbeRequest) (tuneProbeResult, error) {
	res := tuneProbeResult{
		Suggested:    defaultTuneParams,
		RTTSource:    "unavailable",
		BandwidthSrc: "not_requested",
	}

	// Leg 1: RTT = median TCP_INFO rtt over the node's established
	// TCP connections (sock_diag, see tuning_probe_linux.go).
	if sample, err := tuneProbeTCPRTTs(); err != nil {
		res.Warnings = append(res.Warnings, "现有 TCP 连接内核 RTT 不可用（sock_diag 取数失败），延迟回落默认值 150ms")
	} else {
		// 148-P3-5: a single-family dump failure used to vanish —
		// the median silently covered only the surviving family.
		// Name the failed family in a warning (shown in the panel's
		// fallback display) and log it; the median itself still
		// comes from the surviving family's samples.
		for i, ferr := range sample.FamilyErrors {
			if ferr == nil {
				continue
			}
			failed, surviving := sockDiagFamilyNames[i], sockDiagFamilyNames[1-i]
			log.Printf("tune probe: %s sock_diag dump failed, RTT median covers %s only: %v", failed, surviving, ferr)
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s sock_diag 取数失败，中位数仅基于 %s 连接", failed, surviving))
		}
		if ms, used, ok := tuneProbeMedianRTTMs(sample.RTTs); !ok {
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
			if mn, p95, mx, dok := tuneProbeRTTDistributionMs(sample.RTTs); dok {
				res.RTTMinMs = &mn
				res.RTTP95Ms = &p95
				res.RTTMaxMs = &mx
			}
		}
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
			n, elapsed, err := tuneProbeSpeedtest(ctx, url)
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

	// Leg 3 (optional reference): probe series of TCP connects to
	// the reference host — the loss / jitter / distribution leg.
	// The caller may supply one explicitly; otherwise the
	// agent's command router fills it from the live WebSocket
	// connection to the panel (see routeCommand), so the frontend
	// contract needs no host field. Absent when neither is
	// available; a failed reference is simply unavailable, it never
	// changes the suggested line.
	if strings.TrimSpace(req.ReferenceHost) != "" {
		if st, err := tuneProbeReferenceRT(strings.TrimSpace(req.ReferenceHost)); err == nil {
			res.PanelRTTSent = st.Sent
			res.PanelRTTReceived = st.Received
			if rate, ok := st.lossRate(); ok {
				res.PanelRTTLossRate = &rate
			}
			if med, ok := st.medianMs(); ok {
				res.PanelRTTMs = &med
				res.PanelRTTAvail = true
			}
			if mn, p95, mx, dok := st.distributionMs(); dok {
				res.PanelRTTMinMs = &mn
				res.PanelRTTMaxMs = &mx
				if st.Received >= 2 {
					res.PanelRTTP95Ms = &p95
				}
			}
			if jit, ok := st.jitterMs(); ok {
				res.PanelRTTJitterMs = &jit
			}
			if st.Received == 0 {
				res.Warnings = append(res.Warnings, "参考主机探针整轮无响应（丢包 100%），其延迟与抖动不可测")
			}
		}
	}

	return res, nil
}
