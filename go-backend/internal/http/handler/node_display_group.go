package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go-backend/internal/http/response"
)

// 节点展示分组（纯展示功能）v2 交互面（用户 2026-11-09 23:34 改
// 版）：建组→多选→移入一步到位，取代 v1「逐台编辑节点填标签」的
// 主流程。本文件四个端点：
//   - node/batch-set-group：批量移入/移出（逐条回执，M-0051 口径）；
//   - node/group/create：新建分组（组名注册进顺序名单，空组即存在）；
//   - node/group/rename：改名（成员标签级联＋名单原位替换）；
//   - node/group/delete：删组（成员回落未分组＋名单摘除）。
// 分组名单仍存面板配置键 node_display_group_order（值＝JSON 名称
// 数组，v1 起即为段序名单；v2 起兼作分组登记册：名单内而无成员
// 的组是合法空组——节点页不渲染空段，但移入菜单与管理面可见）。
// 编辑节点的 displayGroup 字段口径不变（见 mutations.go）。

const nodeGroupOrderConfigKey = "node_display_group_order"

// nodeGroupUngroupedLabel 是未分组段的固定标题，与前端
// UNGROUPED_LABEL 同值：分组名与之相同会出两个同名段，建组/改名
// 面一律拒绝。
const nodeGroupUngroupedLabel = "未分组"

// parseNodeGroupOrderValue 解析名单值（JSON 名称数组）：trim、去
// 重、丢空项；坏值降级为空名单（与前端 parseNodeGroupOrder 同口
// 径，读面不因存量坏值失败）。
func parseNodeGroupOrderValue(raw string) []string {
	out := []string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return out
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		label := strings.TrimSpace(item)
		if label == "" {
			continue
		}
		if _, dup := seen[label]; dup {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	return out
}

// loadNodeGroupRegistry 读分组名单：配置键不存在＝空名单（非错
// 误）；其余读失败如实回错——写面若把读失败当空名单会把整份名单
// 覆写掉。
func (h *Handler) loadNodeGroupRegistry() ([]string, error) {
	raw, err := h.repo.GetViteConfigValue(nodeGroupOrderConfigKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []string{}, nil
		}
		return nil, err
	}
	return parseNodeGroupOrderValue(raw), nil
}

func (h *Handler) saveNodeGroupRegistry(order []string) error {
	value, err := json.Marshal(order)
	if err != nil {
		return err
	}
	return h.repo.UpsertConfig(nodeGroupOrderConfigKey, string(value), time.Now().UnixMilli())
}

func nodeGroupNameInRegistry(registry []string, name string) bool {
	for _, item := range registry {
		if item == name {
			return true
		}
	}
	return false
}

// nodeGroupExists 分组存在口径：名单在册，或仍有节点在用该标签
// （v1 自由文本留下的名单外标签同样是真分组，可改名/可删）。
func nodeGroupExists(registry []string, inUse map[string]int64, name string) bool {
	return nodeGroupNameInRegistry(registry, name) || inUse[name] > 0
}

// validateNodeGroupName 建组/改名的组名校验：归一同节点标签口径
// （trim＋100 rune），另加三门——非空、不用未分组保留名、不与既
// 有分组重名（exclude 为改名时的原名，自身不算重名）。
func validateNodeGroupName(raw string, registry []string, inUse map[string]int64, exclude string) (string, error) {
	name, err := normalizeNodeDisplayGroup(raw)
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", errors.New("分组名称不能为空")
	}
	if name == nodeGroupUngroupedLabel {
		return "", errors.New("「未分组」是保留名称，不能用作分组名")
	}
	if name != exclude && nodeGroupExists(registry, inUse, name) {
		return "", fmt.Errorf("分组「%s」已存在", name)
	}
	return name, nil
}

type nodeGroupCreateResult struct {
	Name  string   `json:"name"`
	Order []string `json:"order"`
}

type nodeGroupRenameResult struct {
	OldName      string   `json:"oldName"`
	NewName      string   `json:"newName"`
	UpdatedCount int64    `json:"updatedCount"`
	Order        []string `json:"order"`
}

type nodeGroupDeleteResult struct {
	Name         string   `json:"name"`
	UpdatedCount int64    `json:"updatedCount"`
	Order        []string `json:"order"`
}

// nodeGroupCreate 新建分组：只登记名单（空组即存在、零节点写），
// 成员靠批量移入或编辑节点下拉加入。
func (h *Handler) nodeGroupCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	registry, err := h.loadNodeGroupRegistry()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	inUse, err := h.repo.CountNodesByDisplayGroup()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	name, vErr := validateNodeGroupName(req.Name, registry, inUse, "")
	if vErr != nil {
		response.WriteJSON(w, response.ErrDefault(vErr.Error()))
		return
	}
	registry = append(registry, name)
	if err := h.saveNodeGroupRegistry(registry); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(nodeGroupCreateResult{Name: name, Order: registry}))
}

