package handler

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/netguard"
)

// --- R7-C-02: panel-side probe target policy (before dispatch) ---
//
// The retained probe leg measures a TCP-connect RTT reference
// against referenceHost (see "Line probe" below). That host is
// caller-supplied and handed to the agent, which dials it — a
// blind SSRF / port-scan primitive into every managed node's
// network if judged by nobody. The panel judges the target with
// netguard PolicyPublicOnly before dispatch (hard-deny layer
// always; every non-public range rejected by default), and the
// agent re-judges before executing (see x/socket
// outbound_guard.go), the same panel-checks + agent-backstops
// shape as the federation face. Hosts are judged on their
// resolved IPs (fail-closed on resolution failure); url.Parse
// Hostname() is the real host, so userinfo masking cannot smuggle
// a target past.
//
// The retired speed leg (speedtestURL download measurement) had
// its own validator, validateSpeedtestURLDispatch; it was removed
// together with that leg (see the retirement note in nodeTune) —
// its only consumer was the retired BDP sizing derivation.
// referenceHost keeps the full R7-C-02 treatment below.
//
// Allowlist design (explicit, no silent hole): there is deliberately
// NO private-target exception here. A legitimate private probe
// target needs a new explicit channel — a panel-side CIDR
// allowlist (e.g. FLMX_PROBE_PRIVATE_ALLOWLIST) judged here and
// forwarded to the agent in the probe payload for re-judging —
// which does not exist yet; until it does, private probe targets
// are rejected.

var probeLookupIP = net.LookupIP

func judgeProbeDispatchHost(host string) error {
	var ips []net.IP
	var err error
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ips, err = probeLookupIP(host)
		if err != nil || len(ips) == 0 {
			return fmt.Errorf("无法解析，已拒绝: %s", host)
		}
	}
	for _, ip := range ips {
		if d := netguard.Judge(ip, netguard.PolicyPublicOnly, false); !d.Allowed {
			return fmt.Errorf("指向非公网地址，已拒绝: %s", host)
		}
	}
	return nil
}

func validateReferenceHostDispatch(ref string) error {
	host, portStr, err := net.SplitHostPort(ref)
	if err != nil {
		return fmt.Errorf("参考地址须为 host:port 格式")
	}
	port, perr := strconv.Atoi(portStr)
	if perr != nil || port < 1 || port > 65535 {
		return fmt.Errorf("参考地址端口超出范围，已拒绝: %s", portStr)
	}
	if err := judgeProbeDispatchHost(host); err != nil {
		return fmt.Errorf("参考地址%s", err.Error())
	}
	return nil
}

const nodeTuneTimeout = 30 * time.Second

// nodeTuneProbeTimeout is the panel->agent timeout for probe only.
// The retained probe legs are bounded well under this on the agent
// (sock_diag RTT sampling plus up to three short reference-host
// connect dials); the 90s here is panel-side headroom for loaded
// nodes / WS queueing, not the binding constraint. The frontend
// nodeTune export uses a single 40s literal for every call, so the
// ordering agent < frontend (40s) < panel probe (90s) holds;
// scripts/check-timeout-budgets.sh (P003 pin branch) reconciles
// the extracted FE 40s against this constant, so the ordering
// cannot silently invert. Status/apply keep the 30s timeout.
const nodeTuneProbeTimeout = 90 * time.Second

// --- Node tuning, four-face model (vpsing tcp.sh lineage) ---
//
// The pre-2026-11 tuning model — manual bandwidth/RTT line input
// sized by BDP (groups bbr/buffer/tcp/backlog/forward/files), and
// background auto/deep tuning jobs (TuneStart/TuneCancel) — is
// retired. Tuning is now the reference script's four independent
// faces, each applied and reported on its own:
//
//	ipv4    IPv4-preferred name resolution (/etc/gai.conf)
//	bbr     BBR congestion control + fq qdisc
//	kernel  the fixed production sysctl set (buffers sized from
//	        total memory, MSS clamp rule, limits.d nofile/nproc)
//	nic     NIC multi-queue balancing (ring size, RPS masks)
//
// Wire contract with the agent (T1 lane, go-gost/x/socket):
//   - action ""/"status" -> command "TuneStatus" (no payload). The
//     agent replies with the status shape
//     {faces:{ipv4,bbr,kernel,nic}, congestion, qdisc} — four
//     booleans plus the live congestion-control / default-qdisc
//     summary. The panel passes the reply through untouched.
//   - actions "apply-ipv4" / "apply-bbr" / "apply-kernel" /
//     "apply-nic" / "rollback" -> command "TuneFaces" with payload
//     {"faceAction": <action>}. The agent executes the one face
//     (or the full rollback) and replies with the fresh status
//     shape. Every action id is a closed enum judged here before
//     any node traffic; the agent re-validates the same enum, so
//     no caller-supplied string ever reaches a sysctl key, a file
//     path or a shell on the node.
//   - action "probe" -> command "TuneProbe" (see "Line probe").
//
// "rollback" is the reference script's full rollback: delete the
// tuning files (sysctl.d performance conf, limits.d conf, BBR
// conf), force the in-kernel congestion control back to cubic and
// the default qdisc to pfifo_fast, remove the MSS clamp rule,
// zero the RPS flow entries/masks and restore gai.conf. It is NOT
// the retired "restore"/"restoreAuto": those replayed the captured
// pre-tuning sysctl values (and, for restoreAuto, removed the HTB
// shaper and tuning results); both actions are rejected below.
//
// --- Line probe (retained, narrowed) ---
//
// Action "probe" keeps exactly two measurement legs, both
// read-only diagnostics of the node's return path — latency, loss,
// jitter and the RTT distribution — presented in the frontend's
// standalone 线路探针 panel next to (not inside) the four faces:
//
//   - referenceHost: TCP-connect RTT to a caller-supplied host:port
//     (the agent auto-fills the panel address from its WS
//     connection when absent). Judged by R7-C-02 above before
//     dispatch; the agent re-judges before dialing.
//   - the agent-internal sock_diag RTT sampling over the node's
//     established TCP connections.
//
// Retired with the old model: the withSpeed / speedtestURL
// download speed-test leg. Its only consumer was the retired BDP
// sizing derivation; the four-face kernel set is sized from total
// memory and consumes no bandwidth value, so the panel no longer
// sends withSpeed/speedtestURL and the frontend offers no such
// entry. (Agent-side: the probe's Suggested bandwidth/RTT fields
// fed the same retired derivation — T1 disposes of that leg in
// x/socket; the panel contract no longer carries it.)

