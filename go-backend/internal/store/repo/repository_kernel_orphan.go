package repo

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"go-backend/internal/store/model"
)

// FIX-CASCADE（R2-E1-09 子形态 b）：强删内核（Force、下发移除未
// 确认）后，agent 侧孤儿内核的回收原本只靠重连 reconcile 与周期
// 轮询的未知类型分支；但周期轮询的节点集是 ListKernelNodeIDs
// （按现存内核行取节点），节点最后一个内核被强删后该节点退出
// 轮询集，在线孤儿可一直跑到下次重连。本标记把「疑似有孤儿内核
// 的节点」持久登记（vite_config 动态键族，与 pending_realm_settle
// 同形态），周期轮询把标记节点并入轮询集、以 evenIfNone 轮询清
// 理未知类型，agent 确认空集（状态回读可达且无未知类型）后清除。
// 标记是运行时台账不是配置：PolicyForConfig 默认 sensitive 口径
// 使其不进备份导出明文（与 pending_realm_settle 的豁免方向相反，
// 孤儿标记跨实例恢复无意义）。

const kernelOrphanRecheckKeyPrefix = "kernel_orphan_recheck:"

func kernelOrphanRecheckKey(nodeID int64) string {
	return kernelOrphanRecheckKeyPrefix + strconv.FormatInt(nodeID, 10)
}

// MarkKernelOrphanRecheck 登记一个疑似孤儿内核（类型并入既有
// 标记，累积去重）。删除节点时标记随级联清除（见
// DeleteNodeCascade），不会以已删节点 id 残留。
func (r *Repository) MarkKernelOrphanRecheck(nodeID int64, kernelType string, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if nodeID <= 0 || strings.TrimSpace(kernelType) == "" {
		return nil
	}
	types := map[string]struct{}{kernelType: {}}
	if cfg, err := r.GetConfigByName(kernelOrphanRecheckKey(nodeID)); err == nil && cfg != nil {
		var existing []string
		if json.Unmarshal([]byte(cfg.Value), &existing) == nil {
			for _, t := range existing {
				if strings.TrimSpace(t) != "" {
					types[t] = struct{}{}
				}
			}
		}
	}
	merged := make([]string, 0, len(types))
	for t := range types {
		merged = append(merged, t)
	}
	sort.Strings(merged)
	raw, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	return r.UpsertConfig(kernelOrphanRecheckKey(nodeID), string(raw), now)
}

// ListKernelOrphanRecheckNodeIDs 返回全部已登记孤儿重查的节点 id
// （周期轮询的并入集）。键名后缀解析失败的行跳过（不阻塞轮询）。
func (r *Repository) ListKernelOrphanRecheckNodeIDs() ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var names []string
	if err := r.db.Model(&model.ViteConfig{}).
		Where("name LIKE ?", kernelOrphanRecheckKeyPrefix+"%").
		Pluck("name", &names).Error; err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		id, err := strconv.ParseInt(strings.TrimPrefix(name, kernelOrphanRecheckKeyPrefix), 10, 64)
		if err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// KernelOrphanRecheckTypes 返回某节点已登记的疑似孤儿内核类型
// （未登记时返回 nil）。
func (r *Repository) KernelOrphanRecheckTypes(nodeID int64) ([]string, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	cfg, err := r.GetConfigByName(kernelOrphanRecheckKey(nodeID))
	if err != nil || cfg == nil {
		return nil, err
	}
	var types []string
	if err := json.Unmarshal([]byte(cfg.Value), &types); err != nil {
		return nil, nil
	}
	return types, nil
}

// ClearKernelOrphanRecheck 清除节点的孤儿重查标记（agent 确认空
// 集后调用；未登记时为空操作）。
func (r *Repository) ClearKernelOrphanRecheck(nodeID int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.DeleteConfig(kernelOrphanRecheckKey(nodeID))
}
