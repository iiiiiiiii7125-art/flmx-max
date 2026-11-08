package repo

import (
	"strconv"

	"gorm.io/gorm"

	"go-backend/internal/store/model"
)

// FIX-CASCADE（R2-E1 删除级联完整性一族）：删除入口的级联集必须与
// 引用全集对齐。本文件收口各删除级联共用的三件清理：
//   ① 转发整行删除集（deleteForwardCascadeSetTx）：Forward 本行 +
//      ForwardPort / nft 三表 / relay 与其授权 + UserQuota 的
//      PausedForwardIDs 文本清单摘除（E1-12）——单删转发、删隧道终局
//      墓碑、删节点零端口转发共用一处实现，禁止再逐入口手抄分叉。
//   ② 入站引用回写（cleanupInboundReferencesTx）：订阅/SubUser
//      的 inbound_ids CSV 在目标入站删除时同步摘除（E1-10，与备
//      份恢复面的悬空剥离 filterBackupRefIDList 同口径）；其他入
//      站的 outbound_id / split_rules.OutboundID 经逐面论证保留悬
//      空（fail-closed 语义载体，见函数注释点名登记）。
//   ③ 级联结果（NodeCascadeOutcome / TunnelCascadeOutcome）：把
//      「被终局处置的行」带回 handler 层点名披露（E1-01 / E1-11），
//      旧签名保留为丢弃结果的 wrapper，调用面逐个迁移。

// NodeCascadeOutcome 是 DeleteNodeCascade 的终局处置结果。
type NodeCascadeOutcome struct {
	// DeletedForwardIDs 是因失去最后一个入口端口而随节点级联整行
	// 删除的 Forward id（E1-11）；多入口 Forward 只摘本节点端口
	// 行、不在此列。
	DeletedForwardIDs []int64
}

// TunnelCascadeOutcome 是 DeleteTunnelCascade 的墓碑终局处置结果。
type TunnelCascadeOutcome struct {
	// FinalizedPendingForwardIDs 是 status=PendingDelete 墓碑转发中
	// 在本次级联内终删的 id（E1-01）：台账已耗尽
	// （next_retry_at=0）的墓碑已退出重试调度，保留只会以已删
	// tunnel_id 无限悬空。
	FinalizedPendingForwardIDs []int64
	// RetainedPendingForwardIDs 是仍在退避调度内、本次保留待后台
	// 收敛的墓碑 id（D7 不变量：在线节点先清 nft 规则再删行）。
	RetainedPendingForwardIDs []int64
}

// prunePausedForwardIDsTx 从全部 UserQuota.PausedForwardIDs 文本清单
// 中摘除被删转发 id（E1-12）。只改写真正命中被删 id 的行：未命中
// 的行逐字保留（解析会丢弃非法 token 与重复项，不得借清理之名改
// 写无关行）。清单是配额超限暂停的恢复依据，悬空 id 虽在释放时
// 被跳过自愈，但 disabled 期间残留且随用户备份往返，必须在删除
// 时同步摘除。
func prunePausedForwardIDsTx(tx *gorm.DB, deletedIDs []int64) error {
	if len(deletedIDs) == 0 {
		return nil
	}
	deleted := make(map[int64]struct{}, len(deletedIDs))
	for _, id := range deletedIDs {
		if id > 0 {
			deleted[id] = struct{}{}
		}
	}
	if len(deleted) == 0 {
		return nil
	}
	var quotas []model.UserQuota
	if err := tx.Where("paused_forward_ids <> ?", "").Find(&quotas).Error; err != nil {
		return err
	}
	for _, q := range quotas {
		ids := parsePausedForwardIDs(q.PausedForwardIDs)
		kept := make([]int64, 0, len(ids))
		hit := false
		for _, id := range ids {
			if _, gone := deleted[id]; gone {
				hit = true
				continue
			}
			kept = append(kept, id)
		}
		if !hit {
			continue
		}
		if err := tx.Model(&model.UserQuota{}).Where("user_id = ?", q.UserID).
			Update("paused_forward_ids", joinPausedForwardIDs(kept)).Error; err != nil {
			return err
		}
	}
	return nil
}

// deleteForwardCascadeSetTx 整行删除一组 Forward 及其全部按
// forward_id 的附属行（DeleteForwardCascade 的集合形）。删除顺序
// 与单删一致：先物化 relay id 集清授权，再删 relay 本行，最后删
// Forward 本行并摘除配额暂停清单。
func deleteForwardCascadeSetTx(tx *gorm.DB, forwardIDs []int64) error {
	if len(forwardIDs) == 0 {
		return nil
	}
	if err := deleteByIDSliceInChunksTx(tx, &model.NftCounterState{}, "forward_id", forwardIDs); err != nil {
		return err
	}
	if err := deleteByIDSliceInChunksTx(tx, &model.NftRuleBinding{}, "forward_id", forwardIDs); err != nil {
		return err
	}
	if err := deleteByIDSliceInChunksTx(tx, &model.NftPendingDelete{}, "forward_id", forwardIDs); err != nil {
		return err
	}
	if err := deleteByIDSliceInChunksTx(tx, &model.ForwardPort{}, "forward_id", forwardIDs); err != nil {
		return err
	}
	relayIDs, err := pluckInboundRelayIDsByColumnTx(tx, "forward_id", forwardIDs)
	if err != nil {
		return err
	}
	if err := deleteUserInboundGrantsByRelayIDsTx(tx, relayIDs); err != nil {
		return err
	}
	if err := deleteByIDSliceInChunksTx(tx, &model.NodeInboundRelay{}, "forward_id", forwardIDs); err != nil {
		return err
	}
	if err := deleteByIDSliceInChunksTx(tx, &model.Forward{}, "id", forwardIDs); err != nil {
		return err
	}
	return prunePausedForwardIDsTx(tx, forwardIDs)
}