// nodeTuneFaceActions is the closed set of mutating actions of the
// four-face model, mapped 1:1 onto the agent's "TuneFaces"
// faceAction enum (T1 wire contract — do not rename).
var nodeTuneFaceActions = map[string]bool{
	"apply-ipv4":   true, // IPv4 优先解析
	"apply-bbr":    true, // BBR + FQ
	"apply-kernel": true, // 生产级内核调优
	"apply-nic":    true, // 网卡多队列均衡
	"rollback":     true, // 一键回退默认
}

func (h *Handler) nodeTune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		ID     int64  `json:"id"`
		Action string `json:"action"`
		// Probe option (action "probe" only): an optional host:port
		// for the TCP-connect RTT reference leg. The agent
		// auto-fills the panel address from its WS connection when
		// absent, so the frontend needs no host field. The retired
		// withSpeed/speedtestURL fields are deliberately absent:
		// decodeJSON rejects unknown fields, so an old caller still
		// sending them gets a parameter error, not a silent
		// speed test.
		ReferenceHost string `json:"referenceHost"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}

	command, data := "TuneStatus", map[string]interface{}{}
	switch req.Action {
	case "", "status":
		// Read-only: reply is the four-face status shape, passed
		// through untouched (no derived annotations — the retired
		// lineSupported/params capability probe went away with the
		// line model it described).
	case "apply-ipv4", "apply-bbr", "apply-kernel", "apply-nic", "rollback":
		if !nodeTuneFaceActions[req.Action] {
			response.WriteJSON(w, response.ErrDefault("未知操作"))
			return
		}
		command, data = "TuneFaces", map[string]interface{}{"faceAction": req.Action}
	case "probe":
		if req.ReferenceHost != "" {
			if _, _, err := net.SplitHostPort(req.ReferenceHost); err != nil {
				response.WriteJSON(w, response.ErrDefault("参考地址须为 host:port 格式"))
				return
			}
		}
		// 复扫 R-02：参考地址透传给 agent 拨号，空标签主机名在
		// 任何 DNS 下都不可解析，写面即拒（只判语法、不做 DNS）。
		if err := validateNoEmptyLabelAddr(req.ReferenceHost); err != nil {
			response.WriteJSON(w, response.ErrDefault("参考地址主机名格式无效："+err.Error()))
			return
		}
		// R7-C-02：netguard 政策判定（硬拒恒拒、私网默认拒），在
		// 下发前拦下打向节点内网/云元数据的参考地址；agent 侧
		// 执行前同口径复判。
		if req.ReferenceHost != "" {
			if err := validateReferenceHostDispatch(req.ReferenceHost); err != nil {
				response.WriteJSON(w, response.ErrDefault(err.Error()))
				return
			}
		}
		command, data = "TuneProbe", map[string]interface{}{}
		if req.ReferenceHost != "" {
			data["referenceHost"] = req.ReferenceHost
		}
	default:
		response.WriteJSON(w, response.ErrDefault("未知操作"))
		return
	}

	timeout := nodeTuneTimeout
	if req.Action == "probe" {
		timeout = nodeTuneProbeTimeout
	}
	res, err := h.sendNodeCommandWithTimeout(req.ID, command, data, timeout, false, false)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "未知命令类型") {
			if req.Action == "probe" {
				// 探测是可选能力：旧 agent 不认 TuneProbe 时只提示，
				// 不拦调优本身。
				msg = "节点 agent 较旧，不支持线路探针，请先升级节点 Agent"
			} else if nodeTuneFaceActions[req.Action] {
				// "TuneFaces" is the new command: an old agent does
				// not know it.
				msg = "节点 agent 版本过旧，不支持新版调优，请先升级节点 Agent"
			} else {
				msg = "节点 agent 版本过旧，不支持调优，请先升级节点"
			}
		}
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("调优失败: %s", msg)))
		return
	}
	response.WriteJSON(w, response.OK(res.Data))
}
