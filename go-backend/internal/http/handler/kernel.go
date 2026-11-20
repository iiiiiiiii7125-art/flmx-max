package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

const (
	kernelApplyTimeout   = 5 * time.Minute
	kernelCommandTimeout = 20 * time.Second

	// kernelVersionPrecheckBudget 是 H131-KVER 版本预检的单段总预算：
	// 一次 tag 查询本应秒级返回，预算只防慢线拖垮安装请求；直连判
	// 活且快时首试即命中，不存在的版本 1–2 秒内被拒，不再走到 agent
	// 侧分钟级的 404 重试。
	kernelVersionPrecheckBudget = 10 * time.Second

	kernelStatusPending = "pending"
	kernelStatusRunning = "running"
	kernelStatusStopped = "stopped"
	kernelStatusError   = "error"
)

var kernelStatusInterval = 60 * time.Second

// errApplyConfirmPending 是下发确认待定哨兵（F2-APPLY-01）：KernelApply
// 的回执超时不等于下发失败——命令已发出、agent 可能已落地，面板须
// 先向 agent 核实再生效状态。文案刻意不含「超时」二字，以免被
// isNodeOfflineOrTimeoutError 一类「离线/超时」归类误判为节点不在线。
// 调用方以 errors.Is 分流后按「已发出、正在确认」回执，不得前缀
// 「下发失败」。
var errApplyConfirmPending = errors.New("下发已发出，正在确认生效状态")

// isKernelApplyTimeoutError 判断下发错误是否为「等回执超时」：命令
// 本身已发出，只有回执没在预算内回来。SendCommand 的超时文本是
// 「等待节点响应超时」；上下文超时的标准文本一并覆盖。
func isKernelApplyTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "等待节点响应超时") ||
		errors.Is(err, context.DeadlineExceeded)
}

// verifyKernelApplyLanded 在 KernelApply 回执超时后向 agent 核实
// 下发是否已落地（F2-APPLY-01）：复用周期 poll（refreshNodeKernelStatus）
// 回读 agent 上报的 configHash，与本次下发的 hash 比对。回读失败或
// hash 不符都只报「未证实」，不替 agent 宣称失败——下一次周期 poll
// 会继续收敛状态。
func (h *Handler) verifyKernelApplyLanded(k *model.NodeKernel, hash string) bool {
	if h == nil || k == nil || hash == "" {
		return false
	}
	h.refreshNodeKernelStatus(k.NodeID)
	cur, err := h.repo.GetNodeKernel(k.ID)
	if err != nil || cur == nil {
		return false
	}
	return cur.AppliedHash == hash
}

// kernelApplyLocks serializes renders and dispatches per kernel so two edits
// in quick succession cannot reach the agent out of order.
//
// P1-07: entries are reference-counted and deleted when the last
// holder/waiter releases, so the map does not grow by one mutex per
// kernel ever touched for the life of the process. kernelApplyLocksMu
// only guards the refcount (never held across the per-kernel lock),
// and the returned release func is idempotent because existing call
// sites invoke it manually on several exit paths.
var kernelApplyLocks sync.Map
var kernelApplyLocksMu sync.Mutex

type kernelApplyLock struct {
	mu   sync.Mutex
	refs int
}

func lockKernelApply(kernelID int64) func() {
	kernelApplyLocksMu.Lock()
	v, _ := kernelApplyLocks.LoadOrStore(kernelID, &kernelApplyLock{})
	l := v.(*kernelApplyLock)
	l.refs++
	kernelApplyLocksMu.Unlock()

	l.mu.Lock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Unlock()
			kernelApplyLocksMu.Lock()
			l.refs--
			if l.refs == 0 {
				kernelApplyLocks.Delete(kernelID)
			}
			kernelApplyLocksMu.Unlock()
		})
	}
}

type kernelView struct {
	ID               int64  `json:"id"`
	NodeID           int64  `json:"nodeId"`
	NodeName         string `json:"nodeName"`
	NodeOnline       bool   `json:"nodeOnline"`
	KernelType       string `json:"kernelType"`
	Version          string `json:"version"`
	InstalledVersion string `json:"installedVersion"`
	Status           string `json:"status"`
	LastError        string `json:"lastError"`
	Synced           bool   `json:"synced"`
	Enabled          bool   `json:"enabled"`
	InboundCount     int    `json:"inboundCount"`
	UpdatedTime      int64  `json:"updatedTime"`
	// E-02: non-empty when enabled companions exist on this node but
	// the split is inactive because the agent version is too old —
	// the companions stay on this (main) kernel; see
	// companionSplitPendingReason.
	CompanionSplitNote string `json:"companionSplitNote,omitempty"`
}

type inboundView struct {
	ID           int64  `json:"id"`
	KernelID     int64  `json:"kernelId"`
	KernelType   string `json:"kernelType"`
	KernelStatus string `json:"kernelStatus"`
	External     bool   `json:"external"`
	// ImportedSummary / ImportedParams describe imported nodes, whose
	// parameters live in the stored proxy rather than the kernel fields.
	ImportedSummary  string             `json:"importedSummary,omitempty"`
	ImportedParams   [][2]string        `json:"importedParams,omitempty"`
	NodeID           int64              `json:"nodeId"`
	NodeName         string             `json:"nodeName"`
	NodeServerIP     string             `json:"nodeServerIp"`
	Name             string             `json:"name"`
	Protocol         string             `json:"protocol"`
	Listen           string             `json:"listen"`
	Port             int                `json:"port"`
	Network          string             `json:"network"`
	Settings         kernel.Settings    `json:"settings"`
	Transport        kernel.Transport   `json:"transport"`
	Security         kernelSecurityView `json:"security"`
	Extra            string             `json:"extra"`
	ShareAddressMode string             `json:"shareAddressMode"`
	ShareAddress     string             `json:"shareAddress"`
	ShareHost        string             `json:"shareHost"`
	Remark           string             `json:"remark"`
	RelayOnly        bool               `json:"relayOnly"`
	RelayCount       int                `json:"relayCount"`
	// RelayInvalidCount (IRL-7) counts relays whose backing forward is
	// deleted or paused; RelayCount itself counts only live relays.
	RelayInvalidCount int    `json:"relayInvalidCount"`
	AcceptProxy       bool   `json:"acceptProxyProtocol"`
	Whitelist         bool   `json:"relayWhitelist"`
	WhitelistExtra    string `json:"whitelistExtra"`
	Enabled           bool   `json:"enabled"`
	UploadBytes       int64  `json:"uploadBytes"`
	DownloadBytes     int64  `json:"downloadBytes"`
	CreatedTime       int64  `json:"createdTime"`
	UpdatedTime       int64  `json:"updatedTime"`
	// Probe is the last reachability check of an imported node.
	Probe *externalProbe `json:"probe,omitempty"`
	// SourceID / SourceName: the remote subscription it came from.
	SourceID   int64  `json:"sourceId,omitempty"`
	SourceName string `json:"sourceName,omitempty"`
	// OutboundID is the inbound this one's traffic is chained through (0 =
	// direct); OutboundMissing flags a target that was deleted.
	OutboundID      int64  `json:"outboundId"`
	OutboundName    string `json:"outboundName,omitempty"`
	OutboundMissing bool   `json:"outboundMissing,omitempty"`
	// SplitRules are the inbound's 分流 rules.
	SplitRules []kernel.SplitRule `json:"splitRules"`
	// OutboundIPStrategy scopes the inbound's outbound connections to an
	// IP family ("auto" = unrestricted).
	OutboundIPStrategy string `json:"outboundIpStrategy"`
	// ConnIdleSeconds is the per-inbound idle timeout (seconds) for
	// Xray (policy.levels[].connIdle); 0 = unset (Xray default 300s),
	// -1 = unlimited (renders as 31536000 = 365d, i.e. never times out).
	ConnIdleSeconds int `json:"connIdleSeconds"`
	// MeteringEnabled is the SS subscription-metering switch;
	// MeteringActive reports that this origin's companion exists AND
	// its config is applied on the node (the same gate subscription
	// pulls use — F4/B-03), i.e. share subscriptions are really being
	// metered right now.
	MeteringEnabled bool `json:"meteringEnabled"`
	MeteringActive  bool `json:"meteringActive"`
}

// kernelSecurityView hides the self-signed private material and DNS API
// credentials from list output; saving without them keeps the stored ones.
type kernelSecurityView struct {
	kernel.Security
	CertPEM       string `json:"certPem,omitempty"`
	KeyPEM        string `json:"keyPem,omitempty"`
	ACMEDNSKey    string `json:"acmeDnsKey,omitempty"`
	ACMEDNSSecret string `json:"acmeDnsSecret,omitempty"`
	ACMEDNSSet    bool   `json:"acmeDnsSet,omitempty"`
}

type inboundRequest struct {
	ID                  int64              `json:"id"`
	NodeID              int64              `json:"nodeId"`
	KernelType          string             `json:"kernelType"`
	Name                string             `json:"name"`
	Protocol            string             `json:"protocol"`
	Listen              string             `json:"listen"`
	Port                int                `json:"port"`
	Settings            kernel.Settings    `json:"settings"`
	Transport           kernel.Transport   `json:"transport"`
	Security            kernel.Security    `json:"security"`
	Extra               string             `json:"extra"`
	ShareAddressMode    *string            `json:"shareAddressMode"`
	ShareHost           string             `json:"shareHost"`
	Remark              string             `json:"remark"`
	RelayOnly           bool               `json:"relayOnly"`
	AcceptProxyProtocol bool               `json:"acceptProxyProtocol"`
	RelayWhitelist      bool               `json:"relayWhitelist"`
	WhitelistExtra      string             `json:"whitelistExtra"`
	OutboundID          int64              `json:"outboundId"`
	SplitRules          []kernel.SplitRule `json:"splitRules"`
	// OutboundIPStrategy is a pointer so an edit that omits the field
	// keeps the stored strategy instead of resetting it to auto.
	OutboundIPStrategy *string `json:"outboundIpStrategy"`
	// ConnIdleSeconds is a pointer for the same reason: an edit that
	// omits it keeps the stored timeout; an explicit 0 clears it (back
	// to Xray default 300s), -1 sets unlimited, and a value sets it.
	ConnIdleSeconds *int  `json:"connIdleSeconds"`
	Enabled         *bool `json:"enabled"`
	// MeteringEnabled is a pointer for the same reason: an edit that
	// omits it keeps the SS subscription-metering switch as stored.
	MeteringEnabled *bool `json:"meteringEnabled"`
}

// ---- conversion ----

func inboundFromModel(row model.NodeInbound) (kernel.Inbound, error) {
	in := kernel.Inbound{ID: row.ID, Protocol: row.Protocol, Listen: row.Listen, Port: row.Port,
		AcceptProxyProtocol: row.AcceptProxyProtocol == 1, OutboundIPStrategy: row.OutboundIPStrategy,
		ConnIdleSeconds: row.ConnIdleSeconds,
		Companion:       row.CompanionOf > 0}
	if err := unmarshalOptional(row.Settings, &in.Settings); err != nil {
		return in, fmt.Errorf("入站 %d settings 损坏: %v", row.ID, err)
	}
	if err := unmarshalOptional(row.Transport, &in.Transport); err != nil {
		return in, fmt.Errorf("入站 %d transport 损坏: %v", row.ID, err)
	}
	if err := unmarshalOptional(row.Security, &in.Security); err != nil {
		return in, fmt.Errorf("入站 %d security 损坏: %v", row.ID, err)
	}
	if strings.TrimSpace(row.Extra) != "" {
		in.Extra = json.RawMessage(row.Extra)
	}
	return in, nil
}

func unmarshalOptional(raw string, out interface{}) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), out)
}

func applyInboundToModel(in kernel.Inbound, row *model.NodeInbound) error {
	settings, err := json.Marshal(in.Settings)
	if err != nil {
		return err
	}
	transport, err := json.Marshal(in.Transport)
	if err != nil {
		return err
	}
	security, err := json.Marshal(in.Security)
	if err != nil {
		return err
	}
	row.Protocol = in.Protocol
	row.Listen = in.Listen
	row.Port = in.Port
	row.OutboundIPStrategy = in.OutboundIPStrategy
	row.ConnIdleSeconds = in.ConnIdleSeconds
	row.Settings = string(settings)
	row.Transport = string(transport)
	row.Security = string(security)
	row.Extra = string(in.Extra)
	return nil
}

// ---- kernel endpoints ----

func (h *Handler) kernelCapabilities(w http.ResponseWriter, r *http.Request) {
	// B11-004：与 kernelList/kernelInstall/kernelVersions/kernelControl 等
	// 兄弟端点统一 POST-only，非 POST 返回信封化的「请求失败」。
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	response.WriteJSON(w, response.OK(map[string]interface{}{
		"kernels":   kernel.Catalog(),
		"ssMethods": kernel.SSMethods(),
		"defaults": map[string]interface{}{
			"realitySni":  kernel.DefaultRealitySNI,
			"fingerprint": kernel.DefaultFingerprint,
			"ssMethod":    kernel.DefaultSSMethod,
			"flow":        kernel.FlowVision,
		},
		"splitCategories": kernel.SplitCategories,
	}))
}

func (h *Handler) kernelList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		NodeID int64 `json:"nodeId"`
	}
	_ = decodeJSON(r.Body, &req)

	views, err := h.kernelViews(req.NodeID)
	if err != nil {
		respondInternalError(w, "kernelList", err)
		return
	}
	response.WriteJSON(w, response.OK(views))
}

func (h *Handler) kernelViews(nodeID int64) ([]kernelView, error) {
	kernels, err := h.repo.ListNodeKernels(nodeID)
	if err != nil {
		return nil, err
	}
	inbounds, err := h.repo.ListNodeInbounds()
	if err != nil {
		return nil, err
	}
	counts := make(map[int64]int)
	for _, in := range inbounds {
		if in.CompanionOf > 0 {
			continue // 计量伴生入站是派生态，不计入内核入站数
		}
		counts[in.KernelID]++
	}
	nodes := make(map[int64]*model.Node)
	views := make([]kernelView, 0, len(kernels))
	for _, k := range kernels {
		// C-03: the companion kernel is managed, never user-facing.
		if kernel.CanonicalKernelType(k.KernelType) == kernel.TypeSingboxCompanion {
			continue
		}
		node, ok := nodes[k.NodeID]
		if !ok {
			node, _ = h.repo.GetNodeByID(k.NodeID)
			nodes[k.NodeID] = node
		}
		v := kernelView{
			ID:               k.ID,
			NodeID:           k.NodeID,
			KernelType:       k.KernelType,
			Version:          k.Version,
			InstalledVersion: k.InstalledVersion,
			Status:           k.Status,
			LastError:        k.LastError,
			Synced:           k.ConfigHash != "" && k.ConfigHash == k.AppliedHash,
			Enabled:          k.Enabled == 1,
			InboundCount:     counts[k.ID],
			UpdatedTime:      k.UpdatedTime,
		}
		// E-02: the note rides the main sing-box row (the companion
		// row itself is managed and never listed) so the node page
		// shows why the split is absent.
		if kernel.CanonicalKernelType(k.KernelType) == kernel.TypeSingbox {
			v.CompanionSplitNote = h.companionSplitPendingReason(k.NodeID)
		}
		if node != nil {
			v.NodeName = node.Name
			v.NodeOnline = node.Status == 1
		}
		views = append(views, v)
	}
	return views, nil
}

// kernelNodeEligible 的业务拒绝是定型文案，调用方可以 errors.Is 区分后
// 原样回执；仓储错误仍走通用出口（R2-F4-01）。
var (
	errKernelNodeNotFound = errors.New("节点不存在")
	errKernelNodeNftables = errors.New("nftables 模式节点不支持部署内核")
)

func (h *Handler) kernelNodeEligible(nodeID int64) (*model.Node, error) {
	node, err := h.repo.GetNodeByID(nodeID)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, errKernelNodeNotFound
	}
	if isNftablesForwardMode(node.ForwardMode) {
		return nil, errKernelNodeNftables
	}
	return node, nil
}

// kernelReleaseTagExists checks whether a concrete version exists as a
// GitHub release tag for an open-list kernel (H131-KVER): without it a
// typo version (9.9.9) sails through to the agent download, burns
// minutes on 404 retries, and leaves a deformed Version row behind.
// A 200 proves the version exists; a 404 from the DIRECT route proves
// it does not — a proxy 404 is not authoritative (a mirror may 404 on
// paths it does not serve), so it only counts as inconclusive and the
// iteration continues. Any other outcome (network error, 403 rate
// limit, 5xx, timeout) is inconclusive: the install proceeds and the
// rollback below covers a later download failure, preserving the
// offline-install tolerance (fixR2-A3). Returns (exists, determined);
// determined=false means "could not tell".
func kernelReleaseTagExists(info kernel.Info, version string) (exists bool, determined bool) {
	tagURL := fmt.Sprintf("%s/repos/%s/releases/tags/v%s", githubAPIBase, info.ReleaseRepo, version)
	_ = forEachGitHubRoute(kernelVersionPrecheckBudget, func(prefix string, attemptTimeout time.Duration) error {
		ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, prefixURL(prefix, tagURL), nil)
		if err != nil {
			return err
		}
		resp, err := githubVersionFetchClientWithTimeout(attemptTimeout).Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		switch resp.StatusCode {
		case http.StatusOK:
			exists, determined = true, true
			return nil
		case http.StatusNotFound:
			if prefix == "" {
				exists, determined = false, true
				return nil
			}
			return fmt.Errorf("release tag %s returned 404 via proxy %s", version, prefix)
		default:
			return fmt.Errorf("release tag %s returned HTTP %d", version, resp.StatusCode)
		}
	})
	return exists, determined
}

// rollbackKernelInstallRecord undoes the version write kernelInstall made
// before a failed apply (H131-KVER): an existing kernel goes back to its
// previous Version — the download never landed, so the running binary is
// still the old one, and leaving the new version in place would display
// a version the node does not run. A row created by this install is
// removed, but only when it still carries this install's version and
// creation stamp and has no inbounds attached, so a concurrent
// install's row or a row an admin has since built on is never deleted.
// Status/last_error are left as the failure trace. Returns false when
// nothing was rolled back.
//
// Callers must only invoke this for the error face (see
// rollbackKernelInstallAfterFailure): a pending apply never reached
// the agent, and its version write stands as the pending install
// intent (R2-F2-A2/A3 — registration precedes dispatch).
func rollbackKernelInstallRecord(r *repo.Repository, kernelID int64, existed bool, prevVersion string, prevUpdated, createdAt int64, version string) bool {
	if existed {
		if err := r.UpdateNodeKernelFields(kernelID, map[string]interface{}{
			"version": prevVersion, "updated_time": prevUpdated,
		}); err != nil {
			log.Printf("kernel install rollback: restore version for kernel_id=%d failed: %v", kernelID, err)
			return false
		}
		return true
	}
	cur, err := r.GetNodeKernel(kernelID)
	if err != nil || cur == nil {
		return false
	}
	if cur.CreatedTime != createdAt || cur.Version != version {
		return false
	}
	if inbounds, err := r.ListNodeInboundsByKernel(kernelID); err != nil || len(inbounds) > 0 {
		if err != nil {
			log.Printf("kernel install rollback: list inbounds for kernel_id=%d failed: %v", kernelID, err)
		}
		return false
	}
	if err := r.DeleteNodeKernel(kernelID); err != nil {
		log.Printf("kernel install rollback: delete kernel_id=%d failed: %v", kernelID, err)
		return false
	}
	return true
}

// rollbackKernelInstallAfterFailure decides whether a failed install's
// version write is rolled back (H131-KVER) and performs it. The
// deciding signal is the status applyKernel left behind:
//   - pending: dispatch never reached the agent (node offline) — the
//     version write stands as the pending install intent, to be
//     attempted again when the node comes back (R2-F2-A2/A3 pin this:
//     registration precedes dispatch; the row keeps the requested or
//     resolved version with the failure in last_error).
//   - anything else (error, or a concurrent poll's running): the agent
//     was reached and the attempt failed — download 404, config error,
//     timeout — so the version the panel claims is not the version the
//     node runs; roll it back.
func (h *Handler) rollbackKernelInstallAfterFailure(kernelID int64, existed bool, prevVersion string, prevUpdated, createdAt int64, version string) bool {
	cur, err := h.repo.GetNodeKernel(kernelID)
	if err != nil || cur == nil {
		return false
	}
	if cur.Status == kernelStatusPending {
		return false
	}
	return rollbackKernelInstallRecord(h.repo, kernelID, existed, prevVersion, prevUpdated, createdAt, version)
}

