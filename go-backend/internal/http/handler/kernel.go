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

	kernelStatusPending = "pending"
	kernelStatusRunning = "running"
	kernelStatusStopped = "stopped"
	kernelStatusError   = "error"
)

var kernelStatusInterval = 60 * time.Second

// kernelApplyLocks serializes renders and dispatches per kernel so two edits
// in quick succession cannot reach the agent out of order.
var kernelApplyLocks sync.Map

func lockKernelApply(kernelID int64) func() {
	v, _ := kernelApplyLocks.LoadOrStore(kernelID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
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
}

type inboundView struct {
	ID           int64  `json:"id"`
	KernelID     int64  `json:"kernelId"`
	KernelType   string `json:"kernelType"`
	KernelStatus string `json:"kernelStatus"`
	External     bool   `json:"external"`
	// ImportedSummary / ImportedParams describe imported nodes, whose
	// parameters live in the stored proxy rather than the kernel fields.
	ImportedSummary string             `json:"importedSummary,omitempty"`
	ImportedParams  [][2]string        `json:"importedParams,omitempty"`
	NodeID          int64              `json:"nodeId"`
	NodeName        string             `json:"nodeName"`
	NodeServerIP    string             `json:"nodeServerIp"`
	Name            string             `json:"name"`
	Protocol        string             `json:"protocol"`
	Listen          string             `json:"listen"`
	Port            int                `json:"port"`
	Network         string             `json:"network"`
	Settings        kernel.Settings    `json:"settings"`
	Transport       kernel.Transport   `json:"transport"`
	Security        kernelSecurityView `json:"security"`
	Extra           string             `json:"extra"`
	ShareHost       string             `json:"shareHost"`
	Remark          string             `json:"remark"`
	RelayOnly       bool               `json:"relayOnly"`
	RelayCount      int                `json:"relayCount"`
	AcceptProxy     bool               `json:"acceptProxyProtocol"`
	Whitelist       bool               `json:"relayWhitelist"`
	WhitelistExtra  string             `json:"whitelistExtra"`
	Enabled         bool               `json:"enabled"`
	UploadBytes     int64              `json:"uploadBytes"`
	DownloadBytes   int64              `json:"downloadBytes"`
	CreatedTime     int64              `json:"createdTime"`
	UpdatedTime     int64              `json:"updatedTime"`
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
	Enabled            *bool   `json:"enabled"`
}

// ---- conversion ----

func inboundFromModel(row model.NodeInbound) (kernel.Inbound, error) {
	in := kernel.Inbound{ID: row.ID, Protocol: row.Protocol, Listen: row.Listen, Port: row.Port,
		AcceptProxyProtocol: row.AcceptProxyProtocol == 1, OutboundIPStrategy: row.OutboundIPStrategy}
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
	row.Settings = string(settings)
	row.Transport = string(transport)
	row.Security = string(security)
	row.Extra = string(in.Extra)
	return nil
}

// ---- kernel endpoints ----

func (h *Handler) kernelCapabilities(w http.ResponseWriter, r *http.Request) {
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
		response.WriteJSON(w, response.Err(-2, err.Error()))
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
		counts[in.KernelID]++
	}
	nodes := make(map[int64]*model.Node)
	views := make([]kernelView, 0, len(kernels))
	for _, k := range kernels {
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
		if node != nil {
			v.NodeName = node.Name
			v.NodeOnline = node.Status == 1
		}
		views = append(views, v)
	}
	return views, nil
}

func (h *Handler) kernelNodeEligible(nodeID int64) (*model.Node, error) {
	node, err := h.repo.GetNodeByID(nodeID)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, errors.New("节点不存在")
	}
	if node.IsRemote == 1 {
		return nil, errors.New("远程节点不支持部署内核")
	}
	if isNftablesForwardMode(node.ForwardMode) {
		return nil, errors.New("nftables 模式节点不支持部署内核")
	}
	return node, nil
}

