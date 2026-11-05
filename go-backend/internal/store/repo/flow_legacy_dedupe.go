package repo

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// F2-03: v1 无 seq（legacy）批次的内容落盘去重。
//
// 带 seq 的批次由 node_seq 推进防重放；无 seq 形态没有任何序号，
// 在 2027-02-02 落日（flowV1LegacySunset）之前，抓到的一份批次字节
// 可原样重放、每次全额再入账一遍（live-d 场景 5 实证双计）。本表以
// (node_id, batch_hash) 为键登记已入账批次：batch_hash 是解密后批
// 次明文的 SHA-256——逐字重放与「首帖已入账、回执丢失后 agent 原
// 样重报」是同一明文、同一哈希，一并去重（采样批以内容为身份：
// 同一节点在保留窗口内发出两份明文逐字相同的采样批，只可能是同
// 一批的两次投递）。
//
// 保留窗口 24 小时：领取时顺带清理本节点过期行。窗口外的处理口
// 径（明示）：加密形态的逐字重放先被信封 timestamp ±15 分钟新鲜
// 度拒收（checkFlowEnvelopeTimestamp），到不了本表；明文形态窗口
// 外的逐字重放不再拦截，由 2027-02-02 落日整批关停兜底。

// FlowLegacyBatchDedupeRetention 是去重登记的保留时长（毫秒）。
const FlowLegacyBatchDedupeRetention = int64(24 * 60 * 60 * 1000)

// FlowLegacyBatchDedupe 登记一行已入账的 legacy 批次内容哈希。
type FlowLegacyBatchDedupe struct {
	NodeID    int64  `gorm:"primaryKey;column:node_id"`
	BatchHash string `gorm:"primaryKey;column:batch_hash;type:varchar(64)"`
	FirstSeen int64  `gorm:"column:first_seen;not null"`
}

func (FlowLegacyBatchDedupe) TableName() string { return "flow_legacy_batch_dedupe" }

// ErrLegacyBatchReplay 是 F2-03 的重放哨兵：入账事务内的内容领取
// 未命中（同节点同哈希批次已入账）时返回，整 tx 随之回滚；handler
// 对该哨兵按重放分支回 ok 不入账（与 ErrSeqReplay 同口径）。
var ErrLegacyBatchReplay = errors.New("flow upload legacy batch replay")

// claimLegacyBatchTx 在入账事务内领取一批 legacy 批次：先清本节点
// 过期登记，再以冲突安全插入登记本批哈希——未插入说明保留窗口内
// 已入账过同内容批次，返回 ErrLegacyBatchReplay 使整 tx 回滚。
func claimLegacyBatchTx(tx *gorm.DB, nodeID int64, batchHash string, now int64) error {
	if err := tx.Where("node_id = ? AND first_seen < ?", nodeID, now-FlowLegacyBatchDedupeRetention).
		Delete(&FlowLegacyBatchDedupe{}).Error; err != nil {
		return err
	}
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&FlowLegacyBatchDedupe{NodeID: nodeID, BatchHash: batchHash, FirstSeen: now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrLegacyBatchReplay
	}
	return nil
}