func (h *Handler) kernelInstall(w http.ResponseWriter, r *http.Request) {
	// R8-T-01: kernelApplyTimeout is 300s; arm the write relief first.
	extendWriteDeadline(w, kernelApplyWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		NodeID     int64  `json:"nodeId"`
		KernelType string `json:"kernelType"`
		Version    string `json:"version"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if _, err := h.kernelNodeEligible(req.NodeID); err != nil {
		if errors.Is(err, errKernelNodeNotFound) {
			response.WriteJSON(w, response.ErrDefault("节点不存在"))
			return
		}
		if errors.Is(err, errKernelNodeNftables) {
			response.WriteJSON(w, response.ErrDefault("nftables 模式节点不支持部署内核"))
			return
		}
		respondInternalError(w, "kernelInstall", err)
		return
	}
	info, ok := kernel.Lookup(req.KernelType)
	if !ok || !info.Implemented {
		response.WriteJSON(w, response.ErrDefault("暂不支持该内核"))
		return
	}
	version := normalizeKernelVersion(req.Version)
	if version == "" {
		response.WriteJSON(w, response.ErrDefault("版本号格式错误"))
		return
	}
	requestedLatest := version == "latest"
	// F5：sing-box（主核与伴生同族）拒绝字面 latest——fork 仓后续再发
	// tag 时 latest 会漂离已核实的 1.14.4-flmx1；fork 安装只接受具体
	// 版本，由调用方点名。Xray 等其余内核仍走下方的 latest 解析。
	if requestedLatest && kernel.EffectiveType(info.Type) == kernel.TypeSingbox {
		response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("sing-box 只支持安装具体版本（当前 fork 版 %s），不支持 latest，请选择具体版本", kernel.SingboxForkVersion)))
		return
	}
	// Kernels with a closed version list store the concrete version
	// so the binary directory and protocol checks agree with what is pinned.
	resolved, err := kernel.ResolveVersion(info, version)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if len(info.Versions) > 0 {
		version = resolved
	}

	now := time.Now().UnixMilli()
	k, err := h.repo.GetNodeKernelByType(req.NodeID, info.Type)
	if err != nil {
		respondInternalError(w, "kernelInstall", err)
		return
	}
	if k != nil {
		// F-021: 换版同样要过入站版本门（AnyTLS/ACME/ENC），不能把存量
		// 入站降到其最低版本之下再靠下发失败暴露。
		if err := h.checkKernelVersionGates(k.ID, info.Type, version); err != nil {
			var ge *kernelVersionGateError
			if errors.As(err, &ge) {
				response.WriteJSON(w, response.ErrDefault(ge.msg))
				return
			}
			respondInternalError(w, "kernelInstall", err)
			return
		}
	}
	// H131-KVER: open-list kernels accept any well-formed version, so a
	// typo (9.9.9) used to sail through to the agent download, burn
	// minutes on 404 retries, and leave a deformed Version row behind.
	// Precheck reachability before writing anything: a definitive
	// non-existence is refused fast with no record written; an
	// inconclusive check (offline panel, rate limit) lets the install
	// proceed and the rollback below covers a later download failure.
	// Closed-list kernels already reject unknown versions in
	// ResolveVersion above; "latest" resolves through the release
	// list in the block below.
	if len(info.Versions) == 0 && info.ReleaseRepo != "" && version != "latest" {
		if exists, determined := kernelReleaseTagExists(info, version); determined && !exists {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("版本 %s 不存在：%s 在 GitHub 上没有该版本的发布", version, info.Name)))
			return
		}
	}
	// R2-F2-A2: an open-list kernel must not be stored (or dispatched) as
	// the literal "latest": the agent keys its binary directory by the
	// version string and skips the download whenever a binary already
	// sits in bin/<type>/latest, so re-installing "latest" after an
	// upstream release silently kept the old binary while reporting
	// success. Resolve to the newest concrete release here — the same
	// list the version picker shows — and store the concrete version.
	// fixR2-A3: resolution failure must not refuse the install, though:
	// when the release list is unreachable (offline panel, GitHub rate
	// limit) the install falls back to storing the literal "latest" and
	// completes — the agent resolves it at download time through its
	// own mirror routes (the pre-A2 guarantee), and the apply entry
	// converges the stored row once the list is reachable again. The
	// fallback is named on the receipt via latestFallbackWarning.
	prevInstalled := ""
	if k != nil {
		prevInstalled = k.InstalledVersion
	}
	latestFellBack := false
	if version == "latest" {
		concrete, rerr := resolveLatestConcreteVersion(info)
		if rerr != nil {
			latestFellBack = true
			log.Printf("kernel install: resolve latest for node_id=%d type=%s failed, installing as literal \"latest\": %v", req.NodeID, info.Type, rerr)
		} else {
			version = concrete
		}
	}
	// H131-KVER: snapshot the pre-write state so a failed apply can
	// roll the version write back (see rollbackKernelInstallRecord).
	existed := k != nil
	prevVersion := ""
	var prevUpdated int64
	if existed {
		prevVersion = k.Version
		prevUpdated = k.UpdatedTime
	}
	if k == nil {
		k = &model.NodeKernel{
			NodeID:      req.NodeID,
			KernelType:  info.Type,
			Version:     version,
			Status:      kernelStatusPending,
			Enabled:     1,
			CreatedTime: now,
			UpdatedTime: now,
		}
		if err := h.repo.CreateNodeKernel(k); err != nil {
			respondInternalError(w, "kernelInstall", err)
			return
		}
		// R4X-023：并发双到达时 CreateNodeKernel 幂等回读的是胜者行，
		// 其版本可能与本次请求不同——按存量分支同一口径收敛到请求版本。
		// F2-01：存量分支只写 version＋updated_time，不动 enabled——
		// 旧形态无条件写 enabled:1，禁用的内核换版即静默启用，且
		// 安装最终失败也已启用（副作用与成败无关）。
		if k.Version != version {
			if err := h.repo.UpdateNodeKernelFields(k.ID, map[string]interface{}{
				"version": version, "updated_time": now,
			}); err != nil {
				respondInternalError(w, "kernelInstall", err)
				return
			}
			k.Version = version
		}
	} else if err := h.repo.UpdateNodeKernelFields(k.ID, map[string]interface{}{
		"version": version, "updated_time": now,
	}); err != nil {
		respondInternalError(w, "kernelInstall", err)
		return
	}

	if err := h.applyKernel(k.ID, true); err != nil {
		// F2-APPLY-01：确认待定不是失败——版本登记保留（pending 态
		// 不回滚，见 rollbackKernelInstallAfterFailure），回执只说
		// 「已发出、正在确认」。
		if errors.Is(err, errApplyConfirmPending) {
			response.WriteJSON(w, response.ErrDefault("内核已登记，下发已发出，正在确认生效状态"))
			return
		}
		// H131-KVER: when the agent was reached and the attempt failed,
		// roll the version write back instead of leaving a version the
		// node does not run. When dispatch never reached the agent the
		// version write stands as the pending install intent, and the
		// receipt keeps the established "registered, dispatch failed"
		// wording (R2-F2-A2/A3).
		if rolledBack := h.rollbackKernelInstallAfterFailure(k.ID, existed, prevVersion, prevUpdated, now, version); rolledBack {
			response.WriteJSON(w, response.ErrDefault("内核安装失败: "+err.Error()+"（版本已回滚）"))
			return
		}
		response.WriteJSON(w, response.ErrDefault("内核已登记，但下发失败: "+err.Error()))
		return
	}
	// Fork 伴生同步：主核换版成功后把伴生行同升到同一版本并立即下发，
	// 否则伴生实例继续跑旧二进制（旧队列 64）。伴生行经同一安装入口
	// （Lookup 的 singbox-companion 特例共用 sing-box 下载信息），
	// 下发失败只记警告、不推翻主核已成功的安装；伴生行版本登记保留
	// 为待安装意图，随下次对账重试。
	companionWarning := ""
	if info.Type == kernel.TypeSingbox {
		h.syncCompanionKernelVersion(req.NodeID, version)
		if comp, cerr := h.repo.GetNodeKernelByType(req.NodeID, kernel.TypeSingboxCompanion); cerr == nil && comp != nil {
			if cerr := h.applyKernel(comp.ID, true); cerr != nil {
				companionWarning = fmt.Sprintf("伴生内核同步到 %s 失败：%s（主内核已安装，伴生仍为旧版，将在下次对账时重试）", version, cerr.Error())
			}
		}
	}
	// R2-F2-A2: re-installing the version the node already runs must not
	// read as a silent success — the agent keeps the existing binary, so
	// name the fact on the envelope.
	warning := sameVersionInstallWarning(requestedLatest, prevInstalled, version)
	if companionWarning != "" {
		if warning != "" {
			warning += "；" + companionWarning
		} else {
			warning = companionWarning
		}
	}
	if w := latestFallbackWarning(latestFellBack); w != "" {
		if warning != "" {
			warning += "；" + w
		} else {
			warning = w
		}
	}
	// R8-005: an explicitly old Xray (< 26.5.9) installs fine but has
	// no built-in private-range block; name that on the success
	// envelope instead of letting it stay silent.
	if w := xrayPrivateBlockWarning(info.Type, version); w != "" {
		if warning != "" {
			warning += "；" + w
		} else {
			warning = w
		}
	}
	// 发现 1：显式装上游构建（回滚）同样点名在回执上，见
	// singboxUpstreamInstallWarning。
	if w := singboxUpstreamInstallWarning(info.Type, version); w != "" {
		if warning != "" {
			warning += "；" + w
		} else {
			warning = w
		}
	}
	if warning != "" {
		response.WriteJSON(w, response.OKWithWarning(nil, warning))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

// kernelVersionEntryPattern 是 normalizeKernelVersion 的入口文法。
// R2-P2-11:边缘按规范文法（internal/kernel/capabilities.go 的
// kernelVersionPattern 规范表述）对齐——首字符须为字母数字、总长
// ≤40、另禁 ".."（旧实现只查字符集与总长,"-"/"."/"1..2" 这类形态
// 能过本入口、要到漏斗本体才被拒,同对象两入口结论不一致)。
// 点名保留差异:本入口只收小写——受支持内核的发布 tag 均为小写,
// 用户输入面在此 fail-closed 拒绝大写不丢任何合法版本;漏斗本体
// 允许大小写的规范不变。
var kernelVersionEntryPattern = regexp.MustCompile(`^[0-9a-z][0-9a-z.-]{0,39}$`)

// normalizeKernelVersion accepts "latest" or a dotted version, returning ""
// for anything that could inject into a download URL.
func normalizeKernelVersion(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" || strings.EqualFold(v, "latest") {
		return "latest"
	}
	if !kernelVersionEntryPattern.MatchString(v) || strings.Contains(v, "..") {
		return ""
	}
	return v
}

func (h *Handler) kernelControl(w http.ResponseWriter, r *http.Request) {
	// B11-001 (R2-K2): start/stop (and the remove failure-repush)
	// await applyKernelWith, whose timeout conditionally escalates
	// to kernelApplyTimeout (300s) when the kernel has no installed
	// version; arm the same write relief as kernelInstall first.
	extendWriteDeadline(w, kernelApplyWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		ID     int64  `json:"id"`
		Action string `json:"action"`
		Force  bool   `json:"force"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	k, err := h.repo.GetNodeKernel(req.ID)
	if err != nil || k == nil {
		response.WriteJSON(w, response.ErrDefault("内核不存在"))
		return
	}
	now := time.Now().UnixMilli()

	switch req.Action {
	case "start", "stop":
		enabled := 0
		if req.Action == "start" {
			enabled = 1
		}
		if err := h.repo.UpdateNodeKernelFields(k.ID, map[string]interface{}{"enabled": enabled, "updated_time": now}); err != nil {
			respondInternalError(w, "kernelControl", err)
			return
		}
		if err := h.applyKernel(k.ID, false); err != nil {
			// F2-APPLY-01：确认待定不是失败——启停状态已更新、下发已
			// 发出，回执只说「正在确认」，不前缀「下发失败」。
			if errors.Is(err, errApplyConfirmPending) {
				response.WriteJSON(w, response.ErrDefault("已更新启停状态，下发已发出，正在确认生效状态"))
				return
			}
			// B11-002：enabled 已先行落库（节点重连时按新状态对账生效），
			// 与 kernelInstall 的部分成功口径对齐——回执必须明示状态已
			// 更新、仅下发失败，不得只回原始错误让用户误判启停未生效。
			response.WriteJSON(w, response.ErrDefault("已更新启停状态，但下发失败: "+err.Error()))
			return
		}
	case "restart":
		// R10-05（I-F3-04）：已停用内核不得走 restart——agent 侧
		// Control 对 restart 一律置 Enabled=true 并拉起进程，面板
		// DB 仍 enabled=0，两侧状态就此分叉（靠下次 apply 才收敛、
		// 状态回读自相矛盾）。停用态须先 start（落库 enabled=1），
		// restart 在此点名拒绝；守卫落面板面，agent 语义不动。
		if k.Enabled == 0 {
			response.WriteJSON(w, response.ErrDefault("内核已停用，请先启动"))
			return
		}
		// NEW-V1-01：restart 同样触发 agent 侧 nft 同步，旧版 agent
		// 会用明文表名重建共享表；与 KernelApply 同门，版本过旧
		// 时拒绝并点名升级（remove/删节点清理路径必须放行，不设
		// 此门）。
		// R2-F4-01：门禁错误含库内节点版本，不得原文回客户端；
		// 详情记服务端日志，回执只给通用文案（须含“版本过旧”，
		// 与 nft_isolation_gate_test.go 的 restart 断言一致）。
		if err := h.requireNftIsolatedAgent(k.NodeID); err != nil {
			log.Printf("kernelControl restart: node_id=%d blocked by nft version gate: %v", k.NodeID, err)
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("agent 版本过旧，需 %s 或更高；请先升级 agent，升级后重连时将自动重推", nftIsolatedAgentMinVersion)))
			return
		}
		unlock := lockKernelApply(k.ID)
		_, err := h.sendNodeCommandWithTimeout(k.NodeID, "KernelControl", map[string]interface{}{
			"type": k.KernelType, "action": "restart",
		}, kernelCommandTimeout, false, false)
		unlock()
		if err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		h.refreshNodeKernelStatus(k.NodeID)
	case "remove":
		// X-P2-2（V-03）：删前链式引用守卫——该内核入站被跨内核/跨节点
		// 入站用作出站时默认拒绝并列明引用方；Force 放行但回传清单。
		// R3X-015：守卫查询失败 fail-closed 中止卸载（Force 只放行已知
		// 清单，不放行查询失败），不得当作无引用继续。
		brokenReferrers, guardMsg, guardErr := h.kernelChainGuard(k.ID)
		if guardErr != nil {
			respondInternalError(w, "kernelControl", guardErr)
			return
		}
		if guardMsg != "" && !req.Force {
			response.WriteJSON(w, response.R{
				Code: -1,
				Msg:  guardMsg,
				TS:   time.Now().UnixMilli(),
				Data: map[string]interface{}{
					"brokenReferrers":     brokenReferrers,
					"brokenReferrerCount": len(brokenReferrers),
				},
			})
			return
		}
		unlock := lockKernelApply(k.ID)
		_, dispatchErr := h.sendNodeCommandWithTimeout(k.NodeID, "KernelControl", map[string]interface{}{
			"type": k.KernelType, "action": "remove",
		}, kernelCommandTimeout, false, true)
		if dispatchErr != nil && !req.Force {
			unlock()
			response.WriteJSON(w, response.ErrDefault("节点卸载失败: "+dispatchErr.Error()+"（可强制删除面板记录）"))
			return
		}
		// R9-08（I-F2-03）：落库前按最终引用集复检——守卫与删除之间
		// 隔着上面的卸载下发（超时 20s），期间新保存的引用不在守卫
		// 清单内。非 Force：中止删除并点名新引用（下发已移除 agent
		// 侧内核，解锁后按 R5X-004(f) 同口径补一次 applyKernel 按
		// 现行 DB 复原）；Force：新引用并入回执清单后照删。复检查
		// 询失败 fail-closed，同守卫口径（复原补偿同上）。
		recheckNames, recheckMsg, recheckErr := h.kernelChainGuard(k.ID)
		if recheckErr != nil {
			unlock()
			if aerr := h.applyKernel(k.ID, false); aerr != nil {
				log.Printf("kernel remove: repush after recheck failure kernel_id=%d err=%v", k.ID, aerr)
			}
			respondInternalError(w, "kernelControl", recheckErr)
			return
		}
		if recheckMsg != "" {
			if !req.Force {
				unlock()
				if aerr := h.applyKernel(k.ID, false); aerr != nil {
					log.Printf("kernel remove: repush after recheck rejection kernel_id=%d err=%v", k.ID, aerr)
				}
				response.WriteJSON(w, response.R{
					Code: -1,
					Msg:  recheckMsg,
					TS:   time.Now().UnixMilli(),
					Data: map[string]interface{}{
						"brokenReferrers":     recheckNames,
						"brokenReferrerCount": len(recheckNames),
					},
				})
				return
			}
			brokenReferrers = mergeReferrerNames(brokenReferrers, recheckNames)
		}
		kernelInboundRows, listErr := h.repo.ListNodeInboundsByKernel(k.ID)
		if listErr == nil {
			for _, row := range kernelInboundRows {
				h.removeInboundRelays(row.ID)
			}
		}
		err = h.repo.DeleteNodeKernel(k.ID)
		unlock()
		if err != nil {
			// R5X-004(f)：removeInboundRelays 已逐入站删掉 relay 与
			// 转发，内核/入站本体尚存（DeleteNodeKernel 为单事务、
			// 失败只回滚其自身级联）——解锁后对本内核补一次
			// applyKernel，按现行 DB 重算各入站 allow（applyKernel
			// 自带同内核锁，必须在 unlock 之后调用）。列表查询失败
			// 时未删任何 relay，跳过补刷。
			if listErr == nil && len(kernelInboundRows) > 0 {
				if aerr := h.applyKernel(k.ID, false); aerr != nil {
					log.Printf("kernel remove: whitelist repush after failed kernel delete kernel_id=%d err=%v", k.ID, aerr)
				}
			}
			respondInternalError(w, "kernelControl", err)
			return
		}
		// C-03: deleting the main sing-box kernel orphans the companion
		// instance (its origins are gone with the cascade, so it would
		// render an empty config forever). Remove the companion kernel
		// with it: agent-side remove + row delete (which cascades the
		// companion inbounds). Best-effort — a failed agent remove is
		// covered by the orphan-recheck mark below.
		h.removeCompanionKernel(k)
		// FIX-CASCADE（E1-09 子形态 b）：走到这里且 dispatchErr 非
		// 空，只可能是 Force 强删（下发未确认、agent 侧内核可能
		// 仍在运行）。登记孤儿重查标记：周期轮询把该节点并入轮询
		// 集、以未知类型分支持续下发移除，agent 确认空集后清除
		// ——否则节点最后一个内核被强删后该节点退出轮询集，孤儿
		// 只能等下次重连才收敛。下发已确认（dispatchErr==nil）时
		// 不登记。登记失败只记日志：删除已落库，孤儿仍有重连
		// reconcile 兜底，与修复前等价、不回滚删除。
		if dispatchErr != nil {
			if merr := h.repo.MarkKernelOrphanRecheck(k.NodeID, k.KernelType, time.Now().UnixMilli()); merr != nil {
				log.Printf("kernel remove: orphan recheck mark failed node_id=%d kernel=%s err=%v", k.NodeID, k.KernelType, merr)
			}
		}
		if len(brokenReferrers) > 0 {
			response.WriteJSON(w, response.OK(map[string]interface{}{
				"brokenReferrers":     brokenReferrers,
				"brokenReferrerCount": len(brokenReferrers),
			}))
			return
		}
	default:
		response.WriteJSON(w, response.ErrDefault("未知操作"))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

// kernelLogsMissingReply reports whether a KernelStatus reply
// says the requested kernel type has no instance on the node
// (F2-23). Extracted as a pure predicate so the decision is
// unit-testable without a live agent; the handler only renders it.
func kernelLogsMissingReply(status agentKernelStatus, kernelType string) bool {
	return status.MissingLogsFor != "" && status.MissingLogsFor == kernelType
}

func (h *Handler) kernelLogs(w http.ResponseWriter, r *http.Request) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	k, err := h.repo.GetNodeKernel(id)
	if err != nil || k == nil {
		response.WriteJSON(w, response.ErrDefault("内核不存在"))
		return
	}
	res, err := h.sendNodeCommandWithTimeout(k.NodeID, "KernelStatus", map[string]interface{}{
		"logs": k.KernelType,
	}, kernelCommandTimeout, false, false)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	var status agentKernelStatus
	if err := remarshal(res.Data, &status); err != nil {
		response.WriteJSON(w, response.ErrDefault("节点返回数据格式错误"))
		return
	}
	// F2-23: the agent says this kernel type has no instance on
	// the node. Say so explicitly instead of synthesizing an
	// empty code-0 state, which is indistinguishable from
	// "kernel present but no logs yet".
	if kernelLogsMissingReply(status, k.KernelType) {
		response.WriteJSON(w, response.ErrDefault("节点上未安装 "+k.KernelType+" 内核，无法查询日志"))
		return
	}
	for _, ks := range status.Kernels {
		if ks.Type == k.KernelType {
			response.WriteJSON(w, response.OK(ks))
			return
		}
	}
	response.WriteJSON(w, response.OK(agentKernelState{Type: k.KernelType}))
}

// ---- inbound endpoints ----

func (h *Handler) inboundList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		respondInternalError(w, "inboundList", err)
		return
	}
	kernels, err := h.repo.ListNodeKernels(0)
	if err != nil {
		respondInternalError(w, "inboundList", err)
		return
	}
	relays, err := h.repo.ListInboundRelays(0)
	if err != nil {
		respondInternalError(w, "inboundList", err)
		return
	}
	// IRL-7: count only relays that can actually carry traffic — the
	// backing forward must exist and be enabled (status 1), the same
	// predicate relayViews uses. Dead relays are counted separately so
	// the UI can flag them instead of inflating the headline count.
	relayCounts := make(map[int64]int)
	relayInvalidCounts := make(map[int64]int)
	for _, rel := range relays {
		if rel.ForwardExists && rel.ForwardStatus == 1 {
			relayCounts[rel.InboundID]++
		} else {
			relayInvalidCounts[rel.InboundID]++
		}
	}
	kernelByID := make(map[int64]model.NodeKernel, len(kernels))
	for _, k := range kernels {
		kernelByID[k.ID] = k
	}
	nodes := make(map[int64]*model.Node)
	sourceNames := map[int64]string{}
	if sources, err := h.repo.ListRemoteSources(); err == nil {
		for _, s := range sources {
			sourceNames[s.ID] = s.Name
		}
	}
	names := make(map[int64]string, len(rows))
	for _, row := range rows {
		names[row.ID] = row.Name
	}
	// Companion inbounds are system-managed derived rows: they never
	// appear in the list; their origins report meteringActive instead.
	// Same gate as subscription pulls (F4/B-03): the companion must
	// exist, be enabled, and its kernel's current config must be
	// acknowledged by the agent — a materialized-but-unapplied
	// companion is not metering anything yet.
	meteringActive := make(map[int64]bool)
	appliedByKernel := make(map[int64]bool)
	checkedKernel := make(map[int64]bool)
	for _, row := range rows {
		if row.CompanionOf <= 0 || row.Enabled != 1 {
			continue
		}
		if !checkedKernel[row.KernelID] {
			checkedKernel[row.KernelID] = true
			k := kernelByID[row.KernelID]
			appliedByKernel[row.KernelID] = h.kernelConfigApplied(&k)
		}
		if appliedByKernel[row.KernelID] {
			meteringActive[row.CompanionOf] = true
		}
	}
	views := make([]inboundView, 0, len(rows))
	for _, row := range rows {
		if row.CompanionOf > 0 {
			continue
		}
		in, err := inboundFromModel(row)
		if err != nil {
			log.Printf("kernel: %v", err)
		}
		node, ok := nodes[row.NodeID]
		if !ok {
			node, _ = h.repo.GetNodeByID(row.NodeID)
			nodes[row.NodeID] = node
		}
		k := kernelByID[row.KernelID]
		v := inboundView{
			ID:                 row.ID,
			KernelID:           row.KernelID,
			KernelType:         k.KernelType,
			KernelStatus:       k.Status,
			External:           isExternalInbound(&row),
			NodeID:             row.NodeID,
			Name:               row.Name,
			Protocol:           row.Protocol,
			Listen:             row.Listen,
			Port:               row.Port,
			Network:            in.Network(),
			Settings:           in.Settings,
			Transport:          in.Transport,
			Security:           kernelSecurityView{Security: in.Security, ACMEDNSSet: in.Security.ACMEDNSKey != ""},
			Extra:              row.Extra,
			ShareAddressMode:   inboundShareMode(&row),
			ShareHost:          row.ShareHost,
			Remark:             row.Remark,
			RelayOnly:          row.RelayOnly == 1,
			RelayCount:         relayCounts[row.ID],
			RelayInvalidCount:  relayInvalidCounts[row.ID],
			AcceptProxy:        row.AcceptProxyProtocol == 1,
			Whitelist:          row.RelayWhitelist == 1,
			WhitelistExtra:     row.WhitelistExtra,
			Enabled:            row.Enabled == 1,
			UploadBytes:        row.UploadBytes,
			DownloadBytes:      row.DownloadBytes,
			CreatedTime:        row.CreatedTime,
			UpdatedTime:        row.UpdatedTime,
			SourceID:           row.SourceID,
			SourceName:         sourceNames[row.SourceID],
			OutboundID:         row.OutboundID,
			SplitRules:         rowSplitRules(row),
			OutboundIPStrategy: row.OutboundIPStrategy,
			ConnIdleSeconds:    row.ConnIdleSeconds,
			MeteringEnabled:    row.MeteringEnabled == 1,
			MeteringActive:     meteringActive[row.ID],
		}
		v.ShareAddress = inboundShareAddress(&row, node)
		if v.SplitRules == nil {
			v.SplitRules = []kernel.SplitRule{}
		}
		if row.OutboundID != 0 {
			name, ok := names[row.OutboundID]
			v.OutboundName, v.OutboundMissing = name, !ok
		}
		if node != nil {
			v.NodeName = node.Name
			v.NodeServerIP = node.ServerIP
		}
		if p := importedProxy(&row); p != nil {
			v.ImportedSummary = kernel.DescribeProxy(p)
			v.ImportedParams = kernel.ProxyParams(p)
			v.Extra = ""
		}
		if v.External {
			if p, ok := h.externalProbes.get(row.ID); ok {
				v.Probe = &p
			}
		}
		views = append(views, v)
	}
	response.WriteJSON(w, response.OK(views))
}

// decodeInboundRequest decodes an inbound save payload like decodeJSON
// (same 10MB limit and DisallowUnknownFields strictness) and additionally
// reports which top-level JSON keys were present. Presence cannot be
// recovered from the decoded struct alone: an omitted object and an object
// sent with every field cleared both decode to the zero value, but the
// update path must treat them differently (GATE-F2).
func decodeInboundRequest(body io.ReadCloser, req *inboundRequest) (map[string]bool, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(nil, body, 10<<20))
	_ = body.Close()
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(req); err != nil {
		return nil, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(top))
	for k := range top {
		present[k] = true
	}
	return present, nil
}

func (h *Handler) inboundCreate(w http.ResponseWriter, r *http.Request) {
	h.inboundSave(w, r, false)
}

func (h *Handler) inboundUpdate(w http.ResponseWriter, r *http.Request) {
	h.inboundSave(w, r, true)
}