func (h *Handler) kernelInstall(w http.ResponseWriter, r *http.Request) {
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
		response.WriteJSON(w, response.ErrDefault(err.Error()))
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
	// Kernels with a closed version list (Snell) store the concrete version
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
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if k != nil && info.Type == kernel.TypeSnell {
		if err := h.checkSnellVersions(k.ID, version); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}
	if k != nil {
		// F-021: 换版同样要过入站版本门（AnyTLS/ACME/ENC），不能把存量
		// 入站降到其最低版本之下再靠下发失败暴露。
		if err := h.checkKernelVersionGates(k.ID, info.Type, version); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
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
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	} else if err := h.repo.UpdateNodeKernelFields(k.ID, map[string]interface{}{
		"version": version, "enabled": 1, "updated_time": now,
	}); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	if err := h.applyKernel(k.ID, true); err != nil {
		response.WriteJSON(w, response.ErrDefault("内核已登记，但下发失败: "+err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

// normalizeKernelVersion accepts "latest" or a dotted version, returning ""
// for anything that could inject into a download URL.
func normalizeKernelVersion(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" || strings.EqualFold(v, "latest") {
		return "latest"
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9') && c != '.' && c != '-' && !(c >= 'a' && c <= 'z') {
			return ""
		}
	}
	if len(v) > 40 {
		return ""
	}
	return v
}

func (h *Handler) kernelControl(w http.ResponseWriter, r *http.Request) {
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
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		if err := h.applyKernel(k.ID, false); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	case "restart":
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
		brokenReferrers, guardMsg := h.kernelChainGuard(k.ID)
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
		_, err := h.sendNodeCommandWithTimeout(k.NodeID, "KernelControl", map[string]interface{}{
			"type": k.KernelType, "action": "remove",
		}, kernelCommandTimeout, false, true)
		if err != nil && !req.Force {
			unlock()
			response.WriteJSON(w, response.ErrDefault("节点卸载失败: "+err.Error()+"（可强制删除面板记录）"))
			return
		}
		if rows, lerr := h.repo.ListNodeInboundsByKernel(k.ID); lerr == nil {
			for _, row := range rows {
				h.removeInboundRelays(row.ID)
			}
		}
		err = h.repo.DeleteNodeKernel(k.ID)
		unlock()
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
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
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	kernels, err := h.repo.ListNodeKernels(0)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	relays, err := h.repo.ListInboundRelays(0)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	relayCounts := make(map[int64]int)
	for _, rel := range relays {
		relayCounts[rel.InboundID]++
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
	views := make([]inboundView, 0, len(rows))
	for _, row := range rows {
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
			ShareHost:          row.ShareHost,
			Remark:             row.Remark,
			RelayOnly:          row.RelayOnly == 1,
			RelayCount:         relayCounts[row.ID],
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
		}
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

func (h *Handler) inboundCreate(w http.ResponseWriter, r *http.Request) {
	h.inboundSave(w, r, false)
}

func (h *Handler) inboundUpdate(w http.ResponseWriter, r *http.Request) {
	h.inboundSave(w, r, true)
}

func (h *Handler) inboundSave(w http.ResponseWriter, r *http.Request, update bool) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req inboundRequest
	if err := decodeJSON(r.Body, &req); err != nil {
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
	req.ShareHost = strings.TrimSpace(req.ShareHost)
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
	if update {
		existing, err := h.repo.GetNodeInbound(req.ID)
		if err != nil || existing == nil {
			response.WriteJSON(w, response.ErrDefault("节点服务不存在"))
			return
		}
		row = *existing
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
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		var err error
		k, err = h.repo.GetNodeKernelByType(req.NodeID, strings.ToLower(strings.TrimSpace(req.KernelType)))
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
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
	}
	if req.OutboundIPStrategy != nil {
		in.OutboundIPStrategy = *req.OutboundIPStrategy
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
	}
	allowExtra, err := kernel.ParseAllowList(req.WhitelistExtra)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// AUDIT-R3 (F2X-001): per-node 端口分配锁必须覆盖"选端口→冲突检查→入库"
	// 整个临界区。R5-B4 把锁放在 CreateNodeInbound 内部，检查在锁外，
	// 并发下两请求可挑中同一随机端口，一个撞唯一索引报裸 DB 错误。
	// R5R3-001: 锁在 DB 写入后立即显式释放，不用 defer——defer 会穿越
	// 下游 applyKernel 等网络调用（最长 5min 超时），违反"锁内禁止网络调用"。
	// 注意：临界区内每个提前返回前都必须先释放（Release 即 Unlock，不可双释放）。
	portAllocMu := repo.AcquirePortAllocLock(row.NodeID)
	releasePortAlloc := func() { repo.ReleasePortAllocLock(portAllocMu) }
	if in.Port == 0 {
		port, err := h.pickFreeInboundPort(row.NodeID, row.ID)
		if err != nil {
			releasePortAlloc()
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		in.Port = port
	}
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
	prevTargets := outboundTargets(row)
	if err := h.setInboundRouting(&row, k.KernelType, k.Version, req.OutboundID, req.SplitRules); err != nil {
		releasePortAlloc()
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// Snell 双向版本判定已并入 kernelVersionGate（上方同一调用点），
	// 保存路径不再单设散点检查（S4 移植，原单向检查会放行 6.x+v4/v5）。
	if msg, err := h.inboundPortConflict(row.NodeID, in, row.ID); err != nil {
		releasePortAlloc()
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	} else if msg != "" {
		releasePortAlloc()
		response.WriteJSON(w, response.ErrDefault(msg))
		return
	}

	if err := applyInboundToModel(in, &row); err != nil {
		releasePortAlloc()
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
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
	row.UpdatedTime = now

	if update {
		err = h.repo.SaveNodeInbound(&row)
	} else {
		err = h.repo.CreateNodeInbound(&row)
	}
	// R5R3-001: 临界区结束，立即释放锁。下游 applyKernel/applyChainReferrers/
	// applyChangedTargets 会做网络调用（节点下发），必须无锁执行。
	releasePortAlloc()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	var warnings []string
	if update && prevAcceptProxy != row.AcceptProxyProtocol {
		warnings = h.syncRelayProxyProtocol(row)
	}
	if err := h.applyKernel(k.ID, false); err != nil {
		warnings = append([]string{"下发到节点失败: " + err.Error()}, warnings...)
	}
	if update {
		warnings = append(warnings, h.applyChainReferrers(row.ID)...)
	}
	warnings = append(warnings, h.applyChangedTargets(prevTargets, row)...)
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
	for i := 0; i < 64; i++ {
		port := 20000 + rand.Intn(40000)
		msg, err := h.inboundPortConflict(nodeID, kernel.Inbound{Port: port}, excludeInboundID)
		if err != nil {
			return 0, err
		}
		if msg == "" {
			return port, nil
		}
	}
	return 0, errors.New("未找到可用端口，请手动指定")
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

// inboundSpans lists the ports an inbound needs: its listen port, the
// loopback backend of a ShadowTLS front, and the port-hopping range.
func inboundSpans(in kernel.Inbound) []portSpan {
	spans := []portSpan{{in.Port, in.Port, "端口"}}
	if bp := in.Settings.BackendPort; bp > 0 {
		spans = append(spans, portSpan{bp, bp, "后端端口"})
	}
	if lo, hi := in.HopRange(); lo > 0 {
		spans = append(spans, portSpan{lo, hi, "端口跳跃范围"})
	}
	return spans
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
			continue
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
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	row, err := h.repo.GetNodeInbound(id)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if row == nil {
		// X-P3-28（L-11-3，V-12）：不存在幂等成功。
		response.WriteJSON(w, response.OKEmpty())
		return
	}
	if msg := h.chainInUseMessage(row); msg != "" {
		response.WriteJSON(w, response.ErrDefault(msg))
		return
	}
	h.removeInboundRelays(id)
	if err := h.repo.DeleteNodeInbound(id); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	for _, target := range outboundTargets(*row) {
		h.applyWhitelistedTarget(target)
	}
	if isExternalInbound(row) {
		response.WriteJSON(w, response.OKEmpty())
		return
	}
	if err := h.applyKernel(row.KernelID, false); err != nil {
		response.WriteJSON(w, response.R{Code: 0, Msg: "已删除，但下发到节点失败: " + err.Error(), TS: time.Now().UnixMilli()})
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

// inboundBatchDelete removes several inbounds and their relays, then pushes
// each affected kernel's config once instead of once per inbound.
func (h *Handler) inboundBatchDelete(w http.ResponseWriter, r *http.Request) {
	ids := idsFromBody(r, w)
	if ids == nil {
		return
	}
	s := 0
	f := 0
	failures := make([]batchFailureDetail, 0)
	var targetIDs []int64
	kernelIDs := make([]int64, 0)
	seenKernel := make(map[int64]bool)
	for _, id := range ids {
		row, err := h.repo.GetNodeInbound(id)
		if err != nil || row == nil {
			f++
			failures = appendBatchFailureReason(failures, id, "", "节点服务不存在")
			continue
		}
		if msg := h.chainInUseMessage(row); msg != "" && !batchDeletesReferrers(ids, h.chainReferrers(id)) {
			f++
			failures = appendBatchFailureReason(failures, id, row.Name, msg)
			continue
		}
		h.removeInboundRelays(id)
		if err := h.repo.DeleteNodeInbound(id); err != nil {
			f++
			failures = appendBatchFailure(failures, id, row.Name, err)
			continue
		}
		s++
		if !isExternalInbound(row) && !seenKernel[row.KernelID] {
			seenKernel[row.KernelID] = true
			kernelIDs = append(kernelIDs, row.KernelID)
		}
		targetIDs = append(targetIDs, outboundTargets(*row)...)
	}
	msg := "操作成功"
	for _, kernelID := range kernelIDs {
		if err := h.applyKernel(kernelID, false); err != nil {
			msg = "已删除，但下发到节点失败: " + err.Error()
		}
	}
	for _, targetID := range targetIDs {
		h.applyWhitelistedTarget(targetID)
	}
	response.WriteJSON(w, response.R{
		Code: 0,
		Msg:  msg,
		Data: batchOperationResult{SuccessCount: s, FailCount: f, Failures: failures},
		TS:   time.Now().UnixMilli(),
	})
}

// inboundBatchToggle enables or disables several inbounds, then pushes each
// affected kernel's config once.
func (h *Handler) inboundBatchToggle(w http.ResponseWriter, r *http.Request) {
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
	msg := "操作成功"
	for _, kernelID := range kernelIDs {
		if err := h.applyKernel(kernelID, false); err != nil {
			msg = "已保存，但下发到节点失败: " + err.Error()
		}
	}
	response.WriteJSON(w, response.R{
		Code: 0,
		Msg:  msg,
		Data: batchOperationResult{SuccessCount: s, FailCount: f, Failures: failures},
		TS:   time.Now().UnixMilli(),
	})
}

func (h *Handler) inboundToggle(w http.ResponseWriter, r *http.Request) {
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
	row.Enabled = boolToInt(req.Enabled)
	row.UpdatedTime = time.Now().UnixMilli()
	if err := h.repo.SaveNodeInbound(row); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if isExternalInbound(row) {
		response.WriteJSON(w, response.OKEmpty())
		return
	}
	if err := h.applyKernel(row.KernelID, false); err != nil {
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
	if err := h.repo.ResetNodeInboundTraffic(id, time.Now().UnixMilli()); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) inboundRegenerate(w http.ResponseWriter, r *http.Request) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	row, err := h.repo.GetNodeInbound(id)
	if err != nil || row == nil {
		response.WriteJSON(w, response.ErrDefault("节点服务不存在"))
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
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if err := h.applyKernel(k.ID, false); err != nil {
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
	Bytes     int64  `json:"bytes"`
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
}

func remarshal(in interface{}, out interface{}) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
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
	inbounds := make([]kernel.Inbound, 0, len(rows))
	ports := make([]map[string]interface{}, 0, len(rows))
	users := h.activeUsersByInbound(time.Now())
	backendPorts := make(map[int]string)
	publicPorts := make(map[int]string)
	for _, row := range rows {
		if row.Enabled != 1 {
			continue
		}
		in, err := inboundFromModel(row)
		if err != nil {
			return nil, "", err
		}
		// 下发前复检：组合必须在内核 catalog 内，历史脏数据不得被渲染层静默降级。
		if !kernel.ComboSupported(k.KernelType, in.Protocol, in.Transport.Type, in.Security.Type) {
			return nil, "", fmt.Errorf("入站「%s」的协议组合（%s / %s / %s）与内核 %s 不匹配，已拒绝下发，请修正或删除该入站", row.Name, in.Protocol, in.Transport.Type, in.Security.Type, k.KernelType)
		}
		if msg := kernelVersionGate(k.KernelType, k.Version, &in); msg != "" {
			return nil, "", fmt.Errorf("入站「%s」与内核版本不兼容：%s，已拒绝下发", row.Name, msg)
		}
		publicPorts[in.Port] = row.Name
		if in.Protocol == kernel.ProtocolSnell && in.Security.Type == kernel.SecurityShadowTLS && in.Settings.BackendPort > 0 {
			if prev, dup := backendPorts[in.Settings.BackendPort]; dup {
				return nil, "", fmt.Errorf("入站「%s」与「%s」的 ShadowTLS 回环端口同为 %d，下发必然冲突，请删除其一后重试", row.Name, prev, in.Settings.BackendPort)
			}
			backendPorts[in.Settings.BackendPort] = row.Name
		}
		if in.SupportsUsers() {
			in.Users = users[in.ID]
		}
		if in.Via, err = h.inboundVia(row); err != nil {
			return nil, "", err
		}
		if in.Split, err = h.inboundSplit(row); err != nil {
			return nil, "", err
		}
		// R2X-007 下发面：存量脏组合（保存期未拦）在下发期同判——任一
		// Via/分流目标带 ENC 且拨号内核 Xray <25.6.8，整单拒绝。
		if msg := encDialGate(k.KernelType, k.Version, in.Via); msg != "" {
			return nil, "", fmt.Errorf("入站「%s」与内核版本不兼容：%s，已拒绝下发", row.Name, msg)
		}
		for _, rule := range in.Split {
			if msg := encDialGate(k.KernelType, k.Version, rule.Via); msg != "" {
				return nil, "", fmt.Errorf("入站「%s」与内核版本不兼容：%s，已拒绝下发", row.Name, msg)
			}
		}
		inbounds = append(inbounds, in)
		port := map[string]interface{}{"inboundId": in.ID, "port": in.Port, "network": in.NetworkOn(k.KernelType)}
		if lo, hi := in.HopRange(); lo > 0 {
			port["hopFrom"], port["hopTo"] = lo, hi
		}
		if row.RelayWhitelist == 1 {
			port["allow"] = h.inboundAllowList(row)
		}
		ports = append(ports, port)
	}
	for bp, name := range backendPorts {
		if owner, clash := publicPorts[bp]; clash {
			return nil, "", fmt.Errorf("入站「%s」的 ShadowTLS 回环端口 %d 与入站「%s」的监听端口冲突，已拒绝下发", name, bp, owner)
		}
	}
	proxy := ""
	if enabled, proxyURL := h.getGithubProxyConfig(); enabled {
		proxy = proxyURL
	}
	rendered, err := kernel.RenderWith(k.KernelType, inbounds, kernel.RenderOptions{
		RuleSetMirror:     proxy,
		SingboxHTTPClient: !versionBelow(k.Version, 1, 14),
		SingboxSnell:      !versionBelow(k.Version, 1, 14),
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
	}
	if len(rendered.UserMarks) > 0 {
		payload["userMarks"] = rendered.UserMarks
	}
	if k.KernelType == kernel.TypeXray {
		// The agent keeps the 分流 data files next to Xray up to date.
		payload["geoData"] = kernel.XrayGeoData(proxy)
	}
	if len(rendered.Processes) > 0 {
		payload["processes"] = rendered.Processes
	}
	if len(rendered.Extras) > 0 {
		extras := make([]map[string]interface{}, 0, len(rendered.Extras))
		for _, name := range rendered.Extras {
			if name != kernel.ShadowTLSExtra {
				return nil, "", fmt.Errorf("未知的附加程序 %s", name)
			}
			d, err := kernel.ExtraDownload(kernel.ShadowTLSDownload(), kernel.ShadowTLSVersion, proxy)
			if err != nil {
				return nil, "", err
			}
			extras = append(extras, map[string]interface{}{"name": name, "version": kernel.ShadowTLSVersion, "download": d})
		}
		payload["extras"] = extras
	}
	return payload, rendered.Hash, nil
}

type kernelVersionItem struct {
	Version     string `json:"version"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"publishedAt,omitempty"`
}

var kernelVersionCache = struct {
	sync.Mutex
	at    map[string]time.Time
	items map[string][]kernelVersionItem
}{at: map[string]time.Time{}, items: map[string][]kernelVersionItem{}}

// kernelReleaseVersions lists recent releases of a kernel from GitHub,
// cached for 30 minutes. Kernels with a pinned list return that list.
func kernelReleaseVersions(info kernel.Info) ([]kernelVersionItem, error) {
	return kernelReleaseVersionsWithProxy(info, "")
}

// F-059：内核版本列表是版本发现的第三个孪生实现，此前只直连 api.github.com
// ——与 F-029 的节点侧列表同病。补同样的代理先行/直连降级；流式解码与
// 256MiB 兜底保持不变（见 fetchKernelReleaseVersionsPage）。
func kernelReleaseVersionsWithProxy(info kernel.Info, proxyURL string) ([]kernelVersionItem, error) {
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
	kernelVersionCache.Lock()
	defer kernelVersionCache.Unlock()
	if at, ok := kernelVersionCache.at[info.Type]; ok && time.Since(at) < 30*time.Minute {
		return kernelVersionCache.items[info.Type], nil
	}
	directURL := fmt.Sprintf("%s/repos/%s/releases?per_page=30", githubAPIBase, info.ReleaseRepo)
	urls := []string{directURL}
	if proxyURL != "" {
		urls = []string{proxyURL + "/" + directURL, directURL}
	}
	var lastErr error
	for _, url := range urls {
		out, err := fetchKernelReleaseVersionsPage(url)
		if err == nil {
			kernelVersionCache.at[info.Type] = time.Now()
			kernelVersionCache.items[info.Type] = out
			return out, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func fetchKernelReleaseVersionsPage(url string) ([]kernelVersionItem, error) {
	// X1-403：超时 15s → 30s（经代理多一跳 + 大响应，口径同 U1-006）。
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
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
	items, err := kernelReleaseVersionsWithProxy(info, h.githubProxyURLForFetch())
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(items))
}

// versionBelow reports whether a concrete "x.y[.z]" version is older than
// major.minor; "latest" and unparsable versions are not.
func versionBelow(version string, major, minor int) bool {
	parts := strings.SplitN(strings.TrimPrefix(version, "v"), ".", 3)
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

// versionBelow3 是 versionBelow 的三段版（Xray 的 25.6.8 这类下限需要精确到
// 修订号）；"latest" 与不可解析的版本同样不视为过低。
func versionBelow3(version string, major, minor, patch int) bool {
	parts := strings.SplitN(strings.TrimPrefix(version, "v"), ".", 3)
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

// kernelVersionGate 是入站对内核版本的硬性要求（单一真源）：入站保存
// （inboundSave）、内核换版（kernelInstall）与下发前复检
// （buildKernelApplyPayload）三处共用同一口径，避免"修一处漏一处"
// （F-021：此前只有保存期查 AnyTLS/ACME 两门，换版与复检均未覆盖）。
// 返回空串表示通过；非空为拒绝原因（不含入站名，调用方按上下文包装）。
// "latest" 与不可解析版本按 versionBelow 既有语义不视为过低、不拦。
func kernelVersionGate(kernelType, version string, in *kernel.Inbound) string {
	if in.Security.CertMode == kernel.CertModeACME && versionBelow(version, 1, 14) {
		return fmt.Sprintf("自动申请证书需要 sing-box 1.14 及以上，节点安装的是 %s", version)
	}
	if kernelType == kernel.TypeSingbox && in.Protocol == kernel.ProtocolAnyTLS && versionBelow(version, 1, 12) {
		return fmt.Sprintf("AnyTLS 需要 sing-box 1.12 及以上，节点安装的是 %s", version)
	}
	if kernelType == kernel.TypeSingbox && in.Protocol == kernel.ProtocolSnell && versionBelow(version, 1, 14) {
		return fmt.Sprintf("sing-box 内置 Snell 需要 1.14 及以上，节点安装的是 %s", version)
	}
	if in.Settings.EncryptionEnabled() && versionBelow3(version, 25, 6, 8) {
		return fmt.Sprintf("VLESS 加密需要 Xray 25.6.8 及以上，节点安装的是 %s", version)
	}
	// 出站 IP 策略版本门（S4）：策略字段随入站一同进入 gate，保存/复检/
	// 换版三处同拦；未知策略字符串也在此拒绝。
	if err := validateOutboundIPVersion(kernelType, version, in.OutboundIPStrategy); err != nil {
		return err.Error()
	}
	// Snell 独立内核：服务端/客户端版本双向判定（S4 移植）。6.x 只接
	// v6 客户端，旧单向检查（客户端版本 > 服务端 major 才拦）会放行
	// 「6.0.0 内核 + v4/v5 入站」这类必然连不上的组合。
	if kernelType == kernel.TypeSnell && in.Protocol == kernel.ProtocolSnell {
		if err := snellServes(version, in.Settings.SnellVersion); err != nil {
			return err.Error()
		}
	}
	return ""
}

// encDialGate 是 R2X-007 的 ENC 拨号侧版本门：链式/分流目标带 VLESS
// 加密时，拨号方内核同样需要 Xray 25.6.8+——ENC 不只是服务端属性。
// 版本比较与入站自有门（kernelVersionGate）同用 versionBelow3，保持
// 单一口径；保存（validateChainVersion）、下发（buildKernelApplyPayload）
// 与换版扫描（checkKernelVersionGates）三面共用。返回空串表示通过。
func encDialGate(kernelType, version string, e *kernel.Endpoint) string {
	if e == nil || kernelType != kernel.TypeXray || !versionBelow3(version, 25, 6, 8) || !e.HasVLESSEncryption() {
		return ""
	}
	return fmt.Sprintf("VLESS 加密需要 Xray 25.6.8 及以上，节点安装的是 %s", version)
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
// would break an existing inbound's version gate（F-021，仿 checkSnellVersions）。
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
		if msg := kernelVersionGate(kernelType, version, &in); msg != "" {
			return fmt.Errorf("入站 %s 与目标版本不兼容：%s，不能安装 %s", row.Name, msg, version)
		}
		// 经 Snell 节点出站（链式/分流目标）的入站同样要求 sing-box
		// 1.14+；目标入站不在本内核上，gate 看不到，须单独扫描（S4）。
		if kernelType == kernel.TypeSingbox && versionBelow(version, 1, 14) {
			for _, id := range outboundTargets(row) {
				if t, err := h.repo.GetNodeInbound(id); err == nil && t != nil && t.Protocol == kernel.ProtocolSnell {
					return fmt.Errorf("入站 %s 经 Snell 节点 %s 出站，需要 sing-box 1.14 及以上，不能安装 %s", row.Name, t.Name, version)
				}
			}
		}
		// R2X-007：经 ENC 节点出站（链式/分流目标）的入站同样要求拨号
		// 内核 Xray 25.6.8+；目标入站不在本内核上，gate 看不到，须单独
		// 扫描（仿上方 Snell 扫描块）。
		if kernelType == kernel.TypeXray && versionBelow3(version, 25, 6, 8) {
			for _, id := range outboundTargets(row) {
				if t, err := h.repo.GetNodeInbound(id); err == nil && t != nil && h.inboundRowHasENC(t) {
					return fmt.Errorf("入站 %s 经 VLESS 加密节点 %s 出站，需要 Xray 25.6.8 及以上，不能安装 %s", row.Name, t.Name, version)
				}
			}
		}
	}
	return nil
}

// snellMajor returns the protocol major version a Snell binary serves.
func snellMajor(version string) int {
	switch {
	case strings.HasPrefix(version, "4."):
		return 4
	case strings.HasPrefix(version, "6."):
		return 6
	}
	return 5
}

// snellServes reports whether a snell-server version accepts clients of a
// protocol version: 5.x also serves v4, while 6.x serves v6 only（S4 移植，
// 上游同名判定）。
func snellServes(version string, client int) error {
	major := snellMajor(version)
	switch {
	case major == 6 && client != 6:
		return fmt.Errorf("Snell %s 只接受 v6 客户端：请把入站改成 v6，或安装 Snell 5.0.1", version)
	case client > major:
		return fmt.Errorf("节点安装的是 Snell %s，不支持 v%d 客户端", version, client)
	}
	return nil
}

// checkSnellVersions refuses to install a Snell binary that cannot serve
// the protocol version an existing inbound advertises to clients.
func (h *Handler) checkSnellVersions(kernelID int64, version string) error {
	rows, err := h.repo.ListNodeInboundsByKernel(kernelID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		in, err := inboundFromModel(row)
		if err == nil {
			if serr := snellServes(version, in.Settings.SnellVersion); serr != nil {
				return fmt.Errorf("入站 %s：%v，不能安装 %s", row.Name, serr, version)
			}
		}
	}
	return nil
}

// applyKernel pushes the current configuration of a kernel to its node. An
// offline node is not an error for persistence: the kernel stays pending and
// is reconciled when the agent reconnects.
func (h *Handler) applyKernel(kernelID int64, install bool) error {
	return h.applyKernelWith(kernelID, install, nil)
}

func (h *Handler) applyKernelWith(kernelID int64, install bool, realmOverride map[int64]bool) error {
	unlock := lockKernelApply(kernelID)
	defer unlock()

	k, err := h.repo.GetNodeKernel(kernelID)
	if err != nil {
		return err
	}
	if k == nil {
		return errors.New("内核不存在")
	}
	payload, hash, err := h.buildKernelApplyPayloadWith(k, realmOverride)
	now := time.Now().UnixMilli()
	if err != nil {
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
		status := kernelStatusError
		if strings.Contains(err.Error(), "不在线") {
			status = kernelStatusPending
		}
		_ = h.repo.UpdateNodeKernelFields(k.ID, map[string]interface{}{
			"status": status, "last_error": err.Error(), "updated_time": now,
		})
		return err
	}

	var state agentKernelState
	_ = remarshal(res.Data, &state)
	fields := map[string]interface{}{
		"applied_hash": hash,
		"status":       kernelStateStatus(state, k.Enabled == 1),
		"last_error":   state.Error,
		"updated_time": now,
	}
	if state.Version != "" {
		fields["installed_version"] = truncate(state.Version, 100)
	}
	return h.repo.UpdateNodeKernelFields(k.ID, fields)
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
	kernels, err := h.repo.ListNodeKernels(nodeID)
	if err != nil {
		return
	}
	for _, k := range kernels {
		if err := h.applyKernel(k.ID, false); err != nil {
			log.Printf("kernel reconcile failed node_id=%d kernel=%s err=%v", nodeID, k.KernelType, err)
		}
	}
	// Also runs when the panel has no kernel left on the node, so kernels
	// deleted while it was offline get removed.
	h.pollNodeKernels(nodeID, true)
}

// refreshNodeKernelStatus polls one node for kernel state and traffic.
func (h *Handler) refreshNodeKernelStatus(nodeID int64) {
	h.pollNodeKernels(nodeID, false)
}

func (h *Handler) pollNodeKernels(nodeID int64, evenIfNone bool) {
	kernels, err := h.repo.ListNodeKernels(nodeID)
	if err != nil || (len(kernels) == 0 && !evenIfNone) {
		return
	}
	res, err := h.sendNodeCommandWithTimeout(nodeID, "KernelStatus", map[string]interface{}{}, kernelCommandTimeout, false, false)
	if err != nil {
		return // offline, or an agent without kernel support
	}
	var status agentKernelStatus
	if err := remarshal(res.Data, &status); err != nil {
		log.Printf("kernel status decode failed node_id=%d err=%v", nodeID, err)
		return
	}

	now := time.Now().UnixMilli()
	byType := make(map[string]agentKernelState, len(status.Kernels))
	for _, s := range status.Kernels {
		byType[s.Type] = s
	}
	known := make(map[string]struct{}, len(kernels))
	for _, k := range kernels {
		known[k.KernelType] = struct{}{}
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
				if err := h.applyKernel(id, false); err != nil {
					log.Printf("kernel drift re-apply failed kernel_id=%d err=%v", id, err)
				}
			}(k.ID)
			continue
		}
		newStatus := kernelStateStatus(s, k.Enabled == 1)
		if newStatus == kernelStatusError && k.Status != kernelStatusError {
			msg := fmt.Sprintf("节点：%s\n内核：%s", h.nodeLabel(nodeID), k.KernelType)
			if s.Error != "" {
				msg += "\n错误：" + truncate(s.Error, 300)
			}
			kernelID, prevStatus := k.ID, k.Status
			h.notifyTracked(notifyEventKernelError, "内核运行出错", msg, func(outcome notifyOutcome) {
				// X-P2-8（A7-03 同族）：发送失败回滚 error 置位。下方 fields
				// 更新已把状态落为 error，若不回滚，下一轮 KernelStatus 因
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
				fmt.Sprintf("节点：%s\n内核：%s", h.nodeLabel(nodeID), k.KernelType))
		}
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

	if !status.NftAvailable {
		return
	}
	if len(status.Users) > 0 {
		h.applyUserCounters(nodeID, status.Epoch, status.Users, now)
	}
	if len(status.Counters) == 0 {
		return
	}
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
	if err := h.repo.ApplyInboundCounterSamples(samples, now); err != nil {
		log.Printf("kernel traffic apply failed node_id=%d err=%v", nodeID, err)
	}
	if len(realmCounters) > 0 {
		h.applyRealmCounters(nodeID, status.Epoch, realmCounters, time.UnixMilli(now))
	}
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
func (h *Handler) refreshKernelStatuses() {
	nodeIDs, err := h.repo.ListKernelNodeIDs()
	if err != nil {
		log.Printf("kernel status node lookup failed: %v", err)
		return
	}
	runParallelNodeTasks(nodeIDs, kernelStatusParallel, func(nodeID int64) {
		node, err := h.repo.GetNodeByID(nodeID)
		if err != nil || node == nil || node.Status != 1 {
			return
		}
		h.refreshNodeKernelStatus(nodeID)
	})
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
