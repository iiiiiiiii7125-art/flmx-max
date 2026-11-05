package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

const peerShareResourcePrefix = "peer-share-"

func peerShareResourceName(shareID int64, kind, original string) string {
	return fmt.Sprintf("%s%d-%s-%s", peerShareResourcePrefix, shareID, kind, base64.RawURLEncoding.EncodeToString([]byte(original)))
}

func parsePeerShareServiceName(name string) (shareID int64, originalName string, ok bool) {
	if !strings.HasPrefix(name, peerShareResourcePrefix) {
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(name, peerShareResourcePrefix), "-", 3)
	if len(parts) != 3 || parts[1] != "service" {
		return
	}
	shareID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || shareID <= 0 {
		return 0, "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(decoded) == 0 {
		return 0, "", false
	}
	return shareID, string(decoded), true
}

func federationResourceCommandKind(cmd string) (kind, action string) {
	lower := strings.ToLower(cmd)
	for _, entry := range []struct{ suffix, kind string }{{"service", "service"}, {"chains", "chain"}, {"climiters", "climiter"}, {"limiters", "limiter"}} {
		if strings.HasSuffix(lower, entry.suffix) {
			return entry.kind, strings.TrimSuffix(lower, entry.suffix)
		}
	}
	return "", ""
}

// Every peer-supplied reference is resolved within the same share namespace.
// Composite traffic limiters use a comma-separated list in GOST.
func scopePeerResourceReferences(value interface{}, shareID int64) {
	switch v := value.(type) {
	case map[string]interface{}:
		for key, child := range v {
			if strings.EqualFold(key, "chains") {
				if names, ok := child.([]interface{}); ok {
					for i, name := range names {
						if raw, ok := name.(string); ok {
							names[i] = peerShareResourceName(shareID, "chain", raw)
						}
					}
					continue
				}
			}
			kind := ""
			switch strings.ToLower(key) {
			case "chain":
				kind = "chain"
			case "limiter":
				kind = "limiter"
			case "climiter":
				kind = "climiter"
			}
			if raw, ok := child.(string); ok && kind != "" && strings.TrimSpace(raw) != "" {
				names := strings.Split(raw, ",")
				for i, name := range names {
					names[i] = peerShareResourceName(shareID, kind, strings.TrimSpace(name))
				}
				v[key] = strings.Join(names, ",")
			} else {
				scopePeerResourceReferences(child, shareID)
			}
		}
	case []interface{}:
		for _, child := range v {
			scopePeerResourceReferences(child, shareID)
		}
	}
}

func peerResourceDeletePayload(item repo.PeerShareResource) interface{} {
	if item.Kind == "service" {
		return map[string]interface{}{"services": []string{item.RuntimeName}}
	}
	key := item.Kind
	if key == "climiter" {
		key = "limiter"
	}
	return map[string]interface{}{key: item.RuntimeName}
}
func peerResourceDeleteCommand(kind string) string {
	switch kind {
	case "service":
		return "DeleteService"
	case "chain":
		return "DeleteChains"
	case "climiter":
		return "DeleteCLimiters"
	default:
		return "DeleteLimiters"
	}
}