func (h *Handler) inboundSave(w http.ResponseWriter, r *http.Request, update bool) {
	// R8-T-01: a save against a kernel with no installed version
	// escalates to a full kernel apply (kernelApplyTimeout, 300s).
	extendWriteDeadline(w, kernelApplyWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req inboundRequest
	present, err := decodeInboundRequest(r.Body, &req)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	// X-P3-27（L-11-2）：新建分支接入 Idempotency-Key 机制。
	if !update {
		var finish func()
		var replayed bool
		w, finish, replayed = h.idemBegin(w, r, idemUserID(r), "inbound:create")
		defer finish()
		if replayed {
			return
		}
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len([]rune(req.Name)) > 100 {
		response.WriteJSON(w, response.ErrDefault("名称不能为空且不超过 100 字"))
		return
	}
	req.ShareHost = normalizeShareHost(req.ShareHost)
	if len(req.ShareHost) > 255 {
		response.WriteJSON(w, response.ErrDefault("分享地址过长"))
		return
	}

	now := time.Now().UnixMilli()
	var row model.NodeInbound
	var k *model.NodeKernel
	// oldIn is the stored inbound an update is based on, captured for the
	// presence backfill after assembly (R2X-030, below).
	var oldIn *kernel.Inbound
	// R2X-009：更新前快照 Port/Listen，保存后据此判断是否需要对名下
	// Managed relay 重算物化 RemoteAddr。
	var prevPort int
	var prevListen string
	// R2-F2-C-L01: pre-edit share address snapshot; the target-side
	// referrer policy check below keys on these (plus Port/Listen)
	// actually changing.
	var prevShareHost string
	var prevShareMode string
	// prevMetering/prevMeteringBlock snapshot the stored metering
	// switch and its shape eligibility before this save mutates the
	// row (F1 gate below).
	var prevMetering int
	var prevMeteringBlock string
	if update {
		existing, err := h.repo.GetNodeInbound(req.ID)
		if err != nil || existing == nil {
			response.WriteJSON(w, response.ErrDefault("节点服务不存在"))
			return
		}
		row = *existing
		prevPort, prevListen = existing.Port, existing.Listen
		prevShareHost, prevShareMode = existing.ShareHost, inboundShareMode(existing)
		if row.CompanionOf > 0 {
			response.WriteJSON(w, response.ErrDefault("计量伴生入站由系统管理，不能编辑"))
			return
		}
		if isExternalInbound(&row) {
			response.WriteJSON(w, response.ErrDefault("导入的节点请在“编辑”里修改地址和密码"))
			return
		}
		k, err = h.repo.GetNodeKernel(row.KernelID)
		if err != nil || k == nil {
			response.WriteJSON(w, response.ErrDefault("内核不存在"))
			return
		}
		// Keep secrets the client did not resend (the list hides PEM).
		old, _ := inboundFromModel(row)
		oldIn = &old
		// R4X-018: 其余入站键 presence 化（与下方 4 键整对象回填同口径）——
		// 部分更新未携带的键回填存量值，显式在场（含零值/空串/空数组）才
		// 是清除：(a) shareHost/remark（shareHost 的归一在 decode 后已按
		// 请求值完成，回填值是存量规范值可直接沿用）；(b) 三 bool——回填
		// 必须发生在下方行赋值段「acceptProxyProtocol→RelayOnly」联动判
		// 定之前，使 PP 省略但存量开时联动照常生效（本块在本函数联动
		// 之前，天然满足）；(c) whitelistExtra 与 relayWhitelist 成对——
		// 省 whitelistExtra 时回填存量规范串（ParseAllowList 可重解析），
		// extra 显式而开关省略时以存量开关决定 ParseAllowList 的合并；
		// (d) 路由对 outboundId+splitRules 同批独立判定（逐键回填，显式
		// [] 与 0 是合法清空）。inboundSplitSave 是全量替换入口，不在此列。
		if !present["shareHost"] {
			req.ShareHost = row.ShareHost
		}
		if !present["remark"] {
			req.Remark = row.Remark
		}
		if !present["relayOnly"] {
			req.RelayOnly = row.RelayOnly == 1
		}
		if !present["acceptProxyProtocol"] {
			req.AcceptProxyProtocol = row.AcceptProxyProtocol == 1
		}
		if !present["relayWhitelist"] {
			req.RelayWhitelist = row.RelayWhitelist == 1
		}
		if !present["whitelistExtra"] {
			req.WhitelistExtra = row.WhitelistExtra
		}
		if !present["outboundId"] {
			req.OutboundID = row.OutboundID
		}
		if !present["splitRules"] {
			req.SplitRules = rowSplitRules(row)
		}
		prevMetering = row.MeteringEnabled
		if prevMetering == 1 {
			prevMeteringBlock = ssMeteringShapeBlock(&row)
		}
		// GATE-F2（R3X-012 整对象限定）：载荷整对象省略 settings /
		// transport / security / extra 时整对象保留旧值。结构体解码无法
		// 区分「对象缺席」与「对象在场但子字段清空」，故 presence 取自
		// 原始 JSON 顶层键（decodeInboundRequest）。批 7 的字段级回填只
		// 覆盖对象在场情形；整对象缺席时旧实现把零值交给 Normalize 取
		// 默认（security.Type=""→目录首选安全层），ShadowTLS 被静默剥
		// 离为 none、口令丢失（发版门项 8）。对象在场时本块不触发，显式
		// 语义不变：security 在场且 type 显式传 "none" 仍是合法关闭路
		// 径、extra 在场传空串仍是合法清空。创建路径不进入本块。
		if !present["settings"] {
			req.Settings = old.Settings
		}
		if !present["transport"] {
			req.Transport = old.Transport
		}
		if !present["security"] {
			req.Security = old.Security
		}
		if !present["extra"] {
			req.Extra = string(old.Extra)
		}
		if req.Security.Type == old.Security.Type && req.Security.CertMode == old.Security.CertMode &&
			req.Security.CertPEM == "" && req.Security.KeyPEM == "" {
			req.Security.CertPEM, req.Security.KeyPEM = old.Security.CertPEM, old.Security.KeyPEM
		}
		if req.Security.CertMode == kernel.CertModeACME && req.Security.ACMEDNS == old.Security.ACMEDNS &&
			req.Security.ACMEDNSKey == "" && req.Security.ACMEDNSSecret == "" {
			req.Security.ACMEDNSKey, req.Security.ACMEDNSSecret = old.Security.ACMEDNSKey, old.Security.ACMEDNSSecret
		}
	} else {
		if _, err := h.kernelNodeEligible(req.NodeID); err != nil {
			if errors.Is(err, errKernelNodeNotFound) {
				response.WriteJSON(w, response.ErrDefault("节点不存在"))
				return
			}
			if errors.Is(err, errKernelNodeNftables) {
				response.WriteJSON(w, response.ErrDefault("nftables 模式节点不支持部署内核"))
				return
			}
			respondInternalError(w, "inboundSave", err)
			return
		}
		var err error
		k, err = h.repo.GetNodeKernelByType(req.NodeID, kernel.CanonicalKernelType(req.KernelType))
		if err != nil {
			respondInternalError(w, "inboundSave", err)
			return
		}
		if k == nil {
			response.WriteJSON(w, response.ErrDefault("请先在该节点安装内核"))
			return
		}
		row = model.NodeInbound{KernelID: k.ID, NodeID: k.NodeID, CreatedTime: now, Enabled: 1}
	}

	in := kernel.Inbound{
		ID:        row.ID,
		Protocol:  req.Protocol,
		Listen:    req.Listen,
		Port:      req.Port,
		Settings:  req.Settings,
		Transport: req.Transport,
		Security:  req.Security,
		Extra:     json.RawMessage(strings.TrimSpace(req.Extra)),

		AcceptProxyProtocol: req.AcceptProxyProtocol,
		OutboundIPStrategy:  row.OutboundIPStrategy,
		ConnIdleSeconds:     row.ConnIdleSeconds,
	}
	if req.OutboundIPStrategy != nil {
		in.OutboundIPStrategy = *req.OutboundIPStrategy
	}
	// ConnIdleSeconds is a pointer field: omitted on update keeps the
	// stored timeout, an explicit value (including 0 = clear) wins.
	// Create starts at 0 (unset) and takes the request value when present.
	if req.ConnIdleSeconds != nil {
		in.ConnIdleSeconds = *req.ConnIdleSeconds
	}
	// X-P3-4（D-R1-01）：更新时省略 port 回退已存行端口——与密钥/策略/
	// Enabled 的 presence 保留同口径；此前唯 port 例外，只改名的更新
	// 会被静默改派随机端口。创建路径不变（仍随机分配）。
	if update && req.Port == 0 {
		in.Port = row.Port
	}
	// R2X-030：更新时凭据类子字段留空一律回填旧值（verdicts §2.9 全集），
	// 与上方 PEM/ACME 保留同口径——Normalize 的「留空自动生成」只应作用
	// 于创建；更新留空无合法的「意图清空」语义，有意轮换走显式新值或
	// /inbound/regenerate。创建路径不进入本块，行为不变。
	if update && oldIn != nil {
		if in.Settings.EncryptionEnabled() && in.Settings.EncryptionSecret == "" {
			in.Settings.EncryptionSecret = oldIn.Settings.EncryptionSecret
		}
		if in.Security.RealityPrivateKey == "" {
			in.Security.RealityPrivateKey = oldIn.Security.RealityPrivateKey
		}
		if len(in.Security.RealityShortIDs) == 0 {
			in.Security.RealityShortIDs = append([]string(nil), oldIn.Security.RealityShortIDs...)
		}
		if strings.TrimSpace(in.Listen) == "" {
			in.Listen = oldIn.Listen
		}
		if in.Settings.UUID == "" {
			in.Settings.UUID = oldIn.Settings.UUID
		}
		if in.Settings.Password == "" {
			in.Settings.Password = oldIn.Settings.Password
		}
		// R3X-012：R2X-030 全集复查漏项——回填条件按入站形态判定（部分
		// 更新时 req 形态即旧形态），避免给无关协议/传输塞值。
		if in.Protocol == kernel.ProtocolShadowsocks && in.Settings.Method == "" {
			in.Settings.Method = oldIn.Settings.Method
		}
		if in.Protocol == kernel.ProtocolNaive && in.Settings.Username == "" {
			in.Settings.Username = oldIn.Settings.Username
		}
		if in.Security.Type == kernel.SecurityShadowTLS && in.Security.ShadowTLSPassword == "" {
			in.Security.ShadowTLSPassword = oldIn.Security.ShadowTLSPassword
		}
		if (in.Transport.Type == kernel.TransportWS || in.Transport.Type == kernel.TransportXHTTP) && in.Transport.Path == "" {
			in.Transport.Path = oldIn.Transport.Path
		}
		if in.Transport.Type == kernel.TransportGRPC && in.Transport.ServiceName == "" {
			in.Transport.ServiceName = oldIn.Transport.ServiceName
		}
	}
	allowExtra, err := kernel.ParseAllowList(req.WhitelistExtra)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// F2-PORT-01：live socket 探针在锁前跑（R5R3-001：锁内禁网络
	// 调用）。in.Port 锁前已定型（Normalize 不改端口；更新省略
	// port 时已回填存量值）；Port==0 是自动选口，端口在下方
	// pick→probe→锁内复检循环里定（见 P0-2）。更新时减去旧入站
	// 自身的段，避免把自己的监听器误判为占用。探针失败 fail-open
	// （只记日志），DB 结论不变。
	if in.Port != 0 {
		spans := inboundSpans(in)
		if oldIn != nil {
			spans = subtractPortSpans(spans, inboundSpans(*oldIn))
		}
		if msg := h.checkLivePortsOccupied([]livePortProbeTarget{{NodeID: row.NodeID, Spans: spans}}); msg != "" {
			response.WriteJSON(w, response.ErrDefault(msg))
			return
		}
	}
	// AUDIT-R3 (F2X-001): per-node 端口分配锁必须覆盖"选端口→冲突检查→入库"
	// 整个临界区。R5-B4 把锁放在 CreateNodeInbound 内部，检查在锁外，
	// 并发下两请求可挑中同一随机端口，一个撞唯一索引报裸 DB 错误。
	// R5R3-001: 锁在 DB 写入后立即显式释放，不用 defer——defer 会穿越
	// 下游 applyKernel 等网络调用（最长 5min 超时），违反"锁内禁止网络调用"。
	// 注意：临界区内每个提前返回前都必须先释放（Release 即 Unlock，不可双释放）。
	// F2-PORT-01 P0-2：自动选口（Port==0，创建路径；更新省略 port 已在
	// 上方回填存量值）走 pick→probe→锁内复检循环，与转发创建的
	// createForwardWithPortRetry 同口径。选口本身是锁外 DB 读（与转发
	// 选口同形）；探针是网络调用，只能在锁外跑（R5R3-001），命中即把
	// 该口记入 tried 换口重试（上限 forwardPortRetryAttempts 次）；
	// 探针失败 fail-open（probeForwardEntryPortLive 回 ""），沿用 DB
	// 结论。锁内 inboundPortConflict 复检仍是最终权威：探活到落库
	// 之间的窗口被并发占用的口在此拦截，自动口同样换口重试（而非
	// 直接报错）；显式口保持原口径（一次复检、命中即拒）。循环以
	// 锁内复检通过 break，持锁进入下方入库段；耗尽回末次碰撞文案。
	autoPick := in.Port == 0
	triedInboundPorts := make(map[int]bool)
	var lastInboundPortErr error
	var prevTargets []int64
	var releasePortAlloc func()
	pickedClean := false
	for attempt := 0; attempt < forwardPortRetryAttempts; attempt++ {
		if autoPick {
			port, pickErr := h.pickFreeInboundPortExcluding(row.NodeID, row.ID, triedInboundPorts)
			if pickErr != nil {
				if errors.Is(pickErr, errNoFreeInboundPort) {
					response.WriteJSON(w, response.ErrDefault("未找到可用端口，请手动指定"))
					return
				}
				respondInternalError(w, "inboundSave", pickErr)
				return
			}
			if msg := h.probeForwardEntryPortLive([]int64{row.NodeID}, port); msg != "" {
				lastInboundPortErr = errors.New(msg)
				triedInboundPorts[port] = true
				continue
			}
			in.Port = port
		}
		portAllocMu := repo.AcquirePortAllocLock(row.NodeID)
		releasePortAlloc = func() { repo.ReleasePortAllocLock(portAllocMu) }
		if err := kernel.Normalize(k.KernelType, &in); err != nil {
			releasePortAlloc()
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		if msg := kernelVersionGate(k.KernelType, k.Version, &in); msg != "" {
			releasePortAlloc()
			response.WriteJSON(w, response.ErrDefault(msg))
			return
		}
		// F2-N2-01：链式/分流目标的字面 IP 族冲突校验读 row.OutboundIPStrategy
		// （validateChainVersion），而本单新策略此时只在 in 上——applyInboundToModel
		// 在校验之后才落 row，同单收紧会被存量旧策略放行、落库后渲染期把链式
		// 出站判 unreachable 静默丢流量（同单放宽则被旧策略误拒）。校验前先把
		// 经 Normalize 的本单策略同步进 row，与 inboundSplitSave（先写 row
		// 再校验）口径对齐。更新未携带策略时 in 即存量值，同步为恒等、无副作用。
		row.OutboundIPStrategy = in.OutboundIPStrategy
		prevTargets = outboundTargets(row)
		if err := h.setInboundRouting(&row, k.KernelType, k.Version, req.OutboundID, req.SplitRules); err != nil {
			releasePortAlloc()
			// R2-A01：链式校验定型失败原样回（TestFixA／TestR2B6），仓储故障才泛化。
			respondChainValidationError(w, "inboundSave", err)
			return
		}
		if msg, err := h.inboundPortConflict(row.NodeID, in, row.ID); err != nil {
			releasePortAlloc()
			// R-068：损坏行无法恢复时的 fail-closed 拒绝须点名该行，
			// 不走吞文案的通用 Internal error。
			var blocked *inboundConflictBlockedError
			if errors.As(err, &blocked) {
				response.WriteJSON(w, response.ErrDefault(blocked.msg))
				return
			}
			respondInternalError(w, "inboundSave", err)
			return
		} else if msg != "" {
			releasePortAlloc()
			if autoPick {
				lastInboundPortErr = errors.New(msg)
				triedInboundPorts[in.Port] = true
				continue
			}
			response.WriteJSON(w, response.ErrDefault(msg))
			return
		}
		pickedClean = true
		break
	}
	if !pickedClean {
		if lastInboundPortErr == nil {
			lastInboundPortErr = errNoFreeInboundPort
		}
		response.WriteJSON(w, response.ErrDefault(lastInboundPortErr.Error()))
		return
	}

	if err := applyInboundToModel(in, &row); err != nil {
		releasePortAlloc()
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// DB-2: resolve the share address mode. An explicit mode wins; a
	// request carrying only a ShareHost keeps the pre-mode behaviour
	// (host set → custom, host cleared → back to auto). Validation needs
	// the node, whose address families decide ipv4/ipv6 availability.
	// R5X-012: the resolution only honours fields the request explicitly
	// carries. On update, req.ShareHost above was backfilled from the
	// stored row when shareHost was absent (R4X-018 presence), so the
	// backfilled value must not be mistaken for an explicitly carried
	// host: a stored row pairing an explicit ipv4/ipv6 mode with a
	// dormant ShareHost would otherwise flip to custom on any partial
	// update omitting both share keys, silently activating the dormant
	// host. When neither key is present the stored mode — and, below,
	// the stored host — are preserved verbatim.
	// Mode explicitness follows the pointer convention used by the
	// other pointer fields (an explicit JSON null decodes to nil and
	// counts as absent, exactly like an omitted key); host explicitness
	// comes from the raw-key presence map (a plain string field cannot
	// distinguish omitted from explicitly empty on its own).
	shareKeysPresent := req.ShareAddressMode != nil || present["shareHost"]
	shareMode := inboundShareMode(&row)
	if update && !shareKeysPresent {
		// Both share keys omitted: keep the stored mode as resolved
		// from the stored row; no host inference from the backfill.
	} else if req.ShareAddressMode != nil {
		shareMode = strings.TrimSpace(*req.ShareAddressMode)
	} else if req.ShareHost != "" {
		shareMode = "custom"
	} else if shareMode == "custom" {
		shareMode = "auto"
	}
	shareNode, err := h.repo.GetNodeByID(row.NodeID)
	if err != nil || shareNode == nil {
		releasePortAlloc()
		response.WriteJSON(w, response.ErrDefault("节点不存在"))
		return
	}
	if err := validateShareAddress(shareMode, req.ShareHost, shareNode); err != nil {
		releasePortAlloc()
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	row.ShareAddressMode = shareMode
	if shareMode != "custom" && !(update && !shareKeysPresent) {
		// Non-custom modes drop the host — but only for a value the
		// request actually carried (or a create). A backfilled dormant
		// host on a both-keys-omitted partial update is stored state,
		// not a request value: preserve it with its stored mode
		// (R5X-012).
		req.ShareHost = ""
	}

	row.Name = req.Name
	row.ShareHost = req.ShareHost
	row.Remark = strings.TrimSpace(req.Remark)
	prevAcceptProxy := row.AcceptProxyProtocol
	row.RelayOnly = boolToInt(req.RelayOnly)
	row.AcceptProxyProtocol = boolToInt(req.AcceptProxyProtocol)
	if row.AcceptProxyProtocol == 1 {
		// Clients cannot speak PROXY protocol, so direct access stops working.
		row.RelayOnly = 1
	}
	row.RelayWhitelist = boolToInt(req.RelayWhitelist)
	row.WhitelistExtra = strings.Join(allowExtra, ", ")
	if req.Enabled != nil {
		row.Enabled = boolToInt(*req.Enabled)
	}
	// SS 订阅计量开关（F1：资格口径与对账统一收口）：协议资格
	// （ssMeteringEligible）维持旧口径——显式开启不合格直接拒绝，
	// 未显式开启时入站变得不合格（如改方法）静默关开关、伴生由对账
	// 回收。形态资格（ssMeteringShapeBlock：仅中转/PROXY/链式出口/
	// 分流）是本轮新增的硬闸：显式开启落在被排除形态上同样拒绝；
	// 已开启的入站被本次保存改成被排除形态也拒绝（提示先关计量），
	// 否则开关存着、伴生永不物化，计量静默失效（B-01/C-01/A-01）；
	// 存量已是被排除形态的（本闸上线前落库）静默关开关自愈。判定
	// 用已定稿的内核形态 in 与落库行 row（与对账读到的形态一致）。
	if req.MeteringEnabled != nil {
		row.MeteringEnabled = boolToInt(*req.MeteringEnabled)
	} else if !update {
		// S05 (AUDIT-HIST): a create that omits meteringEnabled
		// defaults the switch ON when the inbound can carry metering
		// (SS2022, no ShadowTLS front, direct-serving shape) — the
		// API half of the frontend default-on. Explicit false still
		// opts out; the eligibility/shape gates below silently turn
		// an ineligible default back off (explicitMetering is false
		// here, so nothing is refused). Updates never default: an
		// omitted field keeps the stored switch.
		if ssMeteringEligible(in) && ssMeteringShapeBlock(&row) == "" {
			row.MeteringEnabled = 1
		}
	}
	explicitMetering := req.MeteringEnabled != nil && *req.MeteringEnabled
	if row.MeteringEnabled == 1 && !ssMeteringEligible(in) {
		if explicitMetering {
			releasePortAlloc()
			response.WriteJSON(w, response.ErrDefault("仅 Shadowsocks 2022（非 ShadowTLS 前置）的自建入站支持订阅计量"))
			return
		}
		row.MeteringEnabled = 0
	}
	if row.MeteringEnabled == 1 {
		if block := ssMeteringShapeBlock(&row); block != "" {
			switch {
			case explicitMetering:
				releasePortAlloc()
				response.WriteJSON(w, response.ErrDefault(block))
				return
			case update && prevMetering == 1 && prevMeteringBlock == "":
				releasePortAlloc()
				response.WriteJSON(w, response.ErrDefault("该入站已开启订阅计量："+block+"，请先关闭订阅计量"))
				return
			default:
				row.MeteringEnabled = 0
			}
		}
	}
	row.UpdatedTime = now

	// R2-F2-C-L01: target-side save-time interception. If this edit
	// changes the address other inbounds dial to reach this one (share
	// host/mode, listen, port), re-validate every referrer's outbound
	// IP strategy against the new endpoint before anything persists —
	// the same rejection the source side applies (validateChainVersion),
	// so a conflicting edit is refused here instead of silently
	// dropping the referrers' chained traffic at render time.
	if update && (row.ShareHost != prevShareHost || shareMode != prevShareMode || row.Listen != prevListen || row.Port != prevPort) {
		if err := h.validateReferrerChainPolicies(&row); err != nil {
			releasePortAlloc()
			// R2-F4-01：链式引用查询的仓储错已标记，先守卫泛化；
			// 定型策略冲突文案保留原文案。
			if respondInternalIfRepoError(w, "inboundSave: validateReferrerChainPolicies failed", err) {
				return
			}
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}

	if update {
		err = h.repo.SaveNodeInbound(&row)
	} else {
		err = h.repo.CreateNodeInbound(&row)
	}
	// R5R3-001: 临界区结束，立即释放锁。下游 applyKernel/applyChainReferrers/
	// applyChangedTargets 会做网络调用（节点下发），必须无锁执行。
	releasePortAlloc()
	if err != nil {
		respondInternalError(w, "inboundSave", err)
		return
	}
	// SS 计量：开关/方法/启停变化后立即对账伴生行（对账内部会下发受
	// 影响内核；下方 applyKernel 对本内核再推一次，同 hash 无害）。
	h.reconcileSSMetering()

	var warnings []string
	if update && prevAcceptProxy != row.AcceptProxyProtocol {
		warnings = h.syncRelayProxyProtocol(row)
	}
	// R2X-009：入站 Port/Listen 变更后，名下 Managed relay 的物化
	// RemoteAddr 必须同批重算 + 重下发（与上方 PP 同步同位置、同口径），
	// 否则全部中转继续拨旧端口/旧语义地址静默断流。
	if update && (row.Port != prevPort || row.Listen != prevListen) {
		warnings = append(warnings, h.resyncManagedRelayRemoteAddrsForInbound(row.ID)...)
	}
	if err := h.applyKernel(k.ID, false); err != nil {
		// F2-APPLY-01：确认待定不是失败——警告只说「已发出、正在确认」。
		if errors.Is(err, errApplyConfirmPending) {
			warnings = append([]string{"下发已发出，正在确认生效状态"}, warnings...)
		} else if strings.Contains(err.Error(), "不在线") {
			// F-R5-APPLY-01-OBS：干净停机（节点离线）不是下发失败——
			// applyKernel 本身已把该情形归类为 pending（同谓词），
			// 重连后 onNodeOnline 自动重推收敛；回执只说离线待重推，
			// 绝不缀「下发失败」。
			warnings = append([]string{"节点离线，下发待节点重连后自动重推"}, warnings...)
		} else {
			warnings = append([]string{"下发到节点失败: " + err.Error()}, warnings...)
		}
	}
	if update {
		warnings = append(warnings, h.applyChainReferrers(row.ID)...)
	}
	warnings = append(warnings, h.applyChangedTargets(prevTargets, row)...)
	// FIX-UDP-ENTRY (a)：本次保存后该入站若是 ShadowTLS 前置的
	// SS 入站、且名下已有存续中转或有转发直接拨其公网口，其公
	// 网口 UDP 会被静默丢弃——随本函数既有的 warnings 回执
	// （“已保存，但…”）点名；无引用的入站不警告（中转/转发创
	// 建时另行校验）。
	warnings = append(warnings, h.shadowTLSRelayWarningsForInbound(row.ID)...)
	// OPEN-3：PROXY 开关开着但白名单没开时，直连客户端可伪造
	// PROXY 头、污染连接日志里的客户端 IP——保存回执点名。
	if w := acceptProxyProtocolSpoofWarning(row); w != "" {
		warnings = append(warnings, w)
	}
	if len(warnings) > 0 {
		response.WriteJSON(w, response.R{Code: 0, Msg: "已保存，但" + strings.Join(warnings, "；"), TS: now, Data: map[string]interface{}{"id": row.ID}})
		return
	}
	response.WriteJSON(w, response.OK(map[string]interface{}{"id": row.ID}))
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (h *Handler) pickFreeInboundPort(nodeID, excludeInboundID int64) (int, error) {
	return h.pickFreeInboundPortExcluding(nodeID, excludeInboundID, nil)
}

// pickFreeInboundPortExcluding 与 pickFreeInboundPort 同口径，但把
// exclude 里的已试端口从候选集里剔掉——换口重试时不得把刚撞过的口
// 再挑回来（与 pickTunnelPortExcluding 同理，F2-PORT-01 P0-2）。
func (h *Handler) pickFreeInboundPortExcluding(nodeID, excludeInboundID int64, exclude map[int]bool) (int, error) {
	return pickFreePortWithFallback(20000, 60000, func(port int) (bool, error) {
		if exclude[port] {
			return true, nil
		}
		msg, err := h.inboundPortConflict(nodeID, kernel.Inbound{Port: port}, excludeInboundID)
		return msg != "", err
	})
}

// errNoFreeInboundPort 是选口耗尽的定型业务错误（范围内全满），调用方
// 可以 errors.Is 区分后原样回执；冲突查询的仓储错误仍走通用出口。
var errNoFreeInboundPort = errors.New("未找到可用端口，请手动指定")

// pickFreePortWithFallback 在 [lo, hi) 内选一个无冲突端口。
// B3-02：此前只有 64 次纯随机试探，高占用时（97.5%）约 20% 概率
// 假报「未找到可用端口」且每次试探都跑全套冲突查询。现随机试探
// （摊开并发选口的落点）用尽后转确定性全范围线性扫描（随机起
// 点、逐个复检同一冲突谓词）：只要范围内还有空口必然找到，
// 全满才报错。
func pickFreePortWithFallback(lo, hi int, conflict func(port int) (bool, error)) (int, error) {
	span := hi - lo
	if span <= 0 {
		return 0, errNoFreeInboundPort
	}
	for i := 0; i < 64; i++ {
		port := lo + rand.Intn(span)
		taken, err := conflict(port)
		if err != nil {
			return 0, err
		}
		if !taken {
			return port, nil
		}
	}
	start := rand.Intn(span)
	for i := 0; i < span; i++ {
		port := lo + (start+i)%span
		taken, err := conflict(port)
		if err != nil {
			return 0, err
		}
		if !taken {
			return port, nil
		}
	}
	return 0, errNoFreeInboundPort
}

// portSpan is a port range an inbound occupies on its node.
type portSpan struct {
	lo, hi int
	what   string
}

func (s portSpan) String() string {
	if s.lo == s.hi {
		return fmt.Sprintf("%s %d", s.what, s.lo)
	}
	return fmt.Sprintf("%s %d-%d", s.what, s.lo, s.hi)
}

func (s portSpan) overlaps(o portSpan) bool { return s.lo <= o.hi && o.lo <= s.hi }

// inboundSpans lists the ports an inbound needs: its listen port and
// the port-hopping range.
func inboundSpans(in kernel.Inbound) []portSpan {
	spans := []portSpan{{in.Port, in.Port, "端口"}}
	if lo, hi := in.HopRange(); lo > 0 {
		spans = append(spans, portSpan{lo, hi, "端口跳跃范围"})
	}
	return spans
}

// inboundConflictBlockedError 是 inboundPortConflict 的 fail-closed
// 拒绝（R-068）：某损坏行的跳跃范围无法恢复时，新建/更新整单拒绝
// 并点名该行；调用方以 errors.As 分流后原样回执，不走通用
// Internal error（后者会吞掉行名，管理员无从自救）。
type inboundConflictBlockedError struct{ msg string }

func (e *inboundConflictBlockedError) Error() string { return e.msg }

// damagedInboundHopConflict 从 inboundFromModel 失败的损坏行里恢复
// 其端口跳跃范围（Settings JSON 可解析时）并与新建/更新的 spans
// 比对。Settings 可解析时回 (冲突文案, true)——不重叠时文案为空串；
// Settings 本身损坏到无法恢复时回 ("", false)，由调用方 fail-closed。
func damagedInboundHopConflict(row model.NodeInbound, spans []portSpan) (string, bool) {
	var in kernel.Inbound
	if err := json.Unmarshal([]byte(row.Settings), &in.Settings); err != nil {
		return "", false
	}
	for _, os := range inboundSpans(in)[1:] {
		for _, sp := range spans {
			if sp.overlaps(os) {
				return fmt.Sprintf("%s 与入站「%s」的%s 冲突", sp, row.Name, os), true
			}
		}
	}
	return "", true
}

// inboundPortConflict describes the first clash between the ports an
// inbound needs and what already runs on the node ("" when there is none).
func (h *Handler) inboundPortConflict(nodeID int64, in kernel.Inbound, excludeInboundID int64) (string, error) {
	spans := inboundSpans(in)
	for _, sp := range spans {
		occ, err := h.repo.NodePortRangeOccupant(nodeID, sp.lo, sp.hi, excludeInboundID)
		if err != nil {
			return "", err
		}
		if occ != "" {
			return fmt.Sprintf("%s 已被%s占用", sp, occ), nil
		}
	}
	for _, sp := range spans {
		if sp.lo == sp.hi {
			continue
		}
		// A hop range inside the node's forwarding range would capture the
		// UDP traffic of forwards created there later.
		spec, err := h.repo.GetNodePortRange(nodeID)
		if err != nil {
			return "", err
		}
		// B4/F-016（变体）: 此前用 min-max 跨度判重叠，多段规格下段间
		// 空隙会被误报重叠（合法入站被拒）。改为集合成员判定。
		if ports, perr := parsePorts(spec); perr == nil && len(ports) > 0 {
			inSpec := make(map[int]bool, len(ports))
			for _, p := range ports {
				inSpec[p] = true
			}
			for p := sp.lo; p <= sp.hi; p++ {
				if inSpec[p] {
					return fmt.Sprintf("%s 与节点的转发端口段 %s 重叠", sp, spec), nil
				}
			}
		}
	}
	others, err := h.repo.ListNodeInboundsByNode(nodeID)
	if err != nil {
		return "", err
	}
	for _, row := range others {
		if row.ID == excludeInboundID {
			continue
		}
		other, err := inboundFromModel(row)
		if err != nil {
			// R-068：损坏行不得跳过比对——跳跃范围只存在于 Settings
			// JSON，本轮是跳跃范围之间唯一的冲突检查，旧 `continue`
			// 使损坏行的跳跃范围永不参与比对（重叠仍放行创建，agent
			// 端口跳跃重定向撞口）。Settings 可恢复时按其跳跃范围
			// 继续比对（重叠报冲突、不重叠放行）；Settings 本身损坏
			// 到无法恢复时 fail-closed，点名该行拒绝新建（不静默放行）。
			if msg, ok := damagedInboundHopConflict(row, spans); ok {
				if msg != "" {
					return msg, nil
				}
				continue
			}
			return "", &inboundConflictBlockedError{msg: fmt.Sprintf("入站「%s」（ID %d）数据损坏，无法校验端口冲突：%v；请先修复或删除该入站后再创建", row.Name, row.ID, err)}
		}
		// Listen ports were checked by the query above; compare the rest.
		for _, os := range inboundSpans(other)[1:] {
			for _, sp := range spans {
				if sp.overlaps(os) {
					return fmt.Sprintf("%s 与入站「%s」的%s 冲突", sp, row.Name, os), nil
				}
			}
		}
	}
	return "", nil
}

func (h *Handler) inboundDelete(w http.ResponseWriter, r *http.Request) {
	// R2-K2 family closure (B11-001): the tail awaits applyKernel for
	// the deleted inbound's kernel, whose timeout conditionally
	// escalates to kernelApplyTimeout (300s); arm the write relief.
	extendWriteDeadline(w, kernelApplyWriteDeadline)
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	row, err := h.repo.GetNodeInbound(id)
	if err != nil {
		respondInternalError(w, "inboundDelete", err)
		return
	}
	if row == nil {
		// X-P3-28（L-11-3，V-12）：不存在幂等成功。
		response.WriteJSON(w, response.OKEmpty())
		return
	}
	if row.CompanionOf > 0 {
		response.WriteJSON(w, response.ErrDefault("计量伴生入站由系统管理，不能单独删除"))
		return
	}
	// R3X-015：守卫查询失败 fail-closed 中止删除，不得当作无引用放行。
	msg, guardErr := h.chainInUseMessage(row)
	if guardErr != nil {
		// R2-F4-01：守卫查询的仓储错已标记，泛化；无引用文案
		// 本身只在 guardErr == nil 时使用。
		if respondInternalIfRepoError(w, "inboundDelete: chainInUseMessage failed", guardErr) {
			return
		}
		response.WriteJSON(w, response.Err(-2, guardErr.Error()))
		return
	}
	if msg != "" {
		response.WriteJSON(w, response.ErrDefault(msg))
		return
	}
	h.removeInboundRelays(id)
	// R9-08（I-F2-03）：落库前按最终引用集复检——守卫与删除之间隔
	// 着 relay 清理，期间新保存的引用不在守卫结论内，照旧删除会
	// 静默打断它。复检命中则中止删除并点名（relay 已清，按删除
	// 失败同口径补刷白名单，见下方 R5X-004(e)）；查询失败
	// fail-closed，同守卫口径。
	recheckMsg, recheckErr := h.chainInUseMessage(row)
	if recheckErr != nil {
		// T-001：补偿刷新失败时回执须点名（已删 relay 的拨号地址
		// 可能残留在 allow 中）。
		refreshNote := ""
		if rerr := h.refreshInboundWhitelist(id); rerr != nil {
			refreshNote = whitelistRefreshNoteForErr(id, rerr)
		}
		// R2-F4-01：与上方首检同口径，守卫查询的仓储错泛化
		//（respondInternalErrorWithNote 兼带 T-001 刷新失败点名）。
		if isRepoError(recheckErr) {
			respondInternalErrorWithNote(w, "inboundDelete: chainInUseMessage recheck failed", recheckErr, refreshNote)
			return
		}
		raw := recheckErr.Error()
		if refreshNote != "" {
			raw += "；" + refreshNote
		}
		response.WriteJSON(w, response.Err(-2, raw))
		return
	}
	if recheckMsg != "" {
		// T-001：同上，补偿刷新失败进回执。
		refreshNote := ""
		if rerr := h.refreshInboundWhitelist(id); rerr != nil {
			refreshNote = "；" + whitelistRefreshNoteForErr(id, rerr)
		}
		response.WriteJSON(w, response.ErrDefault(recheckMsg+refreshNote))
		return
	}
	if err := h.repo.DeleteNodeInbound(id); err != nil {
		// R5X-004(e)：removeInboundRelays 已逐条删掉本入站的 relay
		// 与其转发，入站本体尚存——失败返回前按现行 DB 补刷本入站
		// 白名单（refreshInboundWhitelist 内部按 RelayWhitelist 门
		// 控、非白名单入站为空转），否则已删中转的拨号地址残留在
		// 本入站 allow 中。outboundTargets 面不受影响：本入站（拨
		// 号方）尚存、其链式关系未变。T-001：补偿刷新失败时回执须
		// 点名；错误本体按 F4-01 泛化（respondInternalErrorWithNote）。
		refreshNote := ""
		if rerr := h.refreshInboundWhitelist(id); rerr != nil {
			refreshNote = whitelistRefreshNoteForErr(id, rerr)
		}
		respondInternalErrorWithNote(w, "inboundDelete", err, refreshNote)
		return
	}
	// E6-S1 同型补齐：连带删除计量伴生行移至原入站删除成功之后
	// （同一内核，下方 applyKernel 一并覆盖）。旧序先删伴生：原入
	// 站删除失败时伴生已被删，须由后续对账以新端口 + 新身份凭据
	// 重建，已分发的计量链接在窗口内指向失效的旧伴生。
	h.deleteCompanionOf(id)
	// R10-04：外部行若来自订阅源，手工删除同写删除屏蔽，源后续同步
	// 不再自动恢复它（见 remote_source.go 的 shieldDeletedImportedRow）。
	// 删除本体已落库，屏蔽登记失败不回滚删除、只在回执里点名。
	var shieldErr error
	if isExternalInbound(row) {
		shieldErr = h.shieldDeletedImportedRow(row)
	}
	for _, target := range outboundTargets(*row) {
		h.applyWhitelistedTarget(target)
	}
	if isExternalInbound(row) {
		if shieldErr != nil {
			response.WriteJSON(w, response.R{Code: 0, Msg: "已删除，但删除屏蔽登记失败，订阅源后续同步可能恢复该节点: " + shieldErr.Error(), TS: time.Now().UnixMilli()})
			return
		}
		if row.SourceID > 0 {
			response.WriteJSON(w, response.R{Code: 0, Msg: "已删除。该节点来自订阅源，已登记删除屏蔽，订阅源后续同步不会再自动恢复它", TS: time.Now().UnixMilli()})
			return
		}
		response.WriteJSON(w, response.OKEmpty())
		return
	}
	if err := h.applyKernel(row.KernelID, false); err != nil {
		// F2-APPLY-01 同族：确认待定不是失败——删除已落库、下发
		// 已发出，回执只说「正在确认」，不前缀「下发失败」。
		if errors.Is(err, errApplyConfirmPending) {
			response.WriteJSON(w, response.R{Code: 0, Msg: "已删除，下发已发出，正在确认生效状态", TS: time.Now().UnixMilli()})
			return
		}
		response.WriteJSON(w, response.R{Code: 0, Msg: "已删除，但下发到节点失败: " + err.Error(), TS: time.Now().UnixMilli()})
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

// inboundBatchDelete removes several inbounds and their relays, then pushes
// each affected kernel's config once instead of once per inbound.
func (h *Handler) inboundBatchDelete(w http.ResponseWriter, r *http.Request) {
	// R2-K2 family closure (B11-001): the tail applies each affected
	// kernel once (deduped), each with the conditional
	// kernelApplyTimeout bound; arm the batch write relief.
	extendWriteDeadline(w, inboundBatchWriteDeadline)
	ids := idsFromBody(r, w)
	if ids == nil {
		return
	}
	// R10-11（I-C4-01）：两段式批删。旧实现逐条顺序处理、批内豁免
	// batchDeletesReferrers 按**请求集**判：目标 A 的引用方 B 只要
	// 在请求集内，A 的守卫与落库前复检都放行；B 随后因批外引用独
	// 立失败而存活时，A 已先行删除、B 的出站悬空、数据面丢弃其流
	// 量，且回执无一处点名被打断的 B→A 引用。现分两段：
	//   段一（结果集守卫）：不动任何数据，按「引用方确将同批删
	//   除才豁免」求最大可删集——一条入站可删 ⟺ 它的全部引用方
	//   都可删；批外引用、批内不可删的引用方都点名记失败。
	//   段二（删除）：按引用拓扑序（先删引用方、后删被引用方）
	//   逐条执行，逐条仍走 R9-08 落库前新鲜复检（批次处理期间新
	//   保存的引用不在段一快照内，复检以现行引用集为据：仍存活
	//   的引用方一律拦，不再按请求集豁免）。
	// 回执 failures 按请求序组装（failAt 以请求位索引），与旧逐条
	// 顺序处理的回执顺序一致。
	type batchDelItem struct {
		pos    int
		id     int64
		row    *model.NodeInbound
		failed bool
	}
	items := make([]*batchDelItem, 0, len(ids))
	failAt := make(map[int]batchFailureDetail, len(ids))
	recordFailure := func(it *batchDelItem, detail batchFailureDetail) {
		it.failed = true
		failAt[it.pos] = detail
	}
	failureReason := func(id int64, name, reason string) batchFailureDetail {
		return appendBatchFailureReason(nil, id, name, reason)[0]
	}
	failureErr := func(id int64, name string, err error) batchFailureDetail {
		return appendBatchFailure(nil, id, name, err)[0]
	}
	// 预检：行不存在/伴生行按旧口径逐条记失败，不入两段流程。
	for pos, id := range ids {
		row, err := h.repo.GetNodeInbound(id)
		it := &batchDelItem{pos: pos, id: id}
		if err != nil || row == nil {
			recordFailure(it, failureReason(id, "", "节点服务不存在"))
			continue
		}
		it.row = row
		if row.CompanionOf > 0 {
			recordFailure(it, failureReason(id, row.Name, "计量伴生入站由系统管理，不能单独删除"))
			continue
		}
		items = append(items, it)
	}

	// 段一：整批共用一份全表快照（X04-001：一轮一载，避免 O(条数)
	// 次全表扫描）。R3X-015：守卫查询失败 fail-closed——全批记失败、
	// 不删除，不得把查询失败读作无引用或「引用方全在批内」。
	planned := make(map[int64]bool, len(items))
	var snapIx *chainRefIndex
	if len(items) > 0 {
		ix, guardErr := h.loadChainRefIndex()
		if guardErr != nil {
			for _, it := range items {
				recordFailure(it, failureErr(it.id, it.row.Name, fmt.Errorf("查询入站 %d 的链式引用方失败: %w", it.id, guardErr)))
			}
		} else {
			snapIx = ix
			for _, it := range items {
				planned[it.id] = true
			}
			// 最大可删集（不动点）：任一条目的引用方落在可删集外
			// （批外行、预检失败行、已被剔出的批内行），该条目即
			// 不可删；剔出会连带其被引用方，迭代至稳定。
			for changed := true; changed; {
				changed = false
				for _, it := range items {
					if !planned[it.id] {
						continue
					}
					for _, ref := range ix.referrers(it.id) {
						if !planned[ref.ID] {
							planned[it.id] = false
							changed = true
							break
						}
					}
				}
			}
			// 不可删条目逐条记失败，点名真正拦住它的引用方（仍存
			// 活者）——含「引用方在批内但自身被批外引用挡下」的
			// 连带格，被打断的引用关系在回执里有落点。
			for _, it := range items {
				if planned[it.id] {
					continue
				}
				var blockers []model.NodeInbound
				for _, ref := range ix.referrers(it.id) {
					if !planned[ref.ID] {
						blockers = append(blockers, ref)
					}
				}
				recordFailure(it, failureReason(it.id, it.row.Name, h.chainInUseMessageFromRefs(it.row, blockers)))
			}
		}
	}

	// 删除序：引用拓扑（引用方先于被引用方），请求序作同层tie-break；
	// 批内互引成环时整环同批删、环内退回请求序（尾部统一 apply，
	// 环内中间态不下发）。
	ordered := make([]*batchDelItem, 0, len(items))
	if snapIx != nil {
		placed := make(map[int64]bool, len(items))
		var remaining []*batchDelItem
		for _, it := range items {
			if planned[it.id] {
				remaining = append(remaining, it)
			}
		}
		for len(remaining) > 0 {
			var deferred []*batchDelItem
			progress := false
			for _, it := range remaining {
				ready := true
				for _, ref := range snapIx.referrers(it.id) {
					if planned[ref.ID] && !placed[ref.ID] {
						ready = false
						break
					}
				}
				if ready {
					ordered = append(ordered, it)
					placed[it.id] = true
					progress = true
				} else {
					deferred = append(deferred, it)
				}
			}
			if !progress {
				ordered = append(ordered, deferred...)
				break
			}
			remaining = deferred
		}
	}

	// 段二：逐条删除。deletedIDs 供复检滤除本批已删引用方。
	deletedIDs := make(map[int64]bool)
	liveRefs := func(ix *chainRefIndex, targetID int64) []model.NodeInbound {
		refs := ix.referrers(targetID)
		if len(deletedIDs) == 0 {
			return refs
		}
		out := make([]model.NodeInbound, 0, len(refs))
		for _, ref := range refs {
			if !deletedIDs[ref.ID] {
				out = append(out, ref)
			}
		}
		return out
	}
	s := 0
	var targetIDs []int64
	kernelIDs := make([]int64, 0)
	seenKernel := make(map[int64]bool)
	markKernel := func(row *model.NodeInbound) {
		if !isExternalInbound(row) && !seenKernel[row.KernelID] {
			seenKernel[row.KernelID] = true
			kernelIDs = append(kernelIDs, row.KernelID)
		}
	}
	for _, it := range ordered {
		id, row := it.id, it.row
		h.removeInboundRelays(id)
		// R9-08（I-F2-03）：落库前按最终引用集复检——段一快照在批
		// 次开始时定格，批次处理期间新保存的引用不在快照内。此处
		// 逐条现查新鲜引用集：批内已删引用方由 liveRefs 滤除，其
		// 余仍存活的引用方一律拦并点名（R10-11 起不再按请求集豁
		// 免——段一已保证排在本条之前的批内引用方都先删，若此刻
		// 仍有批内引用方存活，只可能是处理期间新保存的引用，按
		// 批外引用同口径拦）；命中则该条记失败，按删除失败同口
		// 径把其内核汇入末尾 apply 集（relay 已清、allow 按现行
		// DB 重算，理由同下方 R5X-004(e)）；查询失败 fail-closed，
		// 同守卫口径。
		freshIx, freshErr := h.loadChainRefIndex()
		if freshErr != nil {
			recordFailure(it, failureErr(id, row.Name, fmt.Errorf("查询入站 %d 的链式引用方失败: %w", id, freshErr)))
			markKernel(row)
			continue
		}
		if msg := h.chainInUseMessageFromRefs(row, liveRefs(freshIx, id)); msg != "" {
			recordFailure(it, failureReason(id, row.Name, msg))
			markKernel(row)
			continue
		}
		if err := h.repo.DeleteNodeInbound(id); err != nil {
			recordFailure(it, failureErr(id, row.Name, err))
			// R5X-004(e)：失败项的 relay 已删而入站尚存——把该入站
			// 所在内核汇入末尾统一 apply 集，使其 allow 按现行 DB
			// 重算（与成功项同一下发面）。targetIDs 不汇入：本入站
			// （拨号方）尚存、其出站目标的 allow 不受 relay 删除影响。
			markKernel(row)
			continue
		}
		// E6-S1 同型补齐：伴生连带删除同样移至本行删除成功之后
		// （失败项的伴生保持原样，理由同 inboundDelete）。
		h.deleteCompanionOf(id)
		// R10-04：批删同写删除屏蔽（与单删同口径，见 inboundDelete）；
		// 登记失败不回滚已落库的删除、只记日志，批回执口径不变。
		if err := h.shieldDeletedImportedRow(row); err != nil {
			log.Printf("inbound batch delete: shield deleted imported inbound %d failed: %v", id, err)
		}
		deletedIDs[id] = true
		s++
		markKernel(row)
		targetIDs = append(targetIDs, outboundTargets(*row)...)
	}
	failures := make([]batchFailureDetail, 0, len(failAt))
	for pos := range ids {
		if detail, ok := failAt[pos]; ok {
			failures = append(failures, detail)
		}
	}
	msg := "操作成功"
	// F2-APPLY-01 同族：批内任一内核确认待定不是失败——真失败
	// 优先报（末个真失败为准，与旧口径一致），全待定时才报
	// pending，绝不把「已发出、正在确认」写成「下发失败」。
	pending := false
	for _, kernelID := range kernelIDs {
		if err := h.applyKernel(kernelID, false); err != nil {
			if errors.Is(err, errApplyConfirmPending) {
				pending = true
				continue
			}
			msg = "已删除，但下发到节点失败: " + err.Error()
		}
	}
	if msg == "操作成功" && pending {
		msg = "已删除，下发已发出，正在确认生效状态"
	}
	for _, targetID := range targetIDs {
		h.applyWhitelistedTarget(targetID)
	}
	response.WriteJSON(w, response.R{
		Code: 0,
		Msg:  msg,
		Data: batchOperationResult{SuccessCount: s, FailCount: len(failures), Failures: failures},
		TS:   time.Now().UnixMilli(),
	})
}

// inboundBatchToggle enables or disables several inbounds, then pushes each
// affected kernel's config once.
func (h *Handler) inboundBatchToggle(w http.ResponseWriter, r *http.Request) {
	// R2-K2 family closure (B11-001): same conditional per-kernel
	// apply tail as inboundBatchDelete; arm the batch write relief.
	extendWriteDeadline(w, inboundBatchWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		IDs     []int64 `json:"ids"`
		Enabled bool    `json:"enabled"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if len(req.IDs) == 0 {
		response.WriteJSON(w, response.ErrDefault("ids不能为空"))
		return
	}
	// R10-08：本面自有解析不走 idsFromBody，同口径计数帽（见
	// maxBatchIDs；帽按提交条数计，去重在帽后另行）。
	if !checkBatchIDsCap(w, len(req.IDs)) {
		return
	}
	// R2-F2-B-L01: dedupe before processing — a repeated id otherwise
	// passes the "already in target state" branch on its second
	// occurrence and inflates SuccessCount (the batch-delete family
	// dedupes via idsFromBody for exactly this reason).
	req.IDs = uniqueInt64s(req.IDs)
	s := 0
	f := 0
	failures := make([]batchFailureDetail, 0)
	kernelIDs := make([]int64, 0)
	seenKernel := make(map[int64]bool)
	now := time.Now().UnixMilli()
	for _, id := range req.IDs {
		row, err := h.repo.GetNodeInbound(id)
		if err != nil || row == nil {
			f++
			failures = appendBatchFailureReason(failures, id, "", "节点服务不存在")
			continue
		}
		if row.CompanionOf > 0 {
			f++
			failures = appendBatchFailureReason(failures, id, row.Name, "计量伴生入站由系统管理，不能单独启停")
			continue
		}
		if (row.Enabled == 1) == req.Enabled {
			s++
			continue
		}
		row.Enabled = boolToInt(req.Enabled)
		row.UpdatedTime = now
		if err := h.repo.SaveNodeInbound(row); err != nil {
			f++
			failures = appendBatchFailure(failures, id, row.Name, err)
			continue
		}
		s++
		if !isExternalInbound(row) && !seenKernel[row.KernelID] {
			seenKernel[row.KernelID] = true
			kernelIDs = append(kernelIDs, row.KernelID)
		}
	}
	// 启停原入站后同步伴生行启停（对账内部下发受影响内核）。
	h.reconcileSSMetering()
	msg := "操作成功"
	// F2-APPLY-01 同族：批内真失败优先报，全待定时才报 pending
	//（与 inboundBatchDelete 同口径）。
	pending := false
	for _, kernelID := range kernelIDs {
		if err := h.applyKernel(kernelID, false); err != nil {
			if errors.Is(err, errApplyConfirmPending) {
				pending = true
				continue
			}
			msg = "已保存，但下发到节点失败: " + err.Error()
		}
	}
	if msg == "操作成功" && pending {
		msg = "已保存，下发已发出，正在确认生效状态"
	}
	response.WriteJSON(w, response.R{
		Code: 0,
		Msg:  msg,
		Data: batchOperationResult{SuccessCount: s, FailCount: f, Failures: failures},
		TS:   time.Now().UnixMilli(),
	})
}

func (h *Handler) inboundToggle(w http.ResponseWriter, r *http.Request) {
	// R2-K2 family closure (B11-001): the tail awaits applyKernel,
	// whose timeout conditionally escalates to kernelApplyTimeout
	// (300s); arm the write relief.
	extendWriteDeadline(w, kernelApplyWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		ID      int64 `json:"id"`
		Enabled bool  `json:"enabled"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	row, err := h.repo.GetNodeInbound(req.ID)
	if err != nil || row == nil {
		response.WriteJSON(w, response.ErrDefault("节点服务不存在"))
		return
	}
	if row.CompanionOf > 0 {
		response.WriteJSON(w, response.ErrDefault("计量伴生入站由系统管理，不能单独启停"))
		return
	}
	row.Enabled = boolToInt(req.Enabled)
	row.UpdatedTime = time.Now().UnixMilli()
	if err := h.repo.SaveNodeInbound(row); err != nil {
		respondInternalError(w, "inboundToggle", err)
		return
	}
	if isExternalInbound(row) {
		response.WriteJSON(w, response.OKEmpty())
		return
	}
	// 启停原入站后同步伴生行启停（对账内部下发受影响内核）。
	h.reconcileSSMetering()
	if err := h.applyKernel(row.KernelID, false); err != nil {
		// F2-APPLY-01 同族：确认待定不是失败——启停已落库、下发
		// 已发出，回执只说「正在确认」，不前缀「下发失败」。
		if errors.Is(err, errApplyConfirmPending) {
			response.WriteJSON(w, response.R{Code: 0, Msg: "已保存，下发已发出，正在确认生效状态", TS: row.UpdatedTime})
			return
		}
		response.WriteJSON(w, response.R{Code: 0, Msg: "已保存，但下发到节点失败: " + err.Error(), TS: row.UpdatedTime})
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) inboundResetTraffic(w http.ResponseWriter, r *http.Request) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	// B10-001：与同族 toggle/update/regenerate 同口径——先查行，
	// 不存在直接报错；此前仅拦伴生行、其余更新 0 行也回成功，
	// 不存在 id 静默 code=0。
	row, err := h.repo.GetNodeInbound(id)
	if err != nil || row == nil {
		response.WriteJSON(w, response.ErrDefault("节点服务不存在"))
		return
	}
	if row.CompanionOf > 0 {
		response.WriteJSON(w, response.ErrDefault("计量伴生入站由系统管理，不能重置流量"))
		return
	}
	if err := h.repo.ResetNodeInboundTraffic(id, time.Now().UnixMilli()); err != nil {
		respondInternalError(w, "inboundResetTraffic", err)
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) inboundRegenerate(w http.ResponseWriter, r *http.Request) {
	// R2-K2 family closure (B11-001): the tail awaits applyKernel and
	// the chain referrers' applies, each with the conditional
	// kernelApplyTimeout bound; arm the write relief.
	extendWriteDeadline(w, kernelApplyWriteDeadline)
	// C1-06：凭据轮换接入幂等——同键重试/双击回放首轮回执，不再
	// 二次轮换使首轮刚下发的凭据作废（scope 登记见 idempotency.go）。
	var finish func()
	var replayed bool
	w, finish, replayed = h.idemBegin(w, r, idemUserID(r), "inbound:regenerate")
	defer finish()
	if replayed {
		return
	}
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	row, err := h.repo.GetNodeInbound(id)
	if err != nil || row == nil {
		response.WriteJSON(w, response.ErrDefault("节点服务不存在"))
		return
	}
	if row.CompanionOf > 0 {
		response.WriteJSON(w, response.ErrDefault("计量伴生入站由系统管理，不能更换密钥"))
		return
	}
	if isExternalInbound(row) {
		response.WriteJSON(w, response.ErrDefault("导入的节点由原服务器管理，不能在面板里更换密钥"))
		return
	}
	k, err := h.repo.GetNodeKernel(row.KernelID)
	if err != nil || k == nil {
		response.WriteJSON(w, response.ErrDefault("内核不存在"))
		return
	}
	in, err := inboundFromModel(*row)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if err := kernel.Regenerate(k.KernelType, &in); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if err := applyInboundToModel(in, row); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	row.UpdatedTime = time.Now().UnixMilli()
	if err := h.repo.SaveNodeInbound(row); err != nil {
		respondInternalError(w, "inboundRegenerate", err)
		return
	}
	if err := h.applyKernel(k.ID, false); err != nil {
		// F2-APPLY-01 同族：确认待定不是失败——凭据已轮换落库、
		// 下发已发出，回执只说「正在确认」，不前缀「下发失败」。
		if errors.Is(err, errApplyConfirmPending) {
			response.WriteJSON(w, response.R{Code: 0, Msg: "已重新生成，下发已发出，正在确认生效状态", TS: row.UpdatedTime})
			return
		}
		response.WriteJSON(w, response.R{Code: 0, Msg: "已重新生成，但下发到节点失败: " + err.Error(), TS: row.UpdatedTime})
		return
	}
	if warnings := h.applyChainReferrers(row.ID); len(warnings) > 0 {
		response.WriteJSON(w, response.R{Code: 0, Msg: "已重新生成，但" + strings.Join(warnings, "；"), TS: row.UpdatedTime})
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

// ---- dispatch ----

type agentKernelState struct {
	Type       string   `json:"type"`
	Running    bool     `json:"running"`
	Enabled    bool     `json:"enabled"`
	Pid        int      `json:"pid"`
	Version    string   `json:"version"`
	ConfigHash string   `json:"configHash"`
	Error      string   `json:"error"`
	Restarts   int      `json:"restarts"`
	StartedAt  int64    `json:"startedAt"`
	Logs       []string `json:"logs,omitempty"`
}

type agentKernelCounter struct {
	InboundID int64  `json:"inboundId"`
	Proto     string `json:"proto"`
	Dir       string `json:"dir"`
	// Packets is the packet count behind Bytes (FIX-029: realm IP-layer
	// counters are converted to the payload layer with it). Zero when
	// the agent predates the field; then bytes pass through unconverted.
	Packets int64 `json:"packets,omitempty"`
	Bytes   int64 `json:"bytes"`
}

type agentUserCounter struct {
	ID    int64  `json:"id"`
	Dir   string `json:"dir"`
	Bytes int64  `json:"bytes"`
}

type agentKernelStatus struct {
	Kernels      []agentKernelState   `json:"kernels"`
	Counters     []agentKernelCounter `json:"counters"`
	Users        []agentUserCounter   `json:"users"`
	Epoch        string               `json:"epoch"`
	NftAvailable bool                 `json:"nftAvailable"`
	NftError     string               `json:"nftError"`
	// NftReadError carries the agent's last nft counter-read failure
	// (R3A-NEW-1: table deleted or unreadable). Empty means the last
	// read succeeded; unlike NftError (auto-install failure) it is
	// set on every read failure so the panel can mark the node
	// failing, alert, and re-push the kernels that own the table.
	// R4C-02: a node that never had nftables reports no read error
	// at all (see NftNotInstalled) — a read error here means "a
	// table we had is gone".
	NftReadError string `json:"nftReadError"`
	// NftNotInstalled mirrors the agent's Status signal (R4C-02):
	// the node never had nftables (no binary, no table ever built).
	// Missing counters are the designed shape for such a node, not
	// a failure — the panel must not mark it failing, alert, or
	// re-push kernels for this signal. It wins over NftReadError
	// when both are set.
	NftNotInstalled bool `json:"nftNotInstalled,omitempty"`
	// NftAutoInstalled reports that the agent installed nftables
	// itself on this node (auto-install path); the panel stores
	// it as node nftables_status=auto_installed.
	NftAutoInstalled bool `json:"nftAutoInstalled,omitempty"`
	// MissingLogsFor mirrors the agent's Status signal (F2-23):
	// the requested kernel type has no instance on the node.
	MissingLogsFor string `json:"missingLogsFor,omitempty"`
}

func remarshal(in interface{}, out interface{}) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// userMarkBaseMinVersion 是 per-agent socket-mark base 落地的首个
// agent 版本（F2-DBL-01）：低于它 agent 的 nft 规则只认 legacy base，
// 面板给它渲染新 base 会让用户计数归零。R2 候选版是 2.1.12 之后的
// 首个发版（>= 2.1.13），故门槛取 2.1.13；实际发版号更高时门仍
// 成立（比较按版本大小，不按相等）。
const userMarkBaseMinVersion = "2.1.13"

// userMarkBaseForNode 是某节点的 socket-mark base：agent 版本达门
// 槛用 per-agent base（E-01：节点 secret 的 SHA-256 首字节作
// agent 域，同机多 agent 各计各的，不同面板的同 id 节点也不再
// 串扰），版本过旧/未知用 legacy base（旧行为：同机多 agent
// 仍会多计，待 agent 升级后下一次下发自动切到 per-agent base）。
// fail-closed 向旧行为：版本读不到绝不按新 base 渲染。
func userMarkBaseForNode(nodeID int64, secret, agentVersion string) int64 {
	normalised, ok := parseConnlogAgentVersion(agentVersion)
	if !ok {
		return kernel.LegacyUserMarkBase
	}
	cmp, err := compareVersions(normalised, userMarkBaseMinVersion)
	if err != nil || cmp < 0 {
		return kernel.LegacyUserMarkBase
	}
	return kernel.UserMarkBaseForAgent(nodeID, secret)
}

// buildKernelApplyPayload renders the kernel's enabled inbounds into the
// KernelApply command body.
func (h *Handler) buildKernelApplyPayload(k *model.NodeKernel) (map[string]interface{}, string, error) {
	return h.buildKernelApplyPayloadWith(k, nil)
}

// buildKernelApplyPayloadWith takes realm forwards to force on or off
// (realm.go).
func (h *Handler) buildKernelApplyPayloadWith(k *model.NodeKernel, realmOverride map[int64]bool) (map[string]interface{}, string, error) {
	info, ok := kernel.Lookup(k.KernelType)
	if !ok || !info.Implemented {
		return nil, "", fmt.Errorf("暂不支持内核 %s", k.KernelType)
	}
	if info.Relay {
		return h.buildRealmApplyPayload(k, info, realmOverride)
	}
	rows, err := h.repo.ListNodeInboundsByKernel(k.ID)
	if err != nil {
		return nil, "", err
	}
	// 入站连接日志（U7）：中转落地入站亦采集（R2-D-P2-1）——
	// 公开入站可同时收直连与中转落地，把“是中转落地目标”当作
	// 整入站排除的理由不成立（曾致直连该入站的客户端连接全部
	// 看不见）。中转落地腿会随之现一行（其客户端 IP 为上一跳
	// 地址，见 connlog_marks.go），直连必须可见。链式目标仍
	// 排除（见 connlogChainTargetInboundIDs）。
	chainTargets, err := h.connlogChainTargetInboundIDs()
	if err != nil {
		return nil, "", fmt.Errorf("查询入站链式归属失败: %w", err)
	}
	// 连接日志节点开关（node.conn_log_enabled，默认关）：采集
	// 门控 = 总开关 AND 本节点开关。关时 connlogEntries 恒下发
	// 空集（清 agent 旧标记）、connLogInbounds 不写键，采集全
	// 链停下（见 connlog_marks.go 的 connlogEnabledForNode）。
	connlogOn, err := h.connlogEnabledForNode(k.NodeID)
	if err != nil {
		return nil, "", fmt.Errorf("查询连接日志节点开关失败: %w", err)
	}
	inbounds := make([]kernel.Inbound, 0, len(rows))
	connLogInbounds := make([]int64, 0, len(rows))
	ports := make([]map[string]interface{}, 0, len(rows))
	connlogEntries := make([]map[string]interface{}, 0, len(rows))
	users := h.activeUsersByInbound(time.Now())
	// A companion inbound renders only while its origin renders: if the
	// origin is disabled (or gone), the companion must not keep serving
	// previously distributed metering links. This render-time gate is
	// the hard invariant; reconcile's Enabled mirroring is bookkeeping.
	// C-03: the gate is node-wide, not per-kernel — after the split the
	// companion kernel's rows are companions only, so a same-kernel map
	// would be empty and every companion would be skipped.
	originEnabled := make(map[int64]bool)
	nodeRows, err := h.repo.ListNodeInboundsByNode(k.NodeID)
	if err != nil {
		return nil, "", fmt.Errorf("查询节点入站失败: %w", err)
	}
	for _, row := range nodeRows {
		if row.CompanionOf == 0 {
			originEnabled[row.ID] = row.Enabled == 1
		}
	}
	// C-03: render-side type comparisons use the companion's render
	// family (sing-box); the payload's "type" field below keeps the raw
	// kernel type — that is the agent's instance key.
	renderType := kernel.EffectiveType(k.KernelType)
	// X04-001：白名单入站的链式拨号集共用一份全表快照，首个白名单
	// 入站时懒加载——旧实现逐入站经 chainDialerNodeIDs 重载全表。
	var chainIx *chainRefIndex
	for _, row := range rows {
		if row.Enabled != 1 {
			continue
		}
		if row.CompanionOf > 0 && !originEnabled[row.CompanionOf] {
			continue
		}
		in, err := inboundFromModel(row)
		if err != nil {
			return nil, "", err
		}
		// 下发前复检：组合必须在内核 catalog 内，历史脏数据不得被渲染层静默降级。
		if !kernel.ComboSupported(renderType, in.Protocol, in.Transport.Type, in.Security.Type) {
			return nil, "", fmt.Errorf("入站「%s」的协议组合（%s / %s / %s）与内核 %s 不匹配，已拒绝下发，请修正或删除该入站", row.Name, in.Protocol, in.Transport.Type, in.Security.Type, k.KernelType)
		}
		// 下发前复检（Settings 级条件字段）：ENC×内核、空闲超时×内核、
		// Vision×组合不在三元组与版本门内，存量毒行不得经渲染面静默
		// 降级或带病下发（R4-C2C-02）；谓词与创建面 Normalize 同源。
		if err := kernel.CheckSettingsConditions(renderType, &in); err != nil {
			return nil, "", fmt.Errorf("入站「%s」的设置与内核 %s 不匹配：%v，已拒绝下发，请修正或删除该入站", row.Name, k.KernelType, err)
		}
		if msg := kernelVersionGate(renderType, k.Version, &in); msg != "" {
			return nil, "", fmt.Errorf("入站「%s」与内核版本不兼容：%s，已拒绝下发", row.Name, msg)
		}
		if in.UserCapable() {
			in.Users = users[in.ID]
		}
		if in.Via, err = h.inboundVia(row); err != nil {
			return nil, "", err
		}
		if in.Split, err = h.inboundSplit(row); err != nil {
			return nil, "", err
		}
		// R2X-007 下发面：存量脏组合（保存期未拦）在下发期同判——任一
		// Via/分流目标带 ENC 且拨号内核 Xray <25.9.5，整单拒绝。
		if msg := encDialGate(renderType, k.Version, in.Via); msg != "" {
			return nil, "", fmt.Errorf("入站「%s」与内核版本不兼容：%s，已拒绝下发", row.Name, msg)
		}
		for _, rule := range in.Split {
			if msg := encDialGate(renderType, k.Version, rule.Via); msg != "" {
				return nil, "", fmt.Errorf("入站「%s」与内核版本不兼容：%s，已拒绝下发", row.Name, msg)
			}
		}
		// R9X-005 下发面：存量脏组合（保存期未拦）在下发期同判——任一
		// Via/分流目标为 Naive 且拨号内核 sing-box <1.13，整单点名拒绝
		//（与保存期 CheckChainWith 同点口径，仿 encDialGate 形态）。
		if msg := naiveDialGate(renderType, k.Version, in.Via); msg != "" {
			return nil, "", fmt.Errorf("入站「%s」与内核版本不兼容：%s，已拒绝下发", row.Name, msg)
		}
		for _, rule := range in.Split {
			if msg := naiveDialGate(renderType, k.Version, rule.Via); msg != "" {
				return nil, "", fmt.Errorf("入站「%s」与内核版本不兼容：%s，已拒绝下发", row.Name, msg)
			}
		}
		inbounds = append(inbounds, in)
		// 连接日志 opt-in 名单（U5↔U4 契约）：agent 内核日志解析器
		// 只对名单内的入站 tag 产连接事件（ApplyRequest.connLogInbounds）。
		// 名单只含渲染集内已开启采集的入站；伴生入站是系统派生监听、
		// 非用户入口（设计 §2 形态 6），恒排除。永无直连记录的形态
		// 同样排除——链式目标与分流目标（chainTargets，D-01 后含
		// SplitProxy 目标）、导入入站（SourceID>0）——它们的行本就
		// 被 connlogEntries 排除，收进名单只会白开日志级、交集恒空。
		// 中转落地入站不再排除（R2-D-P2-1）：公开入站可同时收直连
		// 与中转落地，排除整入站会让直连记录一并丢失。名单为空时
		// 不写键——agent 解码为 nil 即空集、采集器随之停用，与「最
		// 后一个开关关闭后采集收敛」同义。逐入站开关已撤：节点开关
		// 开时渲染集内全部合格入站都在名单内（采集门控只剩节点开关）。
		if connlogOn && !in.Companion &&
			!chainTargets[row.ID] && row.SourceID == 0 {
			connLogInbounds = append(connLogInbounds, in.ID)
		}
		port := map[string]interface{}{"inboundId": in.ID, "port": in.Port, "network": in.Network(), "bind": in.Listen}
		if lo, hi := in.HopRange(); lo > 0 {
			port["hopFrom"], port["hopTo"] = lo, hi
		}
		effWhitelist, effExtra, werr := h.effectiveInboundWhitelist(row)
		if werr != nil {
			return nil, "", werr
		}
		if effWhitelist {
			// R3X-015：白名单的链式拨号节点查询失败则整单中止，不
			// 下发少算拨号节点的白名单。
			if chainIx == nil {
				loaded, lerr := h.loadChainRefIndex()
				if lerr != nil {
					return nil, "", fmt.Errorf("查询入站 %d 的链式引用方失败: %w", row.ID, lerr)
				}
				chainIx = loaded
			}
			// 节点级覆盖时用节点列表替换逐入站附加名单再渲染
			// （逐入站存量字段不动）；关时 effExtra 即逐入站值。
			effRow := row
			effRow.WhitelistExtra = effExtra
			allow, aerr := h.inboundAllowListIx(effRow, chainIx)
			if aerr != nil {
				return nil, "", aerr
			}
			port["allow"] = allow
		}
		ports = append(ports, port)
		// U7 直连入口标记：渲染在册、非伴生、非导入、未声明「仅中
		// 转」面貌的入站，自身即客户端入口（链式源入站同理：
		// 客户端连入的是它，出站指向只作归属附加信息，不改变入口
		// 身份）。中转落地入站亦在列（R2-D-P2-1）：公开入站可同时
		// 收直连与中转落地，排除整入站会让直连记录一并丢失；中转
		// 落地腿随之现一行（其客户端 IP 为上一跳地址，见
		// connlog_marks.go），直连必须可见。白名单入站亦在列
		// （F2-WL-01）：白名单只是 nft 层过滤（中转机／本机／额外
		// 名单），不改变入口身份——额外名单本就是用户放行的直连
		// 来源，其直连记录是真数据；中转腿按两行口径现行。采集
		// 门控只有节点开关，白名单开与不开都不另设门。
		if connlogOn && row.CompanionOf == 0 && row.SourceID == 0 && !chainTargets[row.ID] &&
			row.RelayOnly == 0 {
			connlogEntries = append(connlogEntries, map[string]interface{}{
				"inboundId": row.ID,
				"tag":       in.Tag(),
			})
		}
	}
	// 1.7.7（KU5）：给 agent 下发的下载描述不再带面板配置的代理前
	// 缀——「开启 GitHub 加速」设置已整体移除，proxy 恒为空（直连
	// 形态的 URL / rule-set 引用），实际下载路线由 agent 侧内置候
	// 选在下载处自行测速直选（KU4）。
	proxy := ""
	// F2-DBL-01 / E-01：按节点算 socket-mark base（agent 版本达门槛用
	// per-agent base，否则 legacy base），渲染与下发同源——内核
	// 配置里的 mark 与 agent nft 规则的 mark 必须是同一个值，
	// 否则用户计数归零。节点读不到时 fail-closed 用 legacy。
	// R2C-01：同站按节点密钥派生伴生 Clash API 口（41000–41999）；
	// 节点读不到时口为 0，渲染省略该块（agent 回退重载，计量不受影响）。
	markBase := kernel.LegacyUserMarkBase // legacy；节点读到且版本达门槛才切 per-agent
	clashAPIPort := 0
	if node, err := h.repo.GetNodeByID(k.NodeID); err == nil && node != nil {
		markBase = userMarkBaseForNode(k.NodeID, node.Secret, node.Version.String)
		clashAPIPort = kernel.ClashAPIPortForSecret(node.Secret)
	} else if err != nil {
		log.Printf("user mark base node lookup failed node_id=%d err=%v action=legacy_base", k.NodeID, err)
	}
	rendered, err := kernel.RenderWith(renderType, inbounds, kernel.RenderOptions{
		RuleSetMirror:     proxy,
		SingboxHTTPClient: !versionBelowStrict(k.Version, 1, 14),
		SingboxSnell:      !versionBelowStrict(k.Version, 1, 14),
		SingboxNaive:      !versionBelowStrict(k.Version, 1, 13),
		// D-04：节点开关实值进渲染——关时日志形态回落（不升
		// debug／不开 access），与 connLogInbounds 的门同源。
		ConnLogEnabled: connlogOn,
		UserMarkBase:   markBase,
		// R2C-01: per-agent companion Clash API port (0 = omit).
		ClashAPIPort: clashAPIPort,
		// C-(4): the companion (raw type, before EffectiveType
		// folded it to singbox) exposes the Clash API so the
		// agent can close a removed user's live connections
		// selectively.
		Companion: k.KernelType == kernel.TypeSingboxCompanion,
	})
	if err != nil {
		return nil, "", err
	}
	download, err := kernel.ResolveDownload(info, k.Version, proxy)
	if err != nil {
		return nil, "", err
	}
	payload := map[string]interface{}{
		"type":     k.KernelType,
		"version":  k.Version,
		"download": download,
		"files":    rendered.Files,
		"args":     rendered.Args,
		"hash":     rendered.Hash,
		"enabled":  k.Enabled == 1 && !rendered.Idle,
		"ports":    ports,
		// U7：恒下发（可为空集）——拓扑变更后旧标记靠空集清除，
		// 不靠缺省键猜测（见 connlog_marks.go）。
		"connlogEntries": connlogEntries,
	}
	if len(rendered.UserMarks) > 0 {
		payload["userMarks"] = rendered.UserMarks
	}
	// F2-DBL-01：agent 的 nft 用户计数规则按这个 base 造 mark；
	// 与渲染进内核配置的 mark 同源（buildKernelApplyPayload 里
	// 同一个 markBase 值），两边不一致用户计数归零。旧 agent
	// 忽略该字段、继续用 legacy base（面板对旧 agent 渲染的
	// 也是 legacy base，见 userMarkBaseForNode）。
	payload["userMarkBase"] = markBase
	if len(connLogInbounds) > 0 {
		payload["connLogInbounds"] = connLogInbounds
	}
	if k.KernelType == kernel.TypeXray {
		// The agent keeps the 分流 data files next to Xray up to date.
		payload["geoData"] = kernel.XrayGeoData(proxy)
	}
	if len(rendered.Processes) > 0 {
		payload["processes"] = rendered.Processes
	}
	return payload, rendered.Hash, nil
}

type kernelVersionItem struct {
	Version     string `json:"version"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"publishedAt,omitempty"`
	// Source, sing-box only: "fork" for a FLMX fork build
	// (1.14.4-flmx1) or "upstream" for the rollback release
	// (1.14.4) — the two share a base version and are otherwise
	// indistinguishable in the picker (发现 1). Other kernels
	// have no fork build and leave this empty.
	Source string `json:"source,omitempty"`
}

// kernelVersionFetchCall 是一次进行中的内核版本抓取（singleflight
// 合并单元）：同 key 的并发调用方等在 done 上共享结果，不再各自排队
// 重抓（B11-001）。
type kernelVersionFetchCall struct {
	done  chan struct{}
	items []kernelVersionItem
	err   error
}

var kernelVersionCache = struct {
	sync.Mutex
	at      map[string]time.Time
	items   map[string][]kernelVersionItem
	flights map[string]*kernelVersionFetchCall
}{at: map[string]time.Time{}, items: map[string][]kernelVersionItem{}, flights: map[string]*kernelVersionFetchCall{}}

// kernelReleaseVersions lists recent releases of a kernel from GitHub,
// cached for 30 minutes. Kernels with a pinned list return that list.
//
// F-059 的「代理先行/直连降级」在 1.7.7 升级为内置候选测速直选
// （KU5，github_routes.go），与节点侧/面板侧版本发现同口径；流式
// 解码与 256MiB 兜底保持不变（见 fetchKernelReleaseVersionsPageCtx）。
func kernelReleaseVersions(info kernel.Info) ([]kernelVersionItem, error) {
	if len(info.Versions) > 0 {
		out := make([]kernelVersionItem, 0, len(info.Versions))
		for _, v := range info.Versions {
			out = append(out, kernelVersionItem{Version: v})
		}
		return out, nil
	}
	if info.ReleaseRepo == "" {
		return nil, nil
	}
	// B11-001：锁只护 map 读写，网络抓取在锁外。同 key 的并发冷请求
	// 经 flights 合并为一次抓取（等待方共享结果，总等待 ≤ 一次抓取
	// 界 60s < 前端 90s）；不同 key 互不阻塞。旧实现一把全局锁横跨
	// 整个抓取循环，第二个冷请求最坏 排队 60s + 自身 60s ≈ 120s，
	// 先被前端 90s 掐断报假超时。
	kernelVersionCache.Lock()
	if at, ok := kernelVersionCache.at[info.Type]; ok && time.Since(at) < 30*time.Minute {
		items := kernelVersionCache.items[info.Type]
		kernelVersionCache.Unlock()
		return items, nil
	}
	if call, ok := kernelVersionCache.flights[info.Type]; ok {
		kernelVersionCache.Unlock()
		<-call.done
		return call.items, call.err
	}
	call := &kernelVersionFetchCall{done: make(chan struct{})}
	kernelVersionCache.flights[info.Type] = call
	kernelVersionCache.Unlock()

	items, err := fetchKernelReleaseVersionsWithFallback(info)

	kernelVersionCache.Lock()
	if err == nil {
		kernelVersionCache.at[info.Type] = time.Now()
		kernelVersionCache.items[info.Type] = items
	}
	delete(kernelVersionCache.flights, info.Type)
	call.items, call.err = items, err
	close(call.done)
	kernelVersionCache.Unlock()
	return items, err
}

// sameVersionInstallWarning names a no-op reinstall on the success
// envelope (R2-F2-A2): the node already runs the requested version, so
// the agent keeps the existing binary and nothing was downloaded.
// Returns "" when the install actually changes (or first installs)
// the version.
//
// 发现 2：InstalledVersion 是 agent 上报的 banner（"sing-box
// version 1.14.4-flmx1 linux/amd64"），须先取其版本 token 再比；
// 且必须精确比较、fork 后缀计入版本身份——旧实现
// strings.Contains(prevInstalled, version) 把子串当相等：已装
// fork 1.14.4-flmx1 时换装上游 1.14.4 会命中，回执谎报"已安装该
// 版本 1.14.4，本次未重新下载"，实际已下载并降级。底版与后缀按
// 严格比较器的口径分别剥离比较（stripForkVersionSuffixForCompare）：
// 底版相同、后缀不同（fork↔上游任一方向）都不是同一版本。
func sameVersionInstallWarning(requestedLatest bool, prevInstalled, version string) string {
	if prevInstalled == "" || version == "" || !sameInstalledVersion(prevInstalled, version) {
		return ""
	}
	if requestedLatest {
		return fmt.Sprintf("已是最新版 %s，无需更新", version)
	}
	return fmt.Sprintf("已安装该版本 %s，本次未重新下载", version)
}

// sameInstalledVersion reports whether the previously installed
// version names exactly the version being installed: the version
// token of each side (banner-tolerant) must agree on both the base
// version and the -flmx* suffix, so a fork build and its upstream
// base are different versions in either direction.
func sameInstalledVersion(prevInstalled, version string) bool {
	if strings.EqualFold(strings.TrimSpace(prevInstalled), "latest") &&
		strings.EqualFold(strings.TrimSpace(version), "latest") {
		return true
	}
	prev, ok := installedVersionToken(prevInstalled)
	if !ok {
		return false
	}
	want, ok := installedVersionToken(version)
	if !ok {
		return false
	}
	return stripForkVersionSuffixForCompare(prev) == stripForkVersionSuffixForCompare(want) &&
		forkVersionSuffix(prev) == forkVersionSuffix(want)
}

// forkVersionSuffix returns the -flmx* suffix of a version token
// ("" when the token is not a fork build).
func forkVersionSuffix(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '-'); i >= 0 && forkVersionSuffixPattern.MatchString(v[i:]) {
		return v[i:]
	}
	return ""
}

// installedVersionTokenPattern 是 banner 内版本 token 的形态：
// 数字点分段加可选后缀（"1.14.4-flmx1"、"26.11.9"、
// "1.14.4-rc1"），与 kernelVersionEntryPattern 的单段文法同源，
// 但只取 token、不做安装合法性判定。
var installedVersionTokenPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+(?:\.[0-9]+)?(?:-[0-9A-Za-z.-]+)?$`)

// installedVersionToken scans a reported version string for its
// version token: a bare version passes through, a full banner
// ("sing-box version 1.14.4-flmx1 linux/amd64", "Xray 26.11.9
// (Xray, ...)") yields the token. No version-shaped token yields
// false — the caller then treats the install as a change, never
// as a no-op.
func installedVersionToken(raw string) (string, bool) {
	for _, tok := range strings.Fields(raw) {
		if installedVersionTokenPattern.MatchString(tok) {
			return strings.TrimPrefix(tok, "v"), true
		}
	}
	return "", false
}

// latestFallbackWarning names an install that fell back to storing the
// literal "latest" because the release list was unreachable at install
// time (fixR2-A3): the row carries the sentinel and the agent resolves
// it at download time through its own mirror routes. Returns "" when a
// concrete version was resolved (or "latest" was never requested).
func latestFallbackWarning(fellBack bool) string {
	if !fellBack {
		return ""
	}
	return "未能确认最新版具体版本号，已按 latest 安装"
}

// resolveLatestConcreteVersion maps an open-list kernel's "latest" to
// the newest concrete release: the first non-prerelease entry of the
// release list (newest first, drafts already excluded) — the same list
// the version picker shows, matching GitHub's own "latest" semantics.
func resolveLatestConcreteVersion(info kernel.Info) (string, error) {
	items, err := kernelReleaseVersions(info)
	if err != nil {
		return "", err
	}
	for _, it := range items {
		if !it.Prerelease {
			return it.Version, nil
		}
	}
	if len(items) > 0 {
		return items[0].Version, nil
	}
	return "", fmt.Errorf("未获取到 %s 的版本列表", info.Name)
}

// fetchKernelReleaseVersionsWithFallback 是内核版本列表的一次实际
// 抓取：按内置直选路线逐个尝试（F-059 的后继口径，见
// kernelReleaseVersions），结果由调用方回填缓存。
func fetchKernelReleaseVersionsWithFallback(info kernel.Info) ([]kernelVersionItem, error) {
	directURL := fmt.Sprintf("%s/repos/%s/releases?per_page=30", githubAPIBase, info.ReleaseRepo)
	var out []kernelVersionItem
	err := forEachGitHubRoute(githubFetchStageBudget, func(prefix string, attemptTimeout time.Duration) error {
		ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout)
		defer cancel()
		items, err := fetchKernelReleaseVersionsPageCtx(ctx, prefixURL(prefix, directURL), attemptTimeout)
		if err != nil {
			return err
		}
		out = items
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func fetchKernelReleaseVersionsPage(url string) ([]kernelVersionItem, error) {
	return fetchKernelReleaseVersionsPageCtx(context.Background(), url, 30*time.Second)
}

func fetchKernelReleaseVersionsPageCtx(ctx context.Context, url string, clientTimeout time.Duration) ([]kernelVersionItem, error) {
	// X1-403：超时 15s → 30s（经代理多一跳 + 大响应，口径同 U1-006）；
	// 直选尝试时由 clientTimeout 按段预算剩余量收紧。
	// C-02：走共用加固助手（钉死拨号 + 逐跳重定向复检）。
	client := githubVersionFetchClientWithTimeout(clientTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("获取版本列表失败: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取版本列表失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("获取版本列表失败: GitHub 返回 %d", resp.StatusCode)
	}
	// 流式逐条解码、凑够 15 条即停：正确性不再依赖响应体积上限——sing-box 的
	// releases JSON 已达 10MiB 且持续膨胀，任何固定上限迟早被突破（旧 4MiB
	// 上限曾把解码截断成 unexpected EOF，版本列表必然失败）。
	// 256MiB 仅为防异常代理无限灌数据的兜底，正常路径永远触不到。
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 256<<20))
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("解析版本列表失败: %v", err)
	}
	out := make([]kernelVersionItem, 0, 15)
	for len(out) < 15 && decoder.More() {
		var r githubRelease
		if err := decoder.Decode(&r); err != nil {
			return nil, fmt.Errorf("解析版本列表失败: %v", err)
		}
		if r.Draft {
			continue
		}
		v := normalizeKernelVersion(r.TagName)
		if v == "" || v == "latest" {
			continue
		}
		out = append(out, kernelVersionItem{Version: v, Prerelease: r.Prerelease, PublishedAt: r.PublishedAt})
	}
	return out, nil
}

func (h *Handler) kernelVersions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		KernelType string `json:"kernelType"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	info, ok := kernel.Lookup(req.KernelType)
	if !ok || !info.Implemented {
		response.WriteJSON(w, response.ErrDefault("未知内核"))
		return
	}
	items, err := kernelReleaseVersions(info)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// 发现 1：sing-box 的版本列表同时含 fork 构建（1.14.4-flmx1）
	// 与回滚用的上游构建（1.14.4），须在列表里标注来源，否则
	// 两者在下拉框里无法区分、误选即静默降级。回滚路本身保留，
	// 只做显式化。其余内核无 fork 构建，不标注。
	if kernel.EffectiveType(info.Type) == kernel.TypeSingbox {
		for i := range items {
			if kernel.IsForkVersion(items[i].Version) {
				items[i].Source = "fork"
			} else {
				items[i].Source = "upstream"
			}
		}
	}
	response.WriteJSON(w, response.OK(items))
}

// versionBelow reports whether a concrete "x.y[.z]" version is older than
// major.minor; "latest" and unparsable versions are not.
func versionBelow(version string, major, minor int) bool {
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".", 3)
	if len(parts) < 2 {
		return false
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return ma < major || (ma == major && mi < minor)
}

// versionBelow3 是 versionBelow 的三段版（Xray 的 25.9.5 这类下限需要精确到
// 修订号）；"latest" 与不可解析的版本同样不视为过低。四段及以上按前三段
// 比较（与 versionBelowPadded 同口径）：旧实现 SplitN 限 3 段会把第三段
// 拼成 "5.1" 解析失败、对四段串无条件放行，"25.9.4.9" 能绕过 25.9.5 下限。
func versionBelow3(version string, major, minor, patch int) bool {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	if len(parts) < 3 {
		return false
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	pa, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return false
	}
	if ma != major {
		return ma < major
	}
	if mi != minor {
		return mi < minor
	}
	return pa < patch
}

// versionFloorExempt 报告一个版本串是否不携带可判定的具体版本：
// 空串（无版本上下文，见 validateChainVersion 的既有契约）与 "latest"
// 哨兵（用户显式选最新版，normalizeKernelVersion 的落盘形态）。两类
// 在宽松版与严格版下都不视为过低。
func versionFloorExempt(version string) bool {
	v := strings.TrimSpace(version)
	return v == "" || strings.EqualFold(v, "latest")
}

// versionBelowStrict / versionBelow3Strict 是硬门专用的严格变体
// （R2-016/B-sub-03，BC）：宽松版对解析失败与缺段一律「不视为过低」，
// 畸形的具体版本串（"25.9" 缺修订段、"25.5.16-rc1" 段内带后缀——两者
// 都能通过 normalizeKernelVersion 的字符集校验、真实可达）会因此白过
// 能力下限。严格变体要求版本完整解析（所需段数齐全且全为数字）才进
// 入数值比较，解析失败/缺段一律视为低于下限（fail-closed）；两段变
// 体对在席的第三段（语义段）同样校验纯数字，第四段起按发行后缀忽
// 略（R4-06）；豁免集
// 见 versionFloorExempt。消费点全集已逐个分类：各接受-拒绝门、换版
// 扫描、链路校验与渲染能力标志均走严格变体；宽松版仅余两处有意保
// 留——xrayPrivateBlockWarning（纯警告面，R8-005 既定口径）与
// kernelSupportsPostQuantumReality（先取 banner 内首个可解析版本
// token、取不到即 fail-closed，PORT-043）。宽松版本体不动（其语义有既有钉死用例）。
// Fork 例外：三严格变体入口先经 stripForkVersionSuffixForCompare
// 剥 "-flmx<数字>" 后缀（1.14.4-flmx1 按 1.14.4 计），其余后缀
// （如 -rc1）仍原样进入纯数字校验、fail-closed。
// forkVersionSuffixPattern 是严格比较器唯一认可的版本后缀：fork
// 构建的 "-flmx<数字>"（如 1.14.4-flmx1）。口径与 system_upgrade.go
// 的 stripVersionSuffixForCompare（只剥文档化的 "-cand"）同族——
// 只剥白名单后缀，其余后缀一律不动、随后由纯数字段校验 fail-closed
// 拒绝（"-rc1" 仍被拒，见 TestVersionBelowStrictUnits 的 fork 格）。
// 宽松版（versionBelow / versionBelow3 / versionBelowPadded）一字不动。
var forkVersionSuffixPattern = regexp.MustCompile(`^-flmx[0-9A-Za-z]*$`)

// stripForkVersionSuffixForCompare 剥比较用的 fork 版本后缀：
// "1.14.4-flmx1" -> "1.14.4"。非 fork 后缀原样返回，调用方的纯数字
// 段校验随即把它判为低于下限（fail-closed）。
func stripForkVersionSuffixForCompare(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '-'); i >= 0 {
		if forkVersionSuffixPattern.MatchString(v[i:]) {
			return v[:i]
		}
	}
	return v
}

func versionBelowStrict(version string, major, minor int) bool {
	if versionFloorExempt(version) {
		return false
	}
	version = stripForkVersionSuffixForCompare(version)
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	if len(parts) < 2 {
		return true
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return true
	}
	// 第三段是语义段（修订号）而非发行后缀：在席即须同为纯数字，
	// 否则 "1.14.0-rc1" 会被当作 1.14 达标白过两段下限（R4-06，
	// D-F5-01）。第四段起按家族契约忽略（与 versionBelow3Strict /
	// versionBelowPaddedStrict 的「四段按前三段」一致）。
	if len(parts) >= 3 {
		if _, err := strconv.Atoi(parts[2]); err != nil {
			return true
		}
	}
	return ma < major || (ma == major && mi < minor)
}

// versionBelow3Strict 是 versionBelow3 的严格变体；四段及以上仍按
// 前三段比较（与 A-sub-01 修复口径一致）。
func versionBelow3Strict(version string, major, minor, patch int) bool {
	if versionFloorExempt(version) {
		return false
	}
	version = stripForkVersionSuffixForCompare(version)
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	if len(parts) < 3 {
		return true
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	pa, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return true
	}
	if ma != major {
		return ma < major
	}
	if mi != minor {
		return mi < minor
	}
	return pa < patch
}

// kernelVersionGate 是入站对内核版本的硬性要求（单一真源）：入站保存
// （inboundSave）、内核换版（kernelInstall）与下发前复检
// （buildKernelApplyPayload）三处共用同一口径，避免"修一处漏一处"
// （F-021：此前只有保存期查 AnyTLS/ACME 两门，换版与复检均未覆盖）。
// 返回空串表示通过；非空为拒绝原因（不含入站名，调用方按上下文包装）。
// 本门是硬门：自 R2-016 起各下限走严格变体——"latest" 与空串维持既
// 有豁免，畸形/缺段的具体版本串 fail-closed 不再放行（此前按
// versionBelow 宽松语义一律不拦，口径变更属 BC）。
func kernelVersionGate(kernelType, version string, in *kernel.Inbound) string {
	if in.Security.CertMode == kernel.CertModeACME && versionBelowStrict(version, 1, 14) {
		return fmt.Sprintf("自动申请证书需要 sing-box 1.14 及以上，节点安装的是 %s", version)
	}
	if kernelType == kernel.TypeSingbox && in.Protocol == kernel.ProtocolAnyTLS && versionBelowStrict(version, 1, 12) {
		return fmt.Sprintf("AnyTLS 需要 sing-box 1.12 及以上，节点安装的是 %s", version)
	}
	if kernelType == kernel.TypeXray && in.Settings.EncryptionEnabled() && versionBelow3Strict(version, 25, 9, 5) {
		return fmt.Sprintf("VLESS 加密需要 Xray 25.9.5 及以上，节点安装的是 %s", version)
	}
	// 出站 IP 策略版本门（S4）：策略字段随入站一同进入 gate，保存/复检/
	// 换版三处同拦；未知策略字符串也在此拒绝。
	if err := validateOutboundIPVersion(kernelType, version, in.OutboundIPStrategy); err != nil {
		return err.Error()
	}
	return ""
}

// xrayPrivateBlockVersion 是 Xray 内置私网拦截的版本下限：freedom 的
// defaultBlockPrivateRule 自 26.5.9 才引入，此前版本对私网目标直通。
const xrayPrivateBlockVersion = "26.5.9"

// xrayPrivateBlockWarning 是 R8-005 的安装提示：Xray 低于 26.5.9 时
// 没有 freedom 内置私网拦截，经其入站可达节点内网与云元数据地址。
// 这不是 kernelVersionGate 的硬门——硬门会把 ENC 的 25.9.5 下限整个
// 吞掉（任何 <25.9.5 亦 <26.5.9），且剥夺存量旧版节点的全部下发能力，
// 属能力移除级变更；此处只在管理员主动选版的安装面点名提示（经
// envelope warning 通道下发，前端必须展示），文档口径见 faq.md。
// 返回空串表示无需提示（非 Xray / 版本达标 / latest / 不可解析，
// 与 versionBelow3 既有语义一致）。
// singboxUpstreamInstallWarning names an explicit install of a
// non-fork (upstream) sing-box build on the success envelope
// (发现 1)：回滚用的上游构建（如 1.14.4）不含 fork 的入站返回包
// 队列修复（64），200 包突发会丢包。回滚路保留、安装继续，只
// 是回执必须点名——否则换版全程绿灯、用户无从知道节点已离开
// 修好的 fork 版。fork 构建与非 sing-box 内核返回 ""。
func singboxUpstreamInstallWarning(kernelType, version string) string {
	if kernel.EffectiveType(kernelType) != kernel.TypeSingbox {
		return ""
	}
	if versionFloorExempt(version) || kernel.IsForkVersion(version) {
		return ""
	}
	return fmt.Sprintf("警告：已安装上游构建 %s（非 fork 版，无入站返回包队列修复，200 包突发可能丢包）；如需队列修复请安装 fork 版 %s", version, kernel.SingboxForkVersion)
}

func xrayPrivateBlockWarning(kernelType, version string) string {
	if kernelType != kernel.TypeXray || !versionBelow3(version, 26, 5, 9) {
		return ""
	}
	return fmt.Sprintf("警告：Xray %s 低于 %s，没有 freedom 内置私网拦截——经该内核的入站可达节点内网与云元数据地址（169.254.169.254 等）。建议改装 %s 及以上版本；确需使用此版本时，请知悉该节点不具备私网拦截。", version, xrayPrivateBlockVersion, xrayPrivateBlockVersion)
}

// encDialGate 是 R2X-007 的 ENC 拨号侧版本门：链式/分流目标带 VLESS
// 加密时，拨号方内核同样需要 Xray 25.9.5+——ENC 不只是服务端属性。
// 版本比较与入站自有门（kernelVersionGate）同用严格变体
// versionBelow3Strict（R2-016），保持单一口径；保存（validateChainVersion）、
// 下发（buildKernelApplyPayload）与换版扫描（checkKernelVersionGates）
// 三面共用。返回空串表示通过。
func encDialGate(kernelType, version string, e *kernel.Endpoint) string {
	if e == nil || kernelType != kernel.TypeXray || !versionBelow3Strict(version, 25, 9, 5) || !e.HasVLESSEncryption() {
		return ""
	}
	return fmt.Sprintf("VLESS 加密需要 Xray 25.9.5 及以上，节点安装的是 %s", version)
}

// naiveDialGate 是 R9X-005 的 Naive 拨号侧版本门：链式/分流目标为
// Naive（自建入站或导入节点）时，拨号方 sing-box 需要 1.13+——naive
// 出站随 1.13 的 cronet 构建引入，1.12 及以下整单 FATAL。保存
// （validateChainVersion 经 CheckChainWith）、下发
// （buildKernelApplyPayload）与换版扫描（checkKernelVersionGates）
// 三面共用同一口径；返回空串表示通过。
func naiveDialGate(kernelType, version string, e *kernel.Endpoint) string {
	if e == nil || kernelType != kernel.TypeSingbox || !versionBelowStrict(version, 1, 13) || !kernel.IsNaiveEndpoint(*e) {
		return ""
	}
	return fmt.Sprintf("sing-box 1.13 及以上才能连接 Naive 节点，节点安装的是 %s", version)
}

// inboundRowIsNaive reports whether a stored inbound row is a Naive
// target, whether it is a self-built inbound or an imported node.
func (h *Handler) inboundRowIsNaive(target *model.NodeInbound) bool {
	if target == nil {
		return false
	}
	if target.Protocol == kernel.ProtocolNaive {
		return true
	}
	in, err := inboundFromModel(*target)
	if err != nil {
		return false
	}
	return kernel.IsNaiveEndpoint(kernel.Endpoint{Inbound: in, Proxy: importedProxy(target)})
}

// inboundRowHasENC reports whether a stored inbound row carries VLESS
// encryption, whether it is a self-built inbound (Settings) or an
// imported node (proxy in Extra).
func (h *Handler) inboundRowHasENC(target *model.NodeInbound) bool {
	if target == nil {
		return false
	}
	in, err := inboundFromModel(*target)
	if err != nil {
		return false
	}
	return kernel.Endpoint{Inbound: in, Proxy: importedProxy(target)}.HasVLESSEncryption()
}

// checkKernelVersionGates refuses a kernel (re)install whose target version
// would break an existing inbound's version gate（F-021）。
// kernelVersionGateError 是内核版本门的业务拒绝：消息由入站名＋版本门
// 文案构成、不含仓储错误原文，调用方可原样回执；仓储错误仍走通用出口。
type kernelVersionGateError struct{ msg string }

func (e *kernelVersionGateError) Error() string { return e.msg }

func (h *Handler) checkKernelVersionGates(kernelID int64, kernelType, version string) error {
	rows, err := h.repo.ListNodeInboundsByKernel(kernelID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		in, err := inboundFromModel(row)
		if err != nil {
			continue
		}
		if msg := kernelVersionGate(kernel.EffectiveType(kernelType), version, &in); msg != "" {
			return &kernelVersionGateError{msg: fmt.Sprintf("入站 %s 与目标版本不兼容：%s，不能安装 %s", row.Name, msg, version)}
		}
		// R9X-005：经 Naive 节点出站（链式/分流目标）的入站同样要求
		// sing-box 1.13+；目标入站不在本内核上，gate 看不到，须单独
		// 扫描。
		if kernel.EffectiveType(kernelType) == kernel.TypeSingbox && versionBelowStrict(version, 1, 13) {
			for _, id := range outboundTargets(row) {
				if t, err := h.repo.GetNodeInbound(id); err == nil && t != nil && h.inboundRowIsNaive(t) {
					return &kernelVersionGateError{msg: fmt.Sprintf("入站 %s 经 Naive 节点 %s 出站，需要 sing-box 1.13 及以上，不能安装 %s", row.Name, t.Name, version)}
				}
			}
		}
		// R2X-007：经 ENC 节点出站（链式/分流目标）的入站同样要求拨号
		// 内核 Xray 25.9.5+；目标入站不在本内核上，gate 看不到，须单独
		// 扫描。
		if kernelType == kernel.TypeXray && versionBelow3Strict(version, 25, 9, 5) {
			for _, id := range outboundTargets(row) {
				if t, err := h.repo.GetNodeInbound(id); err == nil && t != nil && h.inboundRowHasENC(t) {
					return &kernelVersionGateError{msg: fmt.Sprintf("入站 %s 经 VLESS 加密节点 %s 出站，需要 Xray 25.9.5 及以上，不能安装 %s", row.Name, t.Name, version)}
				}
			}
		}
	}
	return nil
}

// applyKernel pushes the current configuration of a kernel to its node. An
// offline node is not an error for persistence: the kernel stays pending and
// is reconciled when the agent reconnects.
// nftIsolatedAgentMinVersion 是 nft 表按安装隔离的首个 agent 版本
// （F2-NFT-01，9742d6e2）。低于它的 agent 在 KernelApply 时用明文
// 表名 flmx_kernel 重建共享表，会覆盖同机其他 agent 的计数与白名单
// （NEW-V1-01：旧代码 agent 在面板重连重部署时重建共享表）。
// 2.1.13 是 2.1.12 之后的第一个版本号——隔离修复随 2.1.12 之后的首
// 个发布出货，而发布是累积的，所以任何 > 2.1.12 的 agent 版本都含
// 隔离修复；阈值取 2.1.13 与该发布实际编号（2.1.13/2.2.0/…）均成
// 立，因为不存在不含修复的 2.1.13。
const nftIsolatedAgentMinVersion = "2.1.13"

// portProbeAgentMinVersion 是 live 端口探针 CheckPortListening 的首个
// agent 版本（F2-PORT-01，cac080bd，随 2.1.13 出货）。C-04 的暂停
// 验证（NotFound 后验端口实态、成功后验端口已死）依赖该命令；低于
// 它的 agent 走旧自愈重建过渡路径（见 controlForwardServices）。
// 与 nftIsolatedAgentMinVersion 同值是巧合——两者同随 2.1.13 出，
// 各自由自己的引入提交背书，不得合并为一个常量。
const portProbeAgentMinVersion = "2.1.13"

// companionAgentMinVersion 是伴生实例分离（C-03）的首个 agent 版本：
// 伴生 Apply 的 Type "singbox-companion" 须 agent 的 adapters 认得，
// 旧 agent 会报“不支持的内核类型”。暂定 2.1.14——下个发版号定下后
// 复核此常量（与 nftIsolatedAgentMinVersion / portProbeAgentMinVersion
// 同形态，各自由自己的引入版本背书）。
const companionAgentMinVersion = "2.1.14"

// agentSupportsCompanionSplit 判定 agent 版本是否含伴生实例分离
// （纯函数，供单测）：可解析且 >= 2.1.14 为支持；空串与不可解析形态
// 一律按不支持处理（fail-closed——旧 agent 上伴生留在主配置内，
// 行为不变；分裂只发生在新 agent 节点上）。
func agentSupportsCompanionSplit(raw string) bool {
	normalised, ok := parseConnlogAgentVersion(raw)
	if !ok {
		return false
	}
	cmp, err := compareVersions(normalised, companionAgentMinVersion)
	if err != nil {
		return false
	}
	return cmp >= 0
}

// companionSplitPendingReason reports why the companion split is
// not active on a node that has enabled companions (E-02): the
// agent version is known but below companionAgentMinVersion, so
// companions stay on the main kernel (fail-closed, behavior
// unchanged) with no visible trace anywhere. Empty means nothing
// to report — split active, no enabled companions, or the version
// is unknown (offline / never reported) so no version claim can
// be made.
func (h *Handler) companionSplitPendingReason(nodeID int64) string {
	if _, active := h.companionKernelIDForNode(nodeID); active {
		return ""
	}
	node, err := h.repo.GetNodeByID(nodeID)
	if err != nil || node == nil {
		return ""
	}
	raw := strings.TrimSpace(node.Version.String)
	if _, ok := parseConnlogAgentVersion(raw); !ok {
		return ""
	}
	if agentSupportsCompanionSplit(raw) {
		return ""
	}
	comps, err := h.repo.ListCompanionInbounds()
	if err != nil {
		return ""
	}
	for i := range comps {
		if comps[i].NodeID == nodeID && comps[i].Enabled == 1 {
			return fmt.Sprintf("agent 版本 %s 未到 %s，伴生计量仍在主内核运行；请先升级 agent，升级后重连时分离将自动生效", raw, companionAgentMinVersion)
		}
	}
	return ""
}

// companionKernelIDForNode returns the companion NodeKernel row id for a
// node, if the split is active there (the row exists). The split is
// active iff the row exists — that is the single source of truth, so
// createCompanion, reconcileSSMetering and the migration all agree.
func (h *Handler) companionKernelIDForNode(nodeID int64) (int64, bool) {
	k, err := h.repo.GetNodeKernelByType(nodeID, kernel.TypeSingboxCompanion)
	if err != nil || k == nil {
		return 0, false
	}
	return k.ID, true
}

// syncCompanionKernelVersion 把伴生行的版本登记同步到目标版本（主核
// 版本）：伴生行与主核跑同一二进制，版本登记不一致即意味着伴生实例
// 仍在跑旧构建。只写登记，不下发；下发由调用方（kernelInstall 的
// 换版收尾、reconcile 的 apply 循环）完成。latest/空串不参与同步——
// 收敛发生在 apply 入口。
func (h *Handler) syncCompanionKernelVersion(nodeID int64, version string) {
	if version == "" || versionFloorExempt(version) {
		return
	}
	comp, err := h.repo.GetNodeKernelByType(nodeID, kernel.TypeSingboxCompanion)
	if err != nil || comp == nil || comp.Version == version {
		return
	}
	if err := h.repo.UpdateNodeKernelFields(comp.ID, map[string]interface{}{
		"version": version, "updated_time": time.Now().UnixMilli(),
	}); err != nil {
		log.Printf("companion split: sync companion version node_id=%d: %v", nodeID, err)
	}
}

// ensureCompanionKernel performs the one-time companion split for a node:
// when the node has enabled companions and its agent understands the
// companion instance, create the companion NodeKernel row (version tracks
// the node's main sing-box kernel) and move every companion inbound's
// KernelID onto it. It only does DB work; the caller's apply loop pushes
// main first (frees the ports) then companion (binds them) —
// ListNodeKernels orders id ASC, and the companion row is newer.
// Old agents never get the row: companions stay on the main kernel and
// behave exactly as before. Idempotent: a second call finds the row and
// only moves stragglers.
func (h *Handler) ensureCompanionKernel(nodeID int64) {
	node, err := h.repo.GetNodeByID(nodeID)
	if err != nil || node == nil {
		return
	}
	if !agentSupportsCompanionSplit(node.Version.String) {
		return
	}
	if _, active := h.companionKernelIDForNode(nodeID); active {
		// Fork 伴生同步：主核换版后伴生行须同升——只在创建时快照主核
		// 版本会留一个跑旧二进制（旧队列 64）的伴生实例。行已存在
		// 时同样同步版本，实际下发由调用方的 apply 循环（或
		// kernelInstall 的换版收尾）完成。
		if main, err := h.repo.GetNodeKernelByType(nodeID, kernel.TypeSingbox); err == nil && main != nil {
			h.syncCompanionKernelVersion(nodeID, main.Version)
		}
		h.moveCompanionsToKernel(nodeID)
		return
	}
	companions, err := h.repo.ListCompanionInbounds()
	if err != nil {
		log.Printf("companion split: list companions node_id=%d: %v", nodeID, err)
		return
	}
	var nodeCompanions []model.NodeInbound
	for _, c := range companions {
		if c.NodeID == nodeID && c.Enabled == 1 {
			nodeCompanions = append(nodeCompanions, c)
		}
	}
	if len(nodeCompanions) == 0 {
		return
	}
	main, err := h.repo.GetNodeKernelByType(nodeID, kernel.TypeSingbox)
	if err != nil || main == nil {
		log.Printf("companion split: no main sing-box kernel node_id=%d", nodeID)
		return
	}
	now := time.Now().UnixMilli()
	comp := &model.NodeKernel{
		NodeID:      nodeID,
		KernelType:  kernel.TypeSingboxCompanion,
		Version:     main.Version,
		Status:      kernelStatusPending,
		Enabled:     1,
		CreatedTime: now,
		UpdatedTime: now,
	}
	if err := h.repo.CreateNodeKernel(comp); err != nil {
		log.Printf("companion split: create companion kernel node_id=%d: %v", nodeID, err)
		return
	}
	h.moveCompanionsToKernel(nodeID)
	log.Printf("companion split: node_id=%d companion kernel %d created, %d companion(s) moved", nodeID, comp.ID, len(nodeCompanions))
}

// moveCompanionsToKernel moves every companion inbound on the node onto
// the companion kernel row (no-op when the split is not active).
func (h *Handler) moveCompanionsToKernel(nodeID int64) {
	compID, active := h.companionKernelIDForNode(nodeID)
	if !active {
		return
	}
	companions, err := h.repo.ListCompanionInbounds()
	if err != nil {
		log.Printf("companion split: list companions for move node_id=%d: %v", nodeID, err)
		return
	}
	now := time.Now().UnixMilli()
	for _, c := range companions {
		if c.NodeID != nodeID || c.KernelID == compID {
			continue
		}
		updated := c
		updated.KernelID = compID
		updated.UpdatedTime = now
		if err := h.repo.SaveNodeInbound(&updated); err != nil {
			log.Printf("companion split: move companion %d node_id=%d: %v", c.ID, nodeID, err)
		}
	}
}

// removeCompanionKernel removes the companion kernel alongside a deleted
// main sing-box kernel (see the remove flow). No-op for other kernel
// types and when no companion row exists.
func (h *Handler) removeCompanionKernel(k *model.NodeKernel) {
	if k == nil || kernel.CanonicalKernelType(k.KernelType) != kernel.TypeSingbox {
		return
	}
	comp, err := h.repo.GetNodeKernelByType(k.NodeID, kernel.TypeSingboxCompanion)
	if err != nil || comp == nil {
		return
	}
	unlock := lockKernelApply(comp.ID)
	_, dispatchErr := h.sendNodeCommandWithTimeout(k.NodeID, "KernelControl", map[string]interface{}{
		"type": kernel.TypeSingboxCompanion, "action": "remove",
	}, kernelCommandTimeout, false, true)
	unlock()
	if derr := h.repo.DeleteNodeKernel(comp.ID); derr != nil {
		log.Printf("companion split: delete companion kernel %d after main remove: %v", comp.ID, derr)
		return
	}
	if dispatchErr != nil {
		if merr := h.repo.MarkKernelOrphanRecheck(k.NodeID, kernel.TypeSingboxCompanion, time.Now().UnixMilli()); merr != nil {
			log.Printf("companion split: orphan recheck mark failed node_id=%d kernel=%s err=%v", k.NodeID, kernel.TypeSingboxCompanion, merr)
		}
	}
}

// agentSupportsPortProbe 判定 agent 版本是否含 CheckPortListening
// （纯函数，供单测）：可解析且 >= 2.1.13 为支持；空串与不可解析形态
// （dev 等）一律按不支持处理（fail-closed，与 agentSupportsNftIsolation
// 同口径）。
func agentSupportsPortProbe(raw string) bool {
	normalised, ok := parseConnlogAgentVersion(raw)
	if !ok {
		return false
	}
	cmp, err := compareVersions(normalised, portProbeAgentMinVersion)
	if err != nil {
		return false
	}
	return cmp >= 0
}

// agentSupportsNftIsolation 判定 agent 版本是否含 nft 表隔离（纯
// 函数，供单测）：可解析且 >= 2.1.13 为支持；空串与不可解析形态
// （dev 等）一律按不支持处理（fail-closed，与 connlogAgentSupport
// 同口径）。调用方 requireNftIsolatedAgent 对空串（从未上报版本
// 的离线节点）另行放行，由发送侧以“不在线”自然失败。
func agentSupportsNftIsolation(raw string) bool {
	normalised, ok := parseConnlogAgentVersion(raw)
	if !ok {
		return false
	}
	cmp, err := compareVersions(normalised, nftIsolatedAgentMinVersion)
	if err != nil {
		return false
	}
	return cmp >= 0
}

// requireNftIsolatedAgent 在内核下发前核对节点 agent 版本
// （NEW-V1-01 过渡期门）：旧版 agent 的 KernelApply 会用明文表名
// 重建共享 flmx_kernel 表，覆盖同机其他 agent 的计数与白名单。
// 版本过旧时中止下发并点名升级（与自动卸载/定时重启的版本门同
// 口径）；agent 升级后重连即自动重推。空串版本（从未上报——节点
// 离线或未连接）不设此门，下发在发送侧以“不在线”失败，与既往
// 口径一致。
func (h *Handler) requireNftIsolatedAgent(nodeID int64) error {
	node, err := h.repo.GetNodeByID(nodeID)
	if err != nil {
		return err
	}
	if node == nil {
		return errors.New("节点不存在")
	}
	raw := strings.TrimSpace(node.Version.String)
	if raw == "" {
		return nil
	}
	if !agentSupportsNftIsolation(raw) {
		return fmt.Errorf("agent 版本过旧（当前 %s，需 %s 或更高），内核下发会覆盖同机其他 agent 的 nft 表；请先升级 agent，升级后重连时将自动重推", raw, nftIsolatedAgentMinVersion)
	}
	return nil
}

// kernelApplyVersionMismatch 是 F3 的安装面实测版本校验（纯函数，
// 供单测）：安装下发（install=true）回执后，agent 上报的实测版本
// 必须包含请求版本，否则 fork 构建输出 "dev" 也会静默报成功。
// 只约束 sing-box 同族（fork 落地对象）；其余内核与非安装下发
// （配置重推/对账）不在此校验内。reported 为空同样判不一致——
// 实测版本探不到时不得按成功确认。返回空串表示通过。
// 上游请求口径（发现 1）：请求上游版（如 1.14.4）时实测含请求串
// 即通过，fork 实测同样通过（fork 满足上游请求）；上游构建本身
// 的告警不在此函数内——F3 返回非空即判安装失败，警告不能走这
// 条通道，由安装回执的 singboxUpstreamInstallWarning 负责点名。
func kernelApplyVersionMismatch(kernelType, requested, reported string) string {
	if kernel.EffectiveType(kernelType) != kernel.TypeSingbox {
		return ""
	}
	if versionFloorExempt(requested) {
		return ""
	}
	if reported == "" {
		return fmt.Sprintf("内核实测版本为空，与请求版本 %s 不一致，安装未确认", requested)
	}
	if strings.Contains(reported, requested) {
		return ""
	}
	return fmt.Sprintf("内核实测版本 %q 与请求版本 %s 不一致（疑似错误构建），安装未确认", reported, requested)
}

func (h *Handler) applyKernel(kernelID int64, install bool) error {
	return h.applyKernelWith(kernelID, install, nil)
}

func (h *Handler) applyKernelWith(kernelID int64, install bool, realmOverride map[int64]bool) error {
	return h.applyKernelWithMode(kernelID, install, realmOverride, true)
}

// applyKernelWithMode 是 applyKernelWith 的实现。settleCounters=false
// 仅供 reconcileNodeKernels 轮内下发使用：轮尾的 pollNodeKernels 本
// 就摄入计数钉基线，且 R4X-009 以「每轮 reconcile 恰一次
// KernelStatus」为轮次正典计量（backup_r4_b5_test），轮内逐下发
// settle 会打散该计数。独立下发（创建/更新/暂停恢复/订阅推送/
// drift 补推等）不在轮次计量内，逐个 settle（调用点注释详述）。
func (h *Handler) applyKernelWithMode(kernelID int64, install bool, realmOverride map[int64]bool, settleCounters bool) error {
	unlock := lockKernelApply(kernelID)
	defer unlock()

	k, err := h.repo.GetNodeKernel(kernelID)
	if err != nil {
		// RAW291（路 3）：仓储直通错误标 repoError——applyKernel 的
		// 调用方（如订阅保存/轮换的「下发失败」Msg）凭此标记把仓
		// 储原文换成固定文案；数据面下发错误不经此标记、原样回。
		// 标记只改错误类型、Error() 文本不变，其余调用方行为不变。
		return markRepoError(err)
	}
	if k == nil {
		return errors.New("内核不存在")
	}
	// NEW-V1-01：过渡期门——旧版 agent 的 KernelApply 会用明文表名
	// 重建共享 flmx_kernel 表。版本过旧时中止下发（不改任何状态，
	// 门在“latest”归一等写操作之前），并点名升级。
	if err := h.requireNftIsolatedAgent(k.NodeID); err != nil {
		return err
	}
	// R2-F2-A2: rows stored before "latest" was resolved at install time
	// still carry the literal sentinel. Converge them here, at the
	// dispatch entry: resolve via the release list and persist the
	// concrete version so the payload (and every later apply) names a
	// real version. A failed lookup keeps "latest" for this apply —
	// config pushes must not break because GitHub is unreachable — and
	// is logged by name; kernelInstall remains the loud surface.
	if versionFloorExempt(k.Version) && k.Version != "" {
		// F5：sing-box 的存量 "latest" 行钉死到已核实的 fork 版，不再
		// 跟随 fork 仓的 release 列表漂移；其余内核仍走 release 列表。
		if kernel.EffectiveType(k.KernelType) == kernel.TypeSingbox {
			if uerr := h.repo.UpdateNodeKernelFields(k.ID, map[string]interface{}{
				"version": kernel.SingboxForkVersion, "updated_time": time.Now().UnixMilli(),
			}); uerr == nil {
				k.Version = kernel.SingboxForkVersion
			}
		} else if info, ok := kernel.Lookup(k.KernelType); ok && len(info.Versions) == 0 {
			if concrete, rerr := resolveLatestConcreteVersion(info); rerr == nil {
				if uerr := h.repo.UpdateNodeKernelFields(k.ID, map[string]interface{}{
					"version": concrete, "updated_time": time.Now().UnixMilli(),
				}); uerr == nil {
					k.Version = concrete
				}
			} else {
				log.Printf("kernel apply: resolve latest for kernel_id=%d type=%s failed, keeping %q: %v", k.ID, k.KernelType, k.Version, rerr)
			}
		}
	}
	payload, hash, err := h.buildKernelApplyPayloadWith(k, realmOverride)
	now := time.Now().UnixMilli()
	if err != nil {
		h.observeKernelStatusTransition(*k, kernelStatusError, err.Error())
		_ = h.repo.UpdateNodeKernelFields(k.ID, map[string]interface{}{
			"status": kernelStatusError, "last_error": err.Error(), "updated_time": now,
		})
		return err
	}
	_ = h.repo.UpdateNodeKernelFields(k.ID, map[string]interface{}{"config_hash": hash, "updated_time": now})

	timeout := kernelCommandTimeout
	if install || k.InstalledVersion == "" || k.Version != "latest" && !strings.Contains(k.InstalledVersion, k.Version) {
		timeout = kernelApplyTimeout
	}
	res, err := h.sendNodeCommandWithTimeout(k.NodeID, "KernelApply", payload, timeout, false, false)
	now = time.Now().UnixMilli()
	if err != nil {
		// F2-APPLY-01：回执超时不等于下发失败——命令已发出，慢链路
		// 上 agent 可能已落地、只是回执没赶上预算。先向 agent 核实
		// 再定性，核实到落地按成功收尾，核实不到置 pending 待下次
		// poll 收敛；两种情形都不写 error、不报「下发失败」。
		if isKernelApplyTimeoutError(err) {
			if h.verifyKernelApplyLanded(k, hash) {
				// 落地已证实：poll 已把状态/applied_hash 落库，
				// 此处只补成功路径的计数基线钉定。
				if settleCounters && h.importTailBatchDepth.Load() == 0 {
					h.settleKernelCountersAfterApply(k.NodeID)
				}
				return nil
			}
			h.observeKernelStatusTransition(*k, kernelStatusPending, errApplyConfirmPending.Error())
			_ = h.repo.UpdateNodeKernelFields(k.ID, map[string]interface{}{
				"status": kernelStatusPending, "last_error": errApplyConfirmPending.Error(), "updated_time": now,
			})
			return errApplyConfirmPending
		}
		status := kernelStatusError
		if strings.Contains(err.Error(), "不在线") {
			status = kernelStatusPending
		}
		h.observeKernelStatusTransition(*k, status, err.Error())
		_ = h.repo.UpdateNodeKernelFields(k.ID, map[string]interface{}{
			"status": status, "last_error": err.Error(), "updated_time": now,
		})
		return err
	}

	var state agentKernelState
	_ = remarshal(res.Data, &state)
	// F3：安装下发回执后校验实测版本——fork 构建若输出 "dev"（或
	// 旧版二进制）须在此明错，不写 installed_version、不确认成功；
	// kernelInstall 的失败分支随即把版本登记回滚（见
	// rollbackKernelInstallAfterFailure：本路径落 error 态即回滚）。
	if install {
		if msg := kernelApplyVersionMismatch(k.KernelType, k.Version, state.Version); msg != "" {
			h.observeKernelStatusTransition(*k, kernelStatusError, msg)
			_ = h.repo.UpdateNodeKernelFields(k.ID, map[string]interface{}{
				"status": kernelStatusError, "last_error": msg, "updated_time": now,
			})
			return errors.New(msg)
		}
	}
	newStatus := kernelStateStatus(state, k.Enabled == 1)
	h.observeKernelStatusTransition(*k, newStatus, state.Error)
	fields := map[string]interface{}{
		"applied_hash": hash,
		"status":       newStatus,
		"last_error":   state.Error,
		"updated_time": now,
	}
	if state.Version != "" {
		fields["installed_version"] = truncate(state.Version, 100)
	}
	if err := h.repo.UpdateNodeKernelFields(k.ID, fields); err != nil {
		return err
	}
	// FIX-BASELINE（R2-E2-01）：下发成功后立即拉一次计数面，把计
	// 数基线钉在下发时刻。agent 侧累计计数自本次下发重建 nft 表
	// 起算，而入账按「样本 − 已存基线」差分、首个样本只建基线不
	// 入账：若不主动拉，基线只能由 60s 周期轮询的首个样本建立，
	// 下发后至首轮询前产生的全部流量被基线整段吞掉、永不入账
	// （真机 EXEC-RERUN3：新建 realm 转发立即发流量，180s 窗内
	// forward 流量恒 0→0；gost 无基线语义、同轮 0→54 正常）。
	// FIX-REALM-METER 已在 realm（Relay）面以本点为唯一漏斗修复
	// 同一机制；入站内核（xray/singbox）的入站计数与订阅用户计
	// 数同乘 KernelStatus 计数面、同为首样本基线语义，本轮扩面
	// 到全部内核。
	// 与 R4X-009 轮次口径的共存设计（本修复的钉死约束）：settle
	// 只走计数面（ingestKernelCountersMode 的 settle 口径：纯计数
	// 入账，不带订阅同步/执法尾——两者在 apply 锁内会自锁），不走全量
	// pollNodeKernels——状态对账含 drift 重下发与孤儿清理，下发
	// 后立刻全量对账会与 drift 互相触发成无界下发-轮询链（realm
	// v1 实证），计数面不发任何出站命令、不成链；reconcile 轮内
	// 下发由 settleCounters=false 豁免，轮次保持「恰一次
	// KernelStatus（轮尾 poll）」的正典计量不变，轮内基线由轮尾
	// poll 的计数摄入钉定（窗口以下发→轮尾 poll 的时长为界，与
	// drift 补推的既有残余同量级）。
	// settle 失败静默：周期轮询随后补建基线，退化为原行为，不
	// 阻塞下发回执。
	// 导入批次豁免（与轮内豁免同构）：备份导入尾钩子执行期间的
	// apply 属于导入这一次物化事件，基线由导入 reconcile 轮的
	// 尾随 poll 钉定；逐个 settle 会被 R4X-009 轮次口径计为额外
	// 轮次（FIX-BASELINE 实测全量导入 3 轮≠1）。非物化导入（如
	// users-only）的收敛性 apply 在批次内同样抑制——保持原行为
	// （基线由次个周期轮询钉定），不引入新窗口。
	if settleCounters && h.importTailBatchDepth.Load() == 0 {
		h.settleKernelCountersAfterApply(k.NodeID)
	}
	return nil
}

func kernelStateStatus(state agentKernelState, enabled bool) string {
	switch {
	case state.Running:
		return kernelStatusRunning
	case !enabled:
		return kernelStatusStopped
	case state.Error != "":
		return kernelStatusError
	default:
		return kernelStatusStopped
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// reconcileNodeKernels re-applies every kernel of a node, e.g. after the agent
// reconnects. The agent skips work when the config hash is unchanged.
func (h *Handler) reconcileNodeKernels(nodeID int64) {
	// C-03: split the companions onto their own instance before the
	// apply loop lists the kernels, so this round already pushes main
	// first (frees the ports) then companion (binds them).
	h.ensureCompanionKernel(nodeID)
	kernels, err := h.repo.ListNodeKernels(nodeID)
	if err != nil {
		// R18-B9-03：读内核列表失败不得静默 return——调用方
		// （导入尾钩子等）无从察觉本轮重推整轮丢弃，在线节点
		// 将静默跑旧内核配置至重连。失败只记日志（本函数无回执
		// 通道，逐内核失败同口径）。
		log.Printf("kernel reconcile failed node_id=%d list kernels err=%v", nodeID, err)
		return
	}
	for _, k := range kernels {
		// settleCounters=false：本轮尾随的 pollNodeKernels 会摄入
		// 计数钉基线，轮内不再逐下发 settle——R4X-009 以「每轮
		// reconcile 恰一次 KernelStatus」计轮次（backup_r4_b5_test），
		// 逐下发 settle 会把轮次计数打散。窗口与口径见
		// applyKernelWithMode 调用点注释（FIX-BASELINE）。
		if err := h.applyKernelWithMode(k.ID, false, nil, false); err != nil {
			log.Printf("kernel reconcile failed node_id=%d kernel=%s err=%v", nodeID, k.KernelType, err)
		}
	}
	// Also runs when the panel has no kernel left on the node, so kernels
	// deleted while it was offline get removed.
	h.pollNodeKernels(nodeID, true)
}

// kernelReconcileFailure 是单内核重下发的失败（供同步回执点名）。
type kernelReconcileFailure struct {
	KernelType string
	Err        error
}

// reconcileNodeKernelsCollect 与 reconcileNodeKernels 同构，但
// 收集每内核的下发失败并返回（供白名单开关翻转的同步回执用）：
// install=true 只把面板等待放宽到 kernelApplyTimeout（5 分钟，
// 覆盖 agent 侧的 nft 自动安装耗时），不强制重装。离线/不在线
// 类失败由调用方过滤（见 nodeUpdate 的白名单回执），只把真失败
// 点名进回执。
func (h *Handler) reconcileNodeKernelsCollect(nodeID int64) []kernelReconcileFailure {
	// C-03: same split hook as reconcileNodeKernels (see there).
	h.ensureCompanionKernel(nodeID)
	kernels, err := h.repo.ListNodeKernels(nodeID)
	if err != nil {
		return []kernelReconcileFailure{{KernelType: "", Err: err}}
	}
	var failures []kernelReconcileFailure
	for _, k := range kernels {
		if err := h.applyKernelWithMode(k.ID, true, nil, false); err != nil {
			failures = append(failures, kernelReconcileFailure{KernelType: k.KernelType, Err: err})
		}
	}
	// 与 reconcileNodeKernels 同口径收尾：轮询摄入计数与 nftables
	// 状态，节点响应随即带上最新 nftablesStatus。
	h.pollNodeKernels(nodeID, true)
	return failures
}

// refreshNodeKernelStatus polls one node for kernel state and traffic.
func (h *Handler) refreshNodeKernelStatus(nodeID int64) {
	h.pollNodeKernels(nodeID, false)
}

// kernelPollOutcome 是一轮 pollNodeKernels 的收敛判据（FIX-CASCADE
// / E1-09 子形态 b）：Reachable 表示 agent 的 KernelStatus 回读已
// 取得；OrphanTypes 是 agent 仍在运行、但 DB 已无对应行的内核类
// 型中「本轮下发移除未确认」的子集——两者同时满足（可达且子集
// 为空）才证明 agent 侧已收敛到 DB 空集，孤儿重查标记可清除。
type kernelPollOutcome struct {
	Reachable   bool
	OrphanTypes []string
}

// observeKernelStatusTransition 是内核状态跃迁的通知判定唯一漏斗：
// 进入 error 时发 kernel_error（发送失败回滚置位、留待下一次跃迁判
// 定重试，X-P2-8），离开 error 时清问题指纹重新武装（A7-02）。
//
// 判定必须覆盖全部落状态面，而非只有 poll 摄入：applyKernelWithMode
// 的三条落状态路径（构建失败／发送失败／回执状态）若直写 error，
// 跃迁会被提前「消费」——其后 poll 摄入时 DB 已是 error、永不跃
// 迁，告警永久丢失（INV-NOTIFY-KERNELERR，真机 M-0160 三触发之
// (a) 同步安装失败、(b) 恢复→再坏均未得事件）。故 poll 与 apply
// 两面在落状态前都先过本判定，prevStatus 以落状态方读到的现行行
// 状态为准；判定本身不写状态，落状态仍由各调用方按原口径执行。
func (h *Handler) observeKernelStatusTransition(k model.NodeKernel, newStatus, errText string) {
	if newStatus == kernelStatusError && k.Status != kernelStatusError {
		msg := fmt.Sprintf("节点：%s\n内核：%s", h.nodeLabel(k.NodeID), k.KernelType)
		if errText != "" {
			msg += "\n错误：" + truncate(errText, 300)
		}
		kernelID, prevStatus := k.ID, k.Status
		h.notifyTracked(notifyEventKernelError, "内核运行出错", msg, func(outcome notifyOutcome) {
			// X-P2-8（A7-03 同族）：发送失败回滚 error 置位。调用方随后
			// 会把状态落为 error，若不回滚，下一轮 KernelStatus 因
			// 「已是 error」永不再触发、告警永久丢失（旧实现为裸 notify
			// 无条件落状态）。仅当 DB 仍为 error 时回滚为触发前状态，
			// 期间已恢复/被改状态则不覆盖。
			if outcome != notifyOutcomeFailed || h.repo == nil {
				return
			}
			if cur, err := h.repo.GetNodeKernel(kernelID); err == nil && cur != nil && cur.Status == kernelStatusError {
				_ = h.repo.UpdateNodeKernelFields(kernelID, map[string]interface{}{"status": prevStatus})
			}
		})
	}
	if newStatus != kernelStatusError && k.Status == kernelStatusError && h.notifier != nil {
		// A7-02：内核恢复没有恢复通知可挂——直接清掉该内核的问题
		// 指纹，否则 30 分钟窗口内再次出错会被指纹吞掉（同一问题
		// 只提醒一次以「恢复后重新武装」为前提）。
		h.notifier.forgetFingerprintPrefix(notifyEventKernelError + "\x00内核运行出错\x00" +
			fmt.Sprintf("节点：%s\n内核：%s", h.nodeLabel(k.NodeID), k.KernelType))
	}
}

func (h *Handler) pollNodeKernels(nodeID int64, evenIfNone bool) kernelPollOutcome {
	kernels, err := h.repo.ListNodeKernels(nodeID)
	if err != nil || (len(kernels) == 0 && !evenIfNone) {
		return kernelPollOutcome{}
	}
	res, err := h.sendNodeCommandWithTimeout(nodeID, "KernelStatus", map[string]interface{}{}, kernelCommandTimeout, false, false)
	if err != nil {
		return kernelPollOutcome{} // offline, or an agent without kernel support
	}
	var status agentKernelStatus
	if err := remarshal(res.Data, &status); err != nil {
		log.Printf("kernel status decode failed node_id=%d err=%v", nodeID, err)
		return kernelPollOutcome{}
	}
	outcome := kernelPollOutcome{Reachable: true}

	now := time.Now().UnixMilli()
	// B-NEW（入站行计数器 epoch/基线时序丢增量）：计数面紧贴状态
	// 解码后立即入账，再跑内核状态对账（重启探测/孤儿清理/drift
	// 补推）。旧序把入账放在对账尾：孤儿清理每条 KernelControl
	// 可吃满 20s 超时，apply→首样本窗口被拉大；入账只依赖
	// status+now，与状态面对账无数据依赖，移到此处不改变对账
	// 语义（对账读同一份 status；补推是异步 go 程，基线由下一轮
	// 询钉定，见 FIX-BASELINE 注释）。
	h.ingestKernelCounters(nodeID, status, now)
	h.ingestNodeNftablesStatus(nodeID, status)
	// R3A-NEW-1/C NEW-1：agent 计数表丢失的 WS 面——表丢时 agent
	// 的 ConfigHash 不变，drift 对账看不出；ingest 又静默跳过空
	// 计数，故此处单独处理：记一轮采集失败（与 SSH 路共用阈值＋
	// 告警，见 nftables_traffic.go 的来源前缀口径），并把已确认
	// 的内核重推一次（agent Apply 即 content-verified sync，缺表
	// 即重建；agent 自身的 60 秒自检环是第一层，此处是面板侧第
	// 二层）。读错消失时由 recordNftWsCollectSuccess 只清 WS 纹
	// 的 streak，SSH 路的 streak 不受影响。R4C-02：NftNotInstalled
	//（从未装 nft）优先于 NftReadError——该信号不记失败、不重
	// 推、不告警，只走成功分支清 WS streak（若有）。
	if status.NftReadError != "" && !status.NftNotInstalled {
		h.recordNftCollectFailure(nodeID, errors.New(nftWsReadErrorPrefix+status.NftReadError))
		log.Printf("kernel nft table unreadable node_id=%d err=%v (counters frozen; re-applying kernels)", nodeID, status.NftReadError)
		for _, k := range kernels {
			if k.AppliedHash != k.ConfigHash || k.ConfigHash == "" {
				continue
			}
			go func(id int64) {
				if err := h.applyKernelWithMode(id, false, nil, false); err != nil {
					log.Printf("kernel nft table re-apply failed kernel_id=%d err=%v", id, err)
				}
			}(k.ID)
		}
	} else {
		h.recordNftWsCollectSuccess(nodeID, now)
	}
	byType := make(map[string]agentKernelState, len(status.Kernels))
	for _, s := range status.Kernels {
		byType[s.Type] = s
	}
	known := make(map[string]struct{}, len(kernels))
	for _, k := range kernels {
		known[k.KernelType] = struct{}{}
	}
	// R1-B-04：内核在节点保持在线时被 agent 单独重启（崩溃自恢复、
	// KernelControl 手动重启）不经 onNodeOnline，状态轮询是唯一能
	// 看见的面——按 DB-3 同口径排一次定向重测，不必等下个 3 分钟
	// 定时轮。与 onNodeOnline 的排队在静默合并队列里自然去重。
	restarted := false
	for _, k := range kernels {
		if s, ok := byType[k.KernelType]; ok && h.noteKernelStart(nodeID, s) {
			restarted = true
		}
	}
	if restarted {
		h.scheduleNodeReconnectProbe(nodeID)
	}
	for t := range byType {
		if _, ok := known[t]; ok {
			continue
		}
		// Deleted from the panel while the node was offline: the agent
		// restored it from its local state, so remove it now.
		if _, err := h.sendNodeCommandWithTimeout(nodeID, "KernelControl", map[string]interface{}{
			"type": t, "action": "remove",
		}, kernelCommandTimeout, false, true); err != nil {
			log.Printf("kernel orphan removal failed node_id=%d kernel=%s err=%v", nodeID, t, err)
			outcome.OrphanTypes = append(outcome.OrphanTypes, t)
		}
	}
	for _, k := range kernels {
		s, ok := byType[k.KernelType]
		if !ok || s.ConfigHash != k.ConfigHash {
			// The agent lost a config it had acknowledged (reinstall, state
			// wipe): push it again. A failed apply is left for the user or
			// the next reconnect instead of retrying a download every poll.
			if k.AppliedHash != k.ConfigHash || k.ConfigHash == "" {
				continue
			}
			go func(id int64) {
				// FIX-BASELINE：本补推由 poll 摄入自身派生、处于
				// poll 收敛环内，基线由 poll 循环钉定（下一轮询的
				// 计数摄入），故走 settleCounters=false：若在此
				// settle，多发的 KernelStatus 会被 R4X-009 轮次
				// 口径计为额外 reconcile 轮（导入全量格实测），
				// 且摄入路径异步再入状态面与「每轮询至多一次补
				// 推」的既有上界相悖。本路径残余窗口（补推→下轮
				// 询 ≤60s）与修复前同量级，不扩大。
				if err := h.applyKernelWithMode(id, false, nil, false); err != nil {
					log.Printf("kernel drift re-apply failed kernel_id=%d err=%v", id, err)
				}
			}(k.ID)
			continue
		}
		newStatus := kernelStateStatus(s, k.Enabled == 1)
		h.observeKernelStatusTransition(k, newStatus, s.Error)
		fields := map[string]interface{}{
			"status":       newStatus,
			"last_error":   s.Error,
			"applied_hash": s.ConfigHash,
			"updated_time": now,
		}
		if s.Version != "" {
			fields["installed_version"] = truncate(s.Version, 100)
		}
		_ = h.repo.UpdateNodeKernelFields(k.ID, fields)
	}

	return outcome
}

// ingestNodeNftablesStatus 把 agent 上报的 nftables 运行态写进
// 节点行（供节点页白名单区展示）：NftError 非空 → failed（原因
// 原文）；NftReadError 非空 → failed（agent 计数表丢/不可读，
// R3A-NEW-1：与安装失败分开，但同样是“表不在”的明示）；
// NftNotInstalled 优先于 NftReadError（R4C-02：从未装 nft 不是
// 失败，落到默认分支的 missing）；NftAutoInstalled →
// auto_installed；NftAvailable → installed；三者皆无 → 未知
// （""，不写——从不把“没上报”当成失败）。只在状态变化时写，
// 避免每分钟轮询写 churn。写失败只记日志，绝不让状态面摄入失
// 败（best-effort）。
func (h *Handler) ingestNodeNftablesStatus(nodeID int64, status agentKernelStatus) {
	node, err := h.repo.GetNodeByID(nodeID)
	if err != nil || node == nil {
		return
	}
	var state, errText string
	switch {
	case status.NftError != "":
		state, errText = "failed", status.NftError
	case status.NftReadError != "" && !status.NftNotInstalled:
		state, errText = "failed", status.NftReadError
	case status.NftAutoInstalled:
		state, errText = "auto_installed", ""
	case status.NftAvailable:
		state, errText = "installed", ""
	default:
		// A-低-6：agent 已明确上报本机无 nftables（NftAvailable=false
		// 且无错误、未自动安装）。nftables 模式节点的订阅/realm 计数
		// 全靠 nft 计数器，本态即计数缺口的明示——行内记 "missing"，
		// 节点页据此解释「计数永不入账」。gost 模式节点无 nft 是正常
		// 形态，其计费走 WS 上报，不记此态（否则满屏噪音）。
		if defaultNodeForwardMode(node.ForwardMode) != "nftables" {
			return
		}
		state, errText = "missing", "未安装 nftables，订阅／realm 计数不入账"
	}
	if node.NftablesStatus == state && node.NftablesError == errText {
		return
	}
	if err := h.repo.UpdateNodeNftablesStatus(nodeID, state, errText); err != nil {
		log.Printf("nftables status ingest failed node_id=%d err=%v", nodeID, err)
	}
}

// ingestKernelCounters 把一次 KernelStatus 回读的计数面入账：订阅
// 用户计数、入站计数（realm 转发端口分流到 forward 入账链）。只做
// 计数，不做内核状态对账——状态面归 pollNodeKernels 独占。
func (h *Handler) ingestKernelCounters(nodeID int64, status agentKernelStatus, now int64) {
	h.ingestKernelCountersMode(nodeID, status, now, true)
}

// ingestKernelCountersMode 是计数摄入的两种口径：full=true 为周期
// 轮询面（用户计数连带 syncSubscriptions 同步/切断、realm 入账连
// 带执法尾）；full=false 为下发后 settle 面——只入账计数面本身。
// settle 在 applyKernelWith 的 per-kernel apply 锁（不可重入
// sync.Mutex）内执行，而 syncSubscriptions 会经
// applyKernelsForInbounds 回头取同一 kernel 的 apply 锁、执法尾的
// 暂停面同理可达 apply：锁内带这两者必自锁（realm 旧 settle 靠
// 「realm 不拥有入站」侥幸避开，FIX-BASELINE 扩面到入站内核后
// 不能再靠侥幸）。同步/切断/执法由 ≤60s 后的周期轮询全量面补
// 做，判据读的都是已入账累计值，不漏不重。
// B-NEW（入站行计数器 epoch/基线时序丢增量）：同一 status 的全部
// 计数面分两相入账，两相之间以「重建是否发生」为界。
//
// Phase A（纯记账，持 per-node 入账锁 nodeIngestLock）：入站、realm、
// 用户三面只做样本记账，不触发任何副作用。记账面只做 repo DB 写，
// 不取 kernel/nft 锁；锁内不调用 syncSubscriptions/执法尾。
// Phase B（副作用，无锁）：syncSubscriptions 与 realm 执法尾——
// 两者可触发 KernelApply→agent 重建 nft 表→新 epoch→settle 重定
// 基线；它们跑在全部样本已对旧基线结算之后，故重建换代吞不掉
// 本次样本。settle 口径（full=false）只跑 Phase A，与旧行为一致
// （settle 在 apply 锁内执行，Phase B 的副作用面可回头取同一把
// apply 锁，必自锁——见 applyRealmCountersMode 的 enforce=false
// 注释）。
//
// 为什么必须这样分：旧序先算用户计数（含 syncSubscriptions，可
// 触发 KernelApply→重建→新 epoch→settle 重定基线），后算入站计
// 数；同一 status 的陈旧入站样本撞上新 epoch 基线即 delta 0（B
// 路实测 7.3MB 全丢），且陈旧入站记账会把基线写回旧 epoch，使下
// 一轮首个新 epoch 样本也 delta 0（原站 2MB 丢增量）。两相分
// 隔后，记账与重建不再交错；per-node 锁另防两路摄入（轮询 poll
// 与下发后 settle）并发交错。epoch 换代本身仍 delta 0（FIX2-A
// 既定口径，不动）。
func (h *Handler) ingestKernelCountersMode(nodeID int64, status agentKernelStatus, now int64, full bool) {
	// A-低-6：无 nft 的节点无计数可采，早返本身不变；缺口标识由同
	// 轮的 ingestNodeNftablesStatus 落 "missing"（nftables 模式节点），
	// 不在此处重复写态。
	if !status.NftAvailable {
		return
	}
	// ---- Phase A：纯记账（持 per-node 入账锁）----
	nodeIngestMu := h.nodeIngestLock(nodeID)
	nodeIngestMu.Lock()
	var realmBatch *flowUploadBatch
	var realmMetas map[int64]repo.FlowUploadForwardMeta
	var realmNowMs int64
	var realmQuotaViews map[int64]*model.UserQuotaView
	if len(status.Users) > 0 {
		if err := h.applyUserCounterSamples(nodeID, status.Epoch, status.Users, now); err != nil {
			log.Printf("subscription traffic apply failed node_id=%d err=%v", nodeID, err)
		}
	}
	if len(status.Counters) > 0 {
		samples := make([]repo.InboundCounterSample, 0, len(status.Counters))
		var realmCounters []agentKernelCounter
		for _, c := range status.Counters {
			// realm 转发端口的计数样本（InboundID = RealmCounterBase +
			// forwardID）必须分流到 forward 入账链；落进入站样本会被当作
			// 未知入站静默丢弃（仅剩 repo 层的 unaccounted warn 日志）。
			if _, ok := kernel.RealmForwardID(c.InboundID); ok {
				realmCounters = append(realmCounters, c)
				continue
			}
			samples = append(samples, repo.InboundCounterSample{
				InboundID: c.InboundID,
				Protocol:  c.Proto,
				Direction: c.Dir,
				Epoch:     status.Epoch,
				Bytes:     c.Bytes,
			})
		}
		if err := h.repo.ApplyInboundCounterSamples(nodeID, samples, now); err != nil {
			log.Printf("kernel traffic apply failed node_id=%d err=%v", nodeID, err)
		}
		if len(realmCounters) > 0 {
			var err error
			realmBatch, realmMetas, realmNowMs, realmQuotaViews, err = h.accountRealmCounters(nodeID, status.Epoch, realmCounters, time.UnixMilli(now))
			if err != nil {
				realmBatch = nil
			}
		}
	}
	nodeIngestMu.Unlock()
	// ---- Phase B：副作用（无锁）----
	if !full {
		return
	}
	if len(status.Users) > 0 {
		// syncSubscriptions 是全局同步（单参 now）：凭据同步/限额
		// 切断/原站口令轮换，可经 applyKernelsForInbounds 触发
		// KernelApply→重建→settle；跑在 Phase A 全部样本已结算
		// 之后，故换代吞不掉本次样本。
		h.syncSubscriptions(time.UnixMilli(now))
	}
	if realmBatch != nil {
		h.enforceRealmCounters(nodeID, realmBatch, realmMetas, realmNowMs, realmQuotaViews)
	}
}

// settleKernelCountersAfterApply 在 KernelApply 成功后立即拉一次
// KernelStatus、只入账计数面，把计数基线钉在下发时刻
// （FIX-REALM-METER 立法、FIX-BASELINE 扩面到全部内核，调用点
// 注释详述）。刻意不走全量
// pollNodeKernels：状态对账含 drift 重下发与孤儿清理，下发后立刻
// 全量对账会与 drift 逻辑互相触发（agent 回读 hash 与 DB 不一致
// 时 spawn 的重下发又触发新一轮对账），形成无界下发-轮询链；计数
// 钉基线只需要计数面，恰好一条 KernelStatus、入账即止。入账走
// settle 口径（ingestKernelCountersMode full=false）：纯计数入
// 账，不带订阅同步与执法尾——本函数在 apply 锁内执行，两者都可
// 回头取同一把锁（注释见 ingestKernelCountersMode）。
func (h *Handler) settleKernelCountersAfterApply(nodeID int64) {
	res, err := h.sendNodeCommandWithTimeout(nodeID, "KernelStatus", map[string]interface{}{}, kernelCommandTimeout, false, false)
	if err != nil {
		// B-NEW：settle 读失败此前静默返回——下发后基线未钉，首个
		// 周期轮询（≤60s）前产生的流量会被首样本基线整段吞掉，
		// 且无任何记录可查。周期轮询随后补建基线（退化为原行为），
		// 但此处必须大声记一笔；不重试（下一轮询自愈；锁内重试会
		// 给 apply 路径加时序行为）。
		log.Printf("kernel settle status read failed node_id=%d err=%v (baseline not pinned; next poll rebuilds it)", nodeID, err)
		return
	}
	var status agentKernelStatus
	if err := remarshal(res.Data, &status); err != nil {
		log.Printf("kernel settle decode failed node_id=%d err=%v", nodeID, err)
		return
	}
	h.ingestKernelCountersMode(nodeID, status, time.Now().UnixMilli(), false)
	h.ingestNodeNftablesStatus(nodeID, status)
}

// kernelStatusParallel 是内核状态轮询的单节点并发上限（D6-04）：旧实现
// 串行逐节点 refreshNodeKernelStatus，命令通道挂死的节点每个吃满
// kernelCommandTimeout（20s），一轮可被拖到小时级，后面节点的内核错误
// 通知与计数入账全部迟到。节点间状态互不依赖，改为有限并发；单个节点
// 内部的顺序语义不变。
const kernelStatusParallel = 8

func (h *Handler) runKernelStatusLoop(ctx context.Context) {
	defer h.jobsWG.Done()
	ticker := time.NewTicker(kernelStatusInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		h.refreshKernelStatuses()
	}
}

// refreshKernelStatuses 执行一轮内核状态刷新（全部内核节点、有限并发）。
// FIX-CASCADE（E1-09 子形态 b）：轮询集 = ListKernelNodeIDs ∪
// 孤儿重查标记节点——最后一个内核被强删的节点不在前者内，不并
// 入标记集就永远等不到周期收敛。标记节点走 convergeKernelOrphan-
// Recheck（evenIfNone 轮询 + 确认空集后清标记）。
func (h *Handler) refreshKernelStatuses() {
	nodeIDs, err := h.repo.ListKernelNodeIDs()
	if err != nil {
		log.Printf("kernel status node lookup failed: %v", err)
		return
	}
	orphanSet := make(map[int64]struct{})
	if orphanIDs, orphanErr := h.repo.ListKernelOrphanRecheckNodeIDs(); orphanErr != nil {
		log.Printf("kernel orphan recheck node lookup failed: %v", orphanErr)
	} else {
		seen := make(map[int64]struct{}, len(nodeIDs))
		for _, id := range nodeIDs {
			seen[id] = struct{}{}
		}
		for _, id := range orphanIDs {
			orphanSet[id] = struct{}{}
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				nodeIDs = append(nodeIDs, id)
			}
		}
	}
	runParallelNodeTasks(nodeIDs, kernelStatusParallel, func(nodeID int64) {
		node, err := h.repo.GetNodeByID(nodeID)
		if err != nil || node == nil || node.Status != 1 {
			return
		}
		if _, marked := orphanSet[nodeID]; marked {
			h.convergeKernelOrphanRecheck(nodeID)
			return
		}
		h.refreshNodeKernelStatus(nodeID)
	})
}

// convergeKernelOrphanRecheck 对一个已登记孤儿重查的节点做一轮
// 收敛轮询：pollNodeKernels 的未知类型分支会逐个下发移除；只有
// 回读可达且无未确认孤儿时才清标记（节点离线/移除失败时保留标
// 记、下轮再查——标记即「未收敛」的持久事实，不许提前清除）。
func (h *Handler) convergeKernelOrphanRecheck(nodeID int64) {
	outcome := h.pollNodeKernels(nodeID, true)
	if !outcome.Reachable || len(outcome.OrphanTypes) > 0 {
		return
	}
	if err := h.repo.ClearKernelOrphanRecheck(nodeID); err != nil {
		log.Printf("kernel orphan recheck clear failed node_id=%d err=%v", nodeID, err)
	}
}

// removeNodeKernelsFromAgent 在删节点级联前尽力下发本节点全部内
// 核的移除（FIX-CASCADE / E1-09 子形态 a，调用点在删节点漏斗）。
// 逐个经 lockKernelApply 串行化（与并发 apply/控制同口径）；失败
// 只记日志点名——删节点漏斗的数据面清理一贯尽力而为（死节点必
// 须能删），且节点记录删除后 secret 与会话都不复存在、没有后续
// 收敛面，日志即该形态的终局披露。
func (h *Handler) removeNodeKernelsFromAgent(nodeID int64) {
	kernels, err := h.repo.ListNodeKernels(nodeID)
	if err != nil {
		log.Printf("node delete: list kernels failed node_id=%d err=%v (continuing)", nodeID, err)
		return
	}
	for _, k := range kernels {
		unlock := lockKernelApply(k.ID)
		_, derr := h.sendNodeCommandWithTimeout(nodeID, "KernelControl", map[string]interface{}{
			"type": k.KernelType, "action": "remove",
		}, kernelCommandTimeout, false, true)
		unlock()
		if derr != nil {
			log.Printf("node delete: kernel removal unconfirmed node_id=%d kernel=%s err=%v (continuing; node record is being deleted, no convergence path remains)", nodeID, k.KernelType, derr)
		}
	}
}

// runParallelNodeTasks 以至多 limit 个并发对每个 id 执行 fn 并等待全部完成。
func runParallelNodeTasks(ids []int64, limit int, fn func(int64)) {
	if limit < 1 {
		limit = 1
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(id int64) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(id)
		}(id)
	}
	wg.Wait()
}