// cleanupInboundReferencesTx 在一组入站被删除后回写清理订阅面对
// 它们的引用（E1-10）：
//   - Subscription / SubUser 的 inbound_ids CSV → 摘除被删 id
//     （parseBackupRefIDList / formatBackupRefIDList 同口径）。
//     成员清单是纯集合语义，摘除不改变任何渲染行为，不再只靠读
//     侧静默过滤。
//
// 点名保留（同条立案的另两面，逐面论证后不回写）：
//   - 其他入站的 outbound_id 悬空值是 fail-closed 语义载体：构建
//     期把「指向已删入站」渲染为 block 出口（目标删除后流量丢
//     弃、绝不直连泄漏，TestInboundChain 契约）；回写清零等于把
//     block 改成直连，方向相反，故保留悬空由构建期口径处置。
//   - split_rules 中 OutboundID 悬空同理：Via 无 host 时命中流量
//     被丢弃（internal/kernel/split.go），剔除规则会把该部分流量
//     静默改道到入站默认出口，故保留。
//
// 本函数只按「被删 id 集」摘除，不做全表悬空普查。
func cleanupInboundReferencesTx(tx *gorm.DB, deletedInboundIDs []int64) error {
	if len(deletedInboundIDs) == 0 {
		return nil
	}
	deleted := make(map[int64]struct{}, len(deletedInboundIDs))
	for _, id := range deletedInboundIDs {
		if id > 0 {
			deleted[id] = struct{}{}
		}
	}
	if len(deleted) == 0 {
		return nil
	}
	// 订阅 / SubUser 的 inbound_ids CSV。
	var subs []model.Subscription
	if err := tx.Select("id", "inbound_ids").Where("inbound_ids <> ?", "").Find(&subs).Error; err != nil {
		return err
	}
	for _, sub := range subs {
		kept, hit := filterDeletedIDsFromCSV(sub.InboundIDs, deleted)
		if !hit {
			continue
		}
		if err := tx.Model(&model.Subscription{}).Where("id = ?", sub.ID).
			Update("inbound_ids", kept).Error; err != nil {
			return err
		}
	}
	var subUsers []model.SubUser
	if err := tx.Select("id", "inbound_ids").Where("inbound_ids <> ?", "").Find(&subUsers).Error; err != nil {
		return err
	}
	for _, su := range subUsers {
		kept, hit := filterDeletedIDsFromCSV(su.InboundIDs, deleted)
		if !hit {
			continue
		}
		if err := tx.Model(&model.SubUser{}).Where("id = ?", su.ID).
			Update("inbound_ids", kept).Error; err != nil {
			return err
		}
	}
	return nil
}

// filterDeletedIDsFromCSV 从逗号 id 清单中摘除 targets 命中的 id；
// 未命中时返回原文与 hit=false（不改写无关行）。
func filterDeletedIDsFromCSV(raw string, targets map[int64]struct{}) (string, bool) {
	ids := parseBackupRefIDList(raw)
	if len(ids) == 0 {
		return raw, false
	}
	kept := make([]int64, 0, len(ids))
	hit := false
	for _, id := range ids {
		if _, gone := targets[id]; gone {
			hit = true
			continue
		}
		kept = append(kept, id)
	}
	if !hit {
		return raw, false
	}
	return formatBackupRefIDList(kept), true
}

// loadNftPendingDeletesByForwardIDsTx 按 forward_id 集分块物化
// pending 台账行（删隧道级联的终局分流判据）。
func loadNftPendingDeletesByForwardIDsTx(tx *gorm.DB, forwardIDs []int64) ([]model.NftPendingDelete, error) {
	var rows []model.NftPendingDelete
	for _, chunk := range chunkInt64IDs(forwardIDs) {
		var part []model.NftPendingDelete
		if err := tx.Where("forward_id IN ?", chunk).Find(&part).Error; err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	return rows, nil
}

// forwardIDsWithRemainingPortsTx 返回 ids 中在 ForwardPort 仍有
// 任意端口行的子集（删节点级联在摘除本节点端口行后判「失去最后
// 一个入口端口」用）。
func forwardIDsWithRemainingPortsTx(tx *gorm.DB, forwardIDs []int64) (map[int64]struct{}, error) {
	remaining := make(map[int64]struct{})
	for _, chunk := range chunkInt64IDs(forwardIDs) {
		var part []int64
		if err := tx.Model(&model.ForwardPort{}).Where("forward_id IN ?", chunk).
			Distinct("forward_id").Pluck("forward_id", &part).Error; err != nil {
			return nil, err
		}
		for _, id := range part {
			remaining[id] = struct{}{}
		}
	}
	return remaining, nil
}

// pendingRealmSettleConfigKey 是 E1-05 的级联清理目标键（与
// handler.pendingRealmSettleKey 同格式，两处须同步）：删节点时该
// 节点的待结算标记随节点一并删除——节点永不再上线消费，键无 TTL
// 残留即永久孤儿。
func pendingRealmSettleConfigKey(nodeID int64) string {
	return "pending_realm_settle:" + strconv.FormatInt(nodeID, 10)
}