// nodeGroupRename 分组改名：名单先落（原位替换；在册外标签改名则
// 新名追加进名单），成员标签随后级联——名单先行保证级联失败时只
// 多一个空组，不留「标签已改名、名单仍是旧名」的半成品。
func (h *Handler) nodeGroupRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		OldName string `json:"oldName"`
		NewName string `json:"newName"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	oldName := strings.TrimSpace(req.OldName)
	if oldName == "" {
		response.WriteJSON(w, response.ErrDefault("分组名称不能为空"))
		return
	}
	registry, err := h.loadNodeGroupRegistry()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	inUse, err := h.repo.CountNodesByDisplayGroup()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if !nodeGroupExists(registry, inUse, oldName) {
		response.WriteJSON(w, response.ErrDefault("分组不存在"))
		return
	}
	newName, vErr := validateNodeGroupName(req.NewName, registry, inUse, oldName)
	if vErr != nil {
		response.WriteJSON(w, response.ErrDefault(vErr.Error()))
		return
	}
	if newName == oldName {
		response.WriteJSON(w, response.ErrDefault("新名称与原名称相同"))
		return
	}
	next := make([]string, 0, len(registry)+1)
	replaced := false
	for _, item := range registry {
		if item == oldName {
			next = append(next, newName)
			replaced = true
			continue
		}
		next = append(next, item)
	}
	if !replaced {
		next = append(next, newName)
	}
	if err := h.saveNodeGroupRegistry(next); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	updated, err := h.repo.RenameNodeDisplayGroup(oldName, newName)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(nodeGroupRenameResult{
		OldName:      oldName,
		NewName:      newName,
		UpdatedCount: updated,
		Order:        next,
	}))
}

// nodeGroupDelete 删除分组：名单先摘除，成员标签随后清回未分组。
func (h *Handler) nodeGroupDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		response.WriteJSON(w, response.ErrDefault("分组名称不能为空"))
		return
	}
	registry, err := h.loadNodeGroupRegistry()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	inUse, err := h.repo.CountNodesByDisplayGroup()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if !nodeGroupExists(registry, inUse, name) {
		response.WriteJSON(w, response.ErrDefault("分组不存在"))
		return
	}
	next := make([]string, 0, len(registry))
	for _, item := range registry {
		if item != name {
			next = append(next, item)
		}
	}
	if err := h.saveNodeGroupRegistry(next); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	updated, err := h.repo.ClearNodeDisplayGroup(name)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(nodeGroupDeleteResult{
		Name:         name,
		UpdatedCount: updated,
		Order:        next,
	}))
}

// nodeBatchSetGroup 批量移入/移出分组：group 归一后逐条落列，逐
// 条聚合回执（M-0051 口径）——成功/失败分别计数、失败带 id＋原
// 因，部分失败不许报全成功。空 group＝移出回未分组。目标组名不
// 要求在册（名单是偏好不是登记册的口径不变：在册外标签按名序
// 排在名单之后渲染，管理面亦可对其改名/删组）。
func (h *Handler) nodeBatchSetGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		IDs   []int64 `json:"ids"`
		Group string  `json:"group"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if len(req.IDs) == 0 {
		response.WriteJSON(w, response.ErrDefault("ids不能为空"))
		return
	}
	ids := uniqueInt64s(req.IDs)
	if !checkBatchIDsCap(w, len(ids)) {
		return
	}
	group, gErr := normalizeNodeDisplayGroup(req.Group)
	if gErr != nil {
		response.WriteJSON(w, response.ErrDefault(gErr.Error()))
		return
	}
	success := 0
	fail := 0
	failures := make([]batchFailureDetail, 0)
	for _, id := range ids {
		if id <= 0 {
			fail++
			failures = appendBatchFailureReason(failures, id, "", "节点ID无效")
			continue
		}
		rows, err := h.repo.SetNodeDisplayGroup(id, group)
		if err != nil {
			fail++
			failures = appendBatchFailureReason(failures, id, "", "设置分组失败："+err.Error())
			continue
		}
		if rows == 0 {
			fail++
			failures = appendBatchFailureReason(failures, id, "", "节点不存在")
			continue
		}
		success++
	}
	response.WriteJSON(w, response.OK(batchOperationResult{SuccessCount: success, FailCount: fail, Failures: failures}))
}