// preparePeerResourceCommand validates and writes desired state before the node
// sees any command, closing the flow-report race even without a reservation.
func (h *Handler) preparePeerResourceCommand(share *repo.PeerShare, cmd string, data interface{}) ([]repo.PeerShareResource, error) {
	kind, action := federationResourceCommandKind(cmd)
	if kind == "" {
		return nil, fmt.Errorf("command not allowed")
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var decoded interface{}
	if err = json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	var configs []map[string]interface{}
	var names []string
	setting := action == "add" || action == "update"
	if setting {
		if kind == "service" {
			configs = extractFederationServiceEntries(decoded)
		} else if m, ok := decoded.(map[string]interface{}); ok {
			if nested, ok := m["data"].(map[string]interface{}); ok {
				configs = []map[string]interface{}{nested}
			} else {
				configs = []map[string]interface{}{m}
			}
		}
		if len(configs) == 0 {
			return nil, fmt.Errorf("resource configuration is required")
		}
		for _, c := range configs {
			names = append(names, strings.TrimSpace(asString(c["name"])))
		}
	} else {
		if kind == "service" {
			if m, ok := decoded.(map[string]interface{}); ok {
				for _, v := range asAnySlice(m["services"]) {
					names = append(names, strings.TrimSpace(asString(v)))
				}
			}
		} else {
			if s, ok := decoded.(string); ok {
				names = []string{strings.TrimSpace(s)}
			} else if m, ok := decoded.(map[string]interface{}); ok {
				key := kind
				if key == "climiter" {
					key = "limiter"
				}
				names = []string{strings.TrimSpace(asString(m[key]))}
			}
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("resource name is required")
		}
	}
	requestedNames := make(map[string]bool, len(names))
	for _, name := range names {
		requestedNames[name] = true
	}
	items := make([]repo.PeerShareResource, 0, len(names))
	seen := map[string]bool{}
	for i, name := range names {
		if name == "" || strings.HasPrefix(name, peerShareResourcePrefix) {
			return nil, fmt.Errorf("invalid peer resource name")
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate resource name")
		}
		seen[name] = true
		old, err := h.repo.GetPeerShareResource(share.ID, kind, name)
		if err != nil {
			return nil, err
		}
		item := repo.PeerShareResource{ShareID: share.ID, NodeID: share.NodeID, Kind: kind, OriginalName: name, RuntimeName: peerShareResourceName(share.ID, kind, name), DesiredState: "active", UpdatedTime: time.Now().UnixMilli()}
		if old != nil {
			item = *old
			item.Applied = 0
			item.UpdatedTime = time.Now().UnixMilli()
		}
		if setting {
			if old == nil && kind == "service" {
				legacy, err := h.peerShareLegacyServiceNames(share, name)
				if err != nil {
					return nil, err
				}
				if len(legacy) > 0 {
					encoded, _ := json.Marshal(legacy)
					item.LegacyNames = string(encoded)
					item.LegacyServiceBase = normalizeForwardRuntimeServiceName(name)
				}
			}
			config := configs[i]
			if err := validatePeerResourceConfig(config, kind); err != nil {
				return nil, err
			}
			scopePeerResourceReferences(config, share.ID)
			config["name"] = item.RuntimeName
			encoded, err := json.Marshal(config)
			if err != nil {
				return nil, err
			}
			item.Config = string(encoded)
			item.DesiredState = "active"
			item.ReleaseLegacyFamily = false
		} else {
			if old == nil {
				// Delete callers send candidate base/TCP/UDP names. Missing names
				// are safe no-ops; never forward a raw legacy name to the node.
				if action == "delete" {
					if kind != "service" {
						continue
					}
					legacy, err := h.peerShareLegacyServiceNames(share, name)
					if err != nil {
						return nil, err
					}
					if len(legacy) == 0 {
						continue
					}
					encoded, _ := json.Marshal(legacy)
					item.LegacyNames = string(encoded)
					item.LegacyServiceBase = normalizeForwardRuntimeServiceName(name)
				} else {
					return nil, fmt.Errorf("service %q not found", name)
				}
			}
			if old != nil && old.DesiredState == "deleted" && action != "delete" {
				return nil, fmt.Errorf("service %q not found", name)
			}
			switch action {
			case "delete":
				item.DesiredState = "deleted"
				base := normalizeForwardRuntimeServiceName(name)
				if requestedNames[base] && requestedNames[base+"_tcp"] && requestedNames[base+"_udp"] {
					item.ReleaseLegacyFamily = true
				}
			case "pause":
				item.DesiredState = "paused"
			case "resume":
				if item.Config == "" {
					return nil, fmt.Errorf("resource has no saved configuration")
				}
				// B6-06: resume 直接把存量配置重新置 active 并物化，
				// 必须与写入面同律重跑校验全集——否则启动复检刚置
				// paused 的植入配置可被对端一条 ResumeService 原样
				// 复活，复检形同虚设。存量配置 JSON 不可解析同按
				// fail-closed 拒绝。
				var storedConfig interface{}
				if err := json.Unmarshal([]byte(item.Config), &storedConfig); err != nil {
					return nil, fmt.Errorf("resource has no valid saved configuration")
				}
				if err := validatePeerResourceConfig(storedConfig, kind); err != nil {
					return nil, err
				}
				item.DesiredState = "active"
			default:
				return nil, fmt.Errorf("command not allowed")
			}
		}
		items = append(items, item)
	}
	// Ownership and desired config commit atomically. A failed resource write
	// must not rename a legacy binding and expose its remaining transports.
	err = h.repo.WithPeerShareResourceTransaction(func(tx *repo.Repository) error {
		if setting && kind == "service" {
			scoped := make([]interface{}, 0, len(items))
			for _, item := range items {
				var c map[string]interface{}
				_ = json.Unmarshal([]byte(item.Config), &c)
				scoped = append(scoped, c)
			}
			binder := &Handler{repo: tx}
			if err := binder.bindPeerShareForwardRuntimeServices(share, scoped); err != nil {
				return err
			}
		}
		return tx.SavePeerShareResources(items)
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (h *Handler) applyPeerShareResource(item repo.PeerShareResource) (ws.CommandResult, error) {
	var result ws.CommandResult
	var err error
	if item.LegacyNames != "" {
		var names []string
		if err := json.Unmarshal([]byte(item.LegacyNames), &names); err != nil {
			return result, err
		}
		share, loadErr := h.repo.GetPeerShare(item.ShareID)
		if loadErr != nil {
			return result, loadErr
		}
		if share == nil {
			return result, fmt.Errorf("legacy resource ownership missing")
		}
		for _, name := range names {
			owned, checkErr := h.peerShareLegacyServiceNames(share, name)
			if checkErr != nil {
				return result, checkErr
			}
			if len(owned) == 0 {
				return result, fmt.Errorf("legacy resource ownership missing")
			}
		}
		if _, err = h.sendNodeCommand(item.NodeID, "DeleteService", map[string]interface{}{"services": names}, false, true); err != nil {
			return result, err
		}
		if err = h.repo.ClearPeerShareResourceLegacyNames(item.ShareID, item.Kind, item.OriginalName); err != nil {
			return result, err
		}
	}
	if item.DesiredState == "deleted" || item.DesiredState == "paused" {
		// The provider retains the paused configuration durably. Keep the
		// listener absent on reconnect instead of briefly starting it before
		// sending a second pause command. Resume reapplies the saved config.
		result, err = h.sendNodeCommand(item.NodeID, peerResourceDeleteCommand(item.Kind), peerResourceDeletePayload(item), false, true)
	} else {
		var config map[string]interface{}
		if err = json.Unmarshal([]byte(item.Config), &config); err != nil {
			return result, err
		}
		if item.Kind == "service" {
			// F-UDP-4 / VB2-002 + LB-F-P3-1：下发前归一化 udp listener
			// metadata 与 relay handler bind，防对端推送的裸 relay
			// 服务重建 F-UDP-1 坏形态、或 SOCKS5-associate 被门控拒。
			normalizePeerServiceUDPListenerMetadata(config)
			result, err = h.sendNodeCommand(item.NodeID, "UpdateService", []interface{}{config}, false, false)
		} else {
			suffix := "Limiters"
			key := "limiter"
			if item.Kind == "chain" {
				suffix = "Chains"
				key = "chain"
			}
			if item.Kind == "climiter" {
				suffix = "CLimiters"
			}
			result, err = h.sendNodeCommand(item.NodeID, "Add"+suffix, config, false, false)
			if err != nil && isAlreadyExistsMessage(err.Error()) {
				result, err = h.sendNodeCommand(item.NodeID, "Update"+suffix, map[string]interface{}{key: item.RuntimeName, "data": config}, false, false)
			}
		}
	}
	if err != nil {
		return result, err
	}
	if item.Kind == "service" && item.DesiredState == "deleted" {
		// TCP and UDP may share one reservation. Release only after all variants
		// have an acknowledged tombstone.
		items, listErr := h.repo.ListPeerShareResourcesByNode(item.NodeID)
		if listErr != nil {
			return result, listErr
		}
		base := normalizeForwardRuntimeServiceName(item.OriginalName)
		for _, other := range items {
			if other.ShareID == item.ShareID && other.Kind == "service" && other.OriginalName != item.OriginalName && normalizeForwardRuntimeServiceName(other.OriginalName) == base && (other.DesiredState != "deleted" || other.Applied == 0) {
				return result, h.repo.MarkPeerShareResourceApplied(item.ShareID, item.Kind, item.OriginalName)
			}
		}
		legacyBase := item.LegacyServiceBase
		if legacyBase == "" {
			for _, other := range items {
				if other.ShareID == item.ShareID && normalizeForwardRuntimeServiceName(other.OriginalName) == base && other.LegacyServiceBase != "" {
					legacyBase = other.LegacyServiceBase
					break
				}
			}
		}
		if legacyBase != "" && item.ReleaseLegacyFamily {
			share, loadErr := h.repo.GetPeerShare(item.ShareID)
			if loadErr != nil {
				return result, loadErr
			}
			if share == nil {
				return result, fmt.Errorf("share ownership missing")
			}
			if _, checkErr := h.peerShareLegacyServiceNames(share, legacyBase); checkErr != nil {
				return result, checkErr
			}
			if _, deleteErr := h.sendNodeCommand(item.NodeID, "DeleteService", map[string]interface{}{"services": buildForwardServiceDeleteNames([]string{legacyBase})}, false, true); deleteErr != nil {
				return result, deleteErr
			}
		}
		if legacyBase != "" && !item.ReleaseLegacyFamily {
			return result, h.repo.MarkPeerShareResourceApplied(item.ShareID, item.Kind, item.OriginalName)
		}
		runtimes, listErr := h.repo.ListActivePeerShareRuntimesByShareID(item.ShareID)
		if listErr != nil {
			return result, listErr
		}
		return result, h.repo.WithPeerShareResourceTransaction(func(tx *repo.Repository) error {
			for _, runtime := range runtimes {
				sid, original, scoped := parsePeerShareServiceName(runtime.ServiceName)
				ownedScoped := scoped && sid == item.ShareID && normalizeForwardRuntimeServiceName(original) == base
				ownedLegacy := !scoped && runtime.Role == "forward" && item.ReleaseLegacyFamily && legacyBase != "" && normalizeForwardRuntimeServiceName(runtime.ServiceName) == legacyBase
				if ownedScoped || ownedLegacy {
					if err = tx.CompletePeerShareRuntimeRelease(runtime.ID); err != nil {
						return err
					}
				}
			}
			if legacyBase != "" && item.ReleaseLegacyFamily {
				if err := tx.ClearPeerShareResourceLegacyFamily(item.ShareID, legacyBase); err != nil {
					return err
				}
			}
			return tx.MarkPeerShareResourceApplied(item.ShareID, item.Kind, item.OriginalName)
		})
	}
	return result, h.repo.MarkPeerShareResourceApplied(item.ShareID, item.Kind, item.OriginalName)
}

func orderPeerShareResources(items []repo.PeerShareResource) {
	rank := func(item repo.PeerShareResource) int {
		if item.DesiredState == "deleted" {
			if item.Kind == "service" {
				return 0
			}
			return 1
		}
		if item.Kind == "service" {
			return 4
		}
		if item.Kind == "chain" {
			return 3
		}
		return 2
	}
	sort.SliceStable(items, func(i, j int) bool { return rank(items[i]) < rank(items[j]) })
}

// revalidateStoredPeerShareResources 是 FIX2-B（V5-3 残余风险）的启动
// 复检：写入面校验只在 preparePeerResourceCommand 写入时执行，升级前
// 已落库的 peer_share_resource 期望状态不经复检——若提供方在升级前
// 已被恶意对端植入 file/sshd 等危险配置，升级后节点重连对账会原样
// 物化。本函数在后台对账循环启动前把全部存量 active 的期望状态补跑
// 一遍与写入面相同的校验全集（validatePeerResourceConfig：引用＋
// 类型＋本机路径，limiter/climiter 的 loader 检查同在全集内——B6-06，
// 此前本函数只重跑类型白名单且整体跳过 limiter，引用面违规与证书
// 路径植入都能存活）：不过者置 paused（对账按 paused 语义下发删除、
// 监听不拉起，配置保留待管理员处置）并逐条告警，绝不静默沿用；配置
// JSON 不可解析同按 fail-closed 置 paused（对账侧解不出同样无法物
// 化）。deleted 墓碑不在复检面。paused 的存量配置要复活必须过
// preparePeerResourceCommand 里 resume 的同一全集复检（B6-06）。
func (h *Handler) revalidateStoredPeerShareResources() {
	if h == nil || h.repo == nil {
		return
	}
	items, err := h.repo.ListAllPeerShareResources()
	if err != nil {
		log.Printf("federation startup revalidation: list stored resources failed: %v", err)
		return
	}
	var changed []repo.PeerShareResource
	for i := range items {
		item := items[i]
		if item.DesiredState != "active" {
			continue
		}
		switch item.Kind {
		case "service", "chain", "limiter", "climiter":
		default:
			continue
		}
		if strings.TrimSpace(item.Config) == "" {
			continue
		}
		var config interface{}
		if err := json.Unmarshal([]byte(item.Config), &config); err != nil {
			log.Printf("federation startup revalidation: pausing share_id=%d %s %q: stored config is not valid JSON: %v", item.ShareID, item.Kind, item.OriginalName, err)
			item.DesiredState = "paused"
			changed = append(changed, item)
			continue
		}
		if err := validatePeerResourceConfig(config, item.Kind); err != nil {
			log.Printf("federation startup revalidation: pausing share_id=%d %s %q: %v", item.ShareID, item.Kind, item.OriginalName, err)
			item.DesiredState = "paused"
			changed = append(changed, item)
		}
	}
	if len(changed) == 0 {
		return
	}
	if err := h.repo.SavePeerShareResources(changed); err != nil {
		log.Printf("federation startup revalidation: persist paused states failed: %v", err)
		return
	}
	log.Printf("federation startup revalidation: paused %d stored peer resource(s) failing write-time validation; review before resuming", len(changed))
}

func (h *Handler) reconcilePeerShareResourcesOnNode(nodeID int64) error {
	return h.reconcilePeerShareResources(nodeID, false)
}
func (h *Handler) retryPendingPeerShareResourcesOnNode(nodeID int64) error {
	return h.reconcilePeerShareResources(nodeID, true)
}
func (h *Handler) reconcilePeerShareResources(nodeID int64, pendingOnly bool) error {
	h.peerResourceMu.Lock()
	defer h.peerResourceMu.Unlock()
	items, err := h.repo.ListPeerShareResourcesByNode(nodeID)
	if err != nil {
		return err
	}
	runtimes, err := h.repo.ListActivePeerShareRuntimesByNode(nodeID)
	if err != nil {
		return err
	}
	pending := map[string]bool{}
	for _, runtime := range runtimes {
		if runtime.ReleasePending != 0 {
			sid, name, ok := parsePeerShareServiceName(runtime.ServiceName)
			if ok {
				pending[fmt.Sprintf("%d:%s", sid, normalizeForwardRuntimeServiceName(name))] = true
			}
		}
	}
	groups := map[int64][]repo.PeerShareResource{}
	var ids []int64
	for _, item := range items {
		if _, ok := groups[item.ShareID]; !ok {
			ids = append(ids, item.ShareID)
		}
		groups[item.ShareID] = append(groups[item.ShareID], item)
	}
	var failures []error
	for _, shareID := range ids {
		share, err := h.repo.GetPeerShare(shareID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		expired := share == nil || share.IsActive != 1 || (share.ExpiryTime > 0 && share.ExpiryTime <= time.Now().UnixMilli()) || isPeerShareFlowExceeded(share)
		group := groups[shareID]
		var changed []repo.PeerShareResource
		for i := range group {
			item := &group[i]
			if (expired || (item.Kind == "service" && pending[fmt.Sprintf("%d:%s", item.ShareID, normalizeForwardRuntimeServiceName(item.OriginalName))])) && (item.DesiredState != "deleted" || item.Applied == 0 || item.LegacyServiceBase != "") {
				item.DesiredState = "deleted"
				item.ReleaseLegacyFamily = true
				item.Applied = 0
				changed = append(changed, *item)
			}
		}
		if err := h.repo.SavePeerShareResources(changed); err != nil {
			failures = append(failures, err)
			continue
		}
		orderPeerShareResources(group)
		dependencyFailed := false
		deletionFailed := false
		for _, item := range group {
			if item.Applied == 1 && (pendingOnly || item.DesiredState == "deleted") {
				continue
			}
			if dependencyFailed && item.Kind == "service" && item.DesiredState != "deleted" {
				continue
			}
			if deletionFailed && item.Kind != "service" && item.DesiredState == "deleted" {
				continue
			}
			if _, err := h.applyPeerShareResource(item); err != nil {
				failures = append(failures, fmt.Errorf("share %d %s %s: %w", item.ShareID, item.Kind, item.OriginalName, err))
				if item.Kind != "service" {
					dependencyFailed = true
				}
				if item.DesiredState == "deleted" {
					deletionFailed = true
				}
			}
		}
	}
	return errors.Join(failures...)
}

func (h *Handler) releasePeerShareResources(shareID int64) error {
	h.peerResourceMu.Lock()
	defer h.peerResourceMu.Unlock()
	share, err := h.repo.GetPeerShare(shareID)
	if err != nil {
		return err
	}
	if share == nil {
		return nil
	}
	all, err := h.repo.ListPeerShareResourcesByNode(share.NodeID)
	if err != nil {
		return err
	}
	var items []repo.PeerShareResource
	for _, item := range all {
		if item.ShareID == shareID && !(item.DesiredState == "deleted" && item.Applied == 1 && item.LegacyServiceBase == "") {
			item.DesiredState = "deleted"
			item.ReleaseLegacyFamily = true
			item.Applied = 0
			items = append(items, item)
		}
	}
	if err = h.repo.SavePeerShareResources(items); err != nil {
		return err
	}
	orderPeerShareResources(items)
	for _, item := range items {
		if _, err = h.applyPeerShareResource(item); err != nil {
			return err
		}
	}
	return nil
}

// A legacy name is eligible for migration only when its runtime registration
// proves unique ownership. An identically numbered local forward is ambiguous.
func (h *Handler) peerShareLegacyServiceNames(share *repo.PeerShare, original string) ([]string, error) {
	base := normalizeForwardRuntimeServiceName(original)
	runtimes, err := h.repo.ListActiveForwardPeerShareRuntimesByNodeAndServiceName(share.NodeID, base)
	if err != nil {
		return nil, err
	}
	owned := false
	for _, runtime := range runtimes {
		if runtime.ShareID != share.ID {
			return nil, fmt.Errorf("legacy resource %q has ambiguous ownership", original)
		}
		if runtime.ReleasePending != 0 {
			return nil, fmt.Errorf("runtime release is pending")
		}
		owned = true
	}
	if !owned {
		resources, err := h.repo.ListPeerShareResourcesByNode(share.NodeID)
		if err != nil {
			return nil, err
		}
		for _, resource := range resources {
			if resource.ShareID == share.ID && resource.LegacyServiceBase == base {
				owned = true
				break
			}
		}
	}
	if !owned {
		return nil, nil
	}
	if id, _, _, ok := parseFlowServiceIDs(base); ok {
		local, err := h.repo.GetForwardRecord(id)
		if err != nil {
			return nil, err
		}
		if local != nil {
			return nil, fmt.Errorf("legacy resource %q collides with a local forward", original)
		}
	}
	// Only delete the requested transport. Other variants remain registered to
	// their legacy runtime until they are independently migrated.
	return []string{original}, nil
}

// releasePeerShareForwardRuntimeResources removes exact persisted transport
// names belonging to a single forward reservation, retaining failed tombstones.
func (h *Handler) releasePeerShareForwardRuntimeResources(runtime *repo.PeerShareRuntime) error {
	h.peerResourceMu.Lock()
	defer h.peerResourceMu.Unlock()
	shareID, original, ok := parsePeerShareServiceName(runtime.ServiceName)
	if !ok || shareID != runtime.ShareID {
		return fmt.Errorf("runtime is not a scoped shared forward")
	}
	all, err := h.repo.ListPeerShareResourcesByNode(runtime.NodeID)
	if err != nil {
		return err
	}
	var items []repo.PeerShareResource
	for _, item := range all {
		if item.ShareID == runtime.ShareID && item.Kind == "service" && normalizeForwardRuntimeServiceName(item.OriginalName) == normalizeForwardRuntimeServiceName(original) {
			item.DesiredState = "deleted"
			item.ReleaseLegacyFamily = true
			item.Applied = 0
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return fmt.Errorf("shared forward resource ownership is missing")
	}
	if err = h.repo.SavePeerShareResources(items); err != nil {
		return err
	}
	for _, item := range items {
		if _, err = h.applyPeerShareResource(item); err != nil {
			return err
		}
	}
	return nil
}

// D3-03: peer 服务/链配置的类型白名单。validatePeerResourceReferences
// 只堵了注册表引用与 shell hook（黑名单），handler/listener 类型本身
// 此前无任何限制——fork 注册表里的 file（任意目录 FileServer）、sshd、
// tun、dns、redirect 等高危类型可由持 token 对端原样下发到提供方节点
// （R5 真机坐实：file/sshd 被校验层接受并落库为期望状态）。
//
// 白名单按「面板自身构建路径实际会下发的类型全集」反查得出：
//   - 转发服务（buildForwardServiceConfigs）：handler tcp/udp
//     （forward 是同一处理器的注册别名）、listener tcp/udp；
//   - 隧道链服务（buildTunnelChainServiceConfig）：handler relay、
//     listener 为隧道协议；
//   - 转发链（buildTunnelChainConfig / buildFederationMiddleChainConfig）：
//     hop 节点 connector 恒 relay、dialer 为隧道协议。
//
// 隧道协议全集与前端 tunnel 协议枚举及 fork 注册表一致：
// tcp/tls/mtls/mtcp/kcp/ws/wss/mws/mwss。
var peerServiceHandlerTypes = map[string]bool{
	"tcp": true, "udp": true, "forward": true, "relay": true,
}

// listener/dialer 放行集合：转发服务的 listener 是 tcp/udp（按转发
// 协议），隧道链服务 listener 与链 dialer 是隧道协议全集。
var peerTunnelTransportTypes = map[string]bool{
	"tcp": true, "udp": true, "tls": true, "mtls": true, "mtcp": true, "kcp": true,
	"ws": true, "wss": true, "mws": true, "mwss": true,
}

var peerChainConnectorTypes = map[string]bool{
	"relay": true,
}

// F-UDP-4: 隧道段协议在本产品里同时充当链服务的 listener 类型与
// 上一跳的 dialer 类型，故其合法集就是 D3-03 的 listener/dialer
// 放行集 peerTunnelTransportTypes（转发 listener 的 tcp/udp ∪ 隧道
// 协议全集）。注意不要改用前端隧道协议枚举或 D3-03 注释里的九值
// 「隧道协议集」——那是不含 udp 的 UI/注释口径，而 udp 段经 F-UDP-1
// 修复后是合法形态。此前 reserve-port / apply-role / tunnel/create
// 与本地隧道创建面都对 protocol 零校验，任意字符串直达 agent 解析，
// 注册表内的 sshd/dns/tun 等设计外类型可被实例化（与 D3-03 当年为
// resources 路径封堵的是同一类型集）。
// 校验按小写归一后查表，与 walkPeerResourceTypes 的 type 归一同
// 口径；入口同时把存量值小写化（normalizeTunnelProtocol），使生成
// 的 listener/dialer type 恒为注册表规范名。
func isAllowedTunnelProtocol(protocol string) bool {
	return peerTunnelTransportTypes[strings.ToLower(strings.TrimSpace(protocol))]
}

func validateTunnelProtocol(protocol string) error {
	if strings.TrimSpace(protocol) == "" {
		return fmt.Errorf("protocol is required")
	}
	if !isAllowedTunnelProtocol(protocol) {
		return fmt.Errorf("protocol %q is not allowed", strings.TrimSpace(protocol))
	}
	return nil
}

func normalizeTunnelProtocol(protocol string) string {
	return strings.ToLower(strings.TrimSpace(protocol))
}

// F-UDP-4 / VB2-002: resources 透传面只过 D3-03 类型白名单、不验
// listener metadata——对端推送的 {handler:relay, listener:{type:udp}}
// 服务若不带 keepAlive，会在提供方节点重建 F-UDP-1 的坏形态（会话
// 首写后自毁、后续帧撞 bad version）。下发前按面板自身生成口径
// 归一化：udp listener 缺 keepAlive 时补 true、缺 ttl 时补 30s；
// handler 为 relay 时 keepAlive 强制为 true（relay-over-UDP 无
// keepAlive 不是合法形态，显式 false 同样归一）。只改下发副本，
// 存量 Config 原文不动；每次 apply 重演同一纯函数，结果确定。
//
// LB-F-P3-1: 同一透传面还缺 handler 侧的一半——relay handler 的
// BIND 命令（含 SOCKS5 UDP associate）在 x 侧被 metadata bind
// 门控，面板自身生成面已由 KU9/KU10 在两个 builder 无条件开启；
// 对端以 <1.7.7 生成或手工构造的 relay 服务缺 bind 时，经此下发
// 后 associate 会被逐跳以 "BIND is disabled" 拒掉，且存量重新
// apply 只是重演归一化、同样补不上。故在同一收口补齐：handler
// 为 relay 且 metadata 缺 bind 键（键名大小写不敏感判定）时置
// true，metadata 缺失则建；已有 bind（对端显式值）不动、其他键
// 合并保留。此归一与 listener 类型无关（BIND 门控不分监听协议），
// 故在 listener 门之前先行；非 relay 的 handler 逐字不变。
func normalizePeerServiceUDPListenerMetadata(config map[string]interface{}) {
	if handler, _ := config["handler"].(map[string]interface{}); handler != nil &&
		strings.EqualFold(strings.TrimSpace(asString(handler["type"])), "relay") {
		hmeta, _ := handler["metadata"].(map[string]interface{})
		if hmeta == nil {
			hmeta = map[string]interface{}{}
			handler["metadata"] = hmeta
		}
		hasBind := false
		for key := range hmeta {
			if strings.EqualFold(key, "bind") {
				hasBind = true
			}
		}
		if !hasBind {
			hmeta["bind"] = true
		}
	}
	listener, ok := config["listener"].(map[string]interface{})
	if !ok {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(asString(listener["type"])), "udp") {
		return
	}
	metadata, _ := listener["metadata"].(map[string]interface{})
	if metadata == nil {
		metadata = map[string]interface{}{}
		listener["metadata"] = metadata
	}
	handler, _ := config["handler"].(map[string]interface{})
	handlerType := strings.ToLower(strings.TrimSpace(asString(handler["type"])))
	hasKeepAlive, hasTTL := false, false
	for key := range metadata {
		if strings.EqualFold(key, "keepAlive") {
			hasKeepAlive = true
		}
		if strings.EqualFold(key, "ttl") {
			hasTTL = true
		}
	}
	if !hasKeepAlive || handlerType == "relay" {
		metadata["keepAlive"] = true
	}
	if !hasTTL {
		metadata["ttl"] = "30s"
	}
}

// validatePeerResourceTypes 递归走遍服务/链配置，凡出现 handler /
// listener / connector / dialer 段，其 type 必须落在对应白名单内；
// 段存在但 type 缺失同样拒绝（面板构建路径恒带 type，缺失即手工构造）。
// 只对 service/chain 两类资源生效（limiter 配置无此类段）。
func validatePeerResourceTypes(value interface{}, kind string) error {
	if kind != "service" && kind != "chain" {
		return nil
	}
	return walkPeerResourceTypes(value)
}

func walkPeerResourceTypes(value interface{}) error {
	switch v := value.(type) {
	case map[string]interface{}:
		for key, child := range v {
			var allowed map[string]bool
			var label string
			switch strings.ToLower(key) {
			case "handler":
				allowed, label = peerServiceHandlerTypes, "handler"
			case "listener":
				allowed, label = peerTunnelTransportTypes, "listener"
			case "connector":
				allowed, label = peerChainConnectorTypes, "connector"
			case "dialer":
				allowed, label = peerTunnelTransportTypes, "dialer"
			default:
				if err := walkPeerResourceTypes(child); err != nil {
					return err
				}
				continue
			}
			section, ok := child.(map[string]interface{})
			if !ok {
				return fmt.Errorf("shared %s configuration is invalid", label)
			}
			typ := strings.ToLower(strings.TrimSpace(asString(section["type"])))
			if typ == "" {
				return fmt.Errorf("shared %s type is required", label)
			}
			if !allowed[typ] {
				return fmt.Errorf("shared %s type %q is not allowed", label, typ)
			}
			if err := walkPeerResourceTypes(child); err != nil {
				return err
			}
		}
	case []interface{}:
		for _, child := range v {
			if err := walkPeerResourceTypes(child); err != nil {
				return err
			}
		}
	}
	return nil
}

// validatePeerResourceConfig 是对端资源配置的校验全集单点：引用面、
// 类型白名单与本机路径面三校验同律。写入面
// （preparePeerResourceCommand）、存量启动复检与 resume 复活门共用
// 本函数，任何一面加严自动覆盖其余两面（B6-06）。
func validatePeerResourceConfig(value interface{}, kind string) error {
	if err := validatePeerResourceReferences(value, kind); err != nil {
		return err
	}
	if err := validatePeerResourceTypes(value, kind); err != nil {
		return err
	}
	return validatePeerResourceLocalPaths(value)
}

// validatePeerResourceLocalPaths 是 B6-01 的路径面校验：对端资源配置
// 不得携带提供方节点本机文件路径。普查全集（go-gost/x 机械导出）：
// TLSConfig（x/config/config.go，listener/handler/connector/dialer
// 四段共用）的 certFile/keyFile/caFile 三字段是唯一的证书路径字段；
// handler metadata 的 mitm.certFile/mitm.caCertFile/mitm.keyFile/
// mitm.caKeyFile（x/handler/relay 等十处 metadata.go）经
// LoadX509KeyPair 以 agent 的 root 身份加载。面板自身构建的联邦/
// 转发/隧道服务从不下发 tls 段或 mitm.* metadata，此面纯为对端可加。
// 命中非空字符串值即 fail-closed 拒绝；空值在 gost 侧不构成加载，
// 与引用/hook 校验的空值放行口径一致。键名大小写不敏感（对端 JSON
// 的 "CertFile" 形态经 gost 解码同样落到同一字段）。mitm.alpn/
// mitm.bypass 非路径键，不在此面。
//
// B6-01 同族补全（同机制再普查，凡有 agent 侧文件消费证据的键全收）：
//   - kcp 配置文件：metadata 的 kcp.configFile/configFile/c 三别名
//     （x/listener/kcp、x/dialer/kcp 的 metadata.go）经
//     kcp.ParseFromFile 以 os.Open 读取；kcp 在对端合法类型集内，
//     此族在写入面即为活洞。单字母别名 c 只在 metadata 上下文内
//     才是路径键（其余同名键不归本面）。
//   - SSH 密钥材料：metadata 的 privateKeyFile（x/dialer/ssh、
//     x/dialer/sshd、x/listener/ssh、x/listener/sshd 的 metadata.go
//     经 os.ReadFile 后 ParsePrivateKey）与 authorizedKeys
//     （x/listener/ssh、x/listener/sshd 经 ParseAuthorizedKeysFile
//     读取）。ssh/sshd 类型当前不在对端合法集内，此族按路径面独立
//     于类型面的口径一并收：白名单日后放宽不得随之敞开。
//   - file handler 服务目录：metadata 的 file.dir/dir
//     （x/handler/file 的 metadata.go）直达 http.FileServer 的根
//     目录；裸 dir 只在 metadata 上下文内判。handler 类型 file
//     当前不在合法集内，同上纵深。
//   - http 诱饵文件：metadata 的 probeResist/probe_resist/
//     probeResistance（x/handler/http、http2 的 metadata.go 解析，
//     http3 仅解析未消费）在值形为 file:<path> 时经 os.Open 读取
//     并把内容当诱饵响应回给未认证探测者（x/handler/http、
//     http2 的 handler.go）；web:/host: 值是网络目标、不在本面。
//
//   - 网络命名空间（R2-07-01）：metadata 的 netns/netns.out
//     （x/config/parsing 的 MDKeyNetns/MDKeyNetnsOut）经
//     parsing/service 以 netns.GetFromPath（"/" 前缀即任意路径打开）
//     /GetFromName 打开、netns.Set 后在该命名空间内建监听，出站经
//     netns.out 同理；hop metadata 的 netns 被 parsing/hop 强制写到
//     每个 node，NodeConfig.Netns 字段经 NetnsTransportOption 在连接
//     路径同样消费。四入形（service metadata 两键、hop metadata、
//     node 字段）同根，裸键名任意位置非空即拒。面板自身从不下发
//     netns（后端全仓零命中），无合法面可误伤。
//
// R2-07-01 续普查（换谓词：以 x parsing/metadata 层实际读取的键全集
// 与主机资源消费点反推全集）：interface（主机网卡名，parsing/service
// 与 parsing/hop 读取）有意不在本面——它是节点记录上的运营配置字段
// （InterfaceName），面板 federation 生命周期以提供方节点自身配置
// 合法下发，与对端任意携带的 netns 性质不同，其 peer 面口径另案待
// 主控裁定，不得在此顺手一律拒（会打断 exit 接口绑定功能）。
//
// limiter 顶层 file 加载器与 hop loader 已由引用面校验拒绝，不在
// 本面重复登记。URL 路径键（*.path/pathPrefix 等）不落本机文件，
// 不在本面。
func validatePeerResourceLocalPaths(value interface{}) error {
	return walkPeerResourceLocalPaths(value, false)
}

func walkPeerResourceLocalPaths(value interface{}, inMetadata bool) error {
	switch v := value.(type) {
	case map[string]interface{}:
		for key, child := range v {
			lower := strings.ToLower(key)
			if s, ok := child.(string); ok && strings.TrimSpace(s) != "" {
				switch {
				case lower == "certfile" || lower == "keyfile" || lower == "cafile" ||
					(strings.HasPrefix(lower, "mitm.") && strings.Contains(lower, "file")):
					return fmt.Errorf("shared resources must not reference node-local certificate files: %s", key)
				case lower == "configfile" || lower == "kcp.configfile":
					return fmt.Errorf("shared resources must not reference node-local config files: %s", key)
				case lower == "netns" || lower == "netns.out":
					return fmt.Errorf("shared resources must not reference node-local network namespaces: %s", key)
				case lower == "privatekeyfile":
					return fmt.Errorf("shared resources must not reference node-local private key files: %s", key)
				case lower == "authorizedkeys":
					return fmt.Errorf("shared resources must not reference node-local authorized keys files: %s", key)
				case lower == "file.dir" || (inMetadata && lower == "dir"):
					return fmt.Errorf("shared resources must not reference node-local directories: %s", key)
				case inMetadata && lower == "c":
					return fmt.Errorf("shared resources must not reference node-local config files: %s", key)
				case (lower == "proberesist" || lower == "probe_resist" || lower == "proberesistance") &&
					probeResistanceFileValue(s):
					return fmt.Errorf("shared resources must not reference node-local files: %s", key)
				}
			}
			if err := walkPeerResourceLocalPaths(child, inMetadata || lower == "metadata"); err != nil {
				return err
			}
		}
	case []interface{}:
		for _, child := range v {
			if err := walkPeerResourceLocalPaths(child, inMetadata); err != nil {
				return err
			}
		}
	}
	return nil
}

// probeResistanceFileValue 判 metadata 诱饵值是否为 file: 形态（类型
// 段在首个冒号前、大小写不敏感）：x/handler/http 与 http2 按
// "file:<path>" 以 os.Open 打开该文件并把内容回给探测者。
func probeResistanceFileValue(s string) bool {
	head, _, found := strings.Cut(s, ":")
	return found && strings.EqualFold(strings.TrimSpace(head), "file")
}

// Registry kinds without an owned command API cannot be referenced by peers.
func validatePeerResourceReferences(value interface{}, kind string) error {
	var walk func(interface{}) error
	walk = func(value interface{}) error {
		switch v := value.(type) {
		case map[string]interface{}:
			for key, child := range v {
				switch strings.ToLower(key) {
				case "auther", "authers", "admission", "admissions", "bypass", "bypasses", "resolver", "hosts", "rlimiter", "logger", "loggers", "observer", "recorders", "hop", "sd":
					if child != nil {
						empty := false
						switch x := child.(type) {
						case string:
							empty = strings.TrimSpace(x) == ""
						case []interface{}:
							empty = len(x) == 0
						}
						if !empty {
							return fmt.Errorf("unsupported shared registry reference: %s", key)
						}
					}
				// S-03: 联邦 peer 经 service metadata 的 shell hook 可拿到宿主节点 root。
				// 面板自身从不下发这些键，此处 fail-closed 直接拒绝。
				case "preup", "predown", "postup", "postdown":
					if child != nil {
						empty := false
						switch x := child.(type) {
						case string:
							empty = strings.TrimSpace(x) == ""
						case []interface{}:
							empty = len(x) == 0
						}
						if !empty {
							return fmt.Errorf("shell hooks are not allowed in shared resources: %s", key)
						}
					}
				case "forwarder":
					if f, ok := child.(map[string]interface{}); ok {
						for field, value := range f {
							if strings.EqualFold(field, "name") && strings.TrimSpace(asString(value)) != "" {
								return fmt.Errorf("named shared forwarder references are unsupported")
							}
						}
					}
				case "hops":
					for _, hop := range asMapSlice(child) {
						if _, ok := hop["nodes"]; !ok {
							return fmt.Errorf("shared chains require inline hop nodes")
						}
						for _, loader := range []string{"file", "redis", "http", "plugin"} {
							if hop[loader] != nil {
								return fmt.Errorf("shared hop loaders are unsupported")
							}
						}
					}
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []interface{}:
			for _, child := range v {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if kind == "limiter" || kind == "climiter" {
		if config, ok := value.(map[string]interface{}); ok {
			for _, loader := range []string{"file", "redis", "http", "plugin"} {
				if config[loader] != nil {
					return fmt.Errorf("shared limiter loaders are unsupported")
				}
			}
		}
	}
	return walk(value)
}
