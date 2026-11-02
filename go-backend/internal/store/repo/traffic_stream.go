package repo

import (
	"errors"
	"fmt"
	"time"

	"go-backend/internal/store/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 可靠上报 v2 的流游标（与 node_seq 的 v1 序号幂等并存、互不干扰）。
//
// agent 每份 spool 有一个随机 stream 身份，面板按 scope=flow:节点:
// stream 记一行游标（最后入账序号＋内容摘要）：
//   - sequence == 游标序号且摘要一致 → 已入账的重放，确认但不再入账；
//   - sequence == 游标序号但摘要不同 → ErrTrafficStreamConflict
//     （同序号不同内容，agent 侧隔离留证，绝不重放防双扣）；
//   - sequence > 游标序号但不恰好 +1 → TrafficStreamGap（面板备份
//     恢复致游标回退是典型成因，agent 侧换新 stream 原字节重报）；
//   - sequence 恰好 +1 → 入账与游标推进同事务落地。
//
// 游标不进备份导出：备份恢复后游标回退/缺失正由 gap→reset_stream
// 自愈闭环处理，不需要随备份搬运。

// TrafficStreamCursor 记录一个上报 stream 的入账游标。
type TrafficStreamCursor struct {
	Scope       string `gorm:"primaryKey;column:scope;size:200"`
	Sequence    int64  `gorm:"column:sequence;not null;default:0"`
	Digest      string `gorm:"column:digest;size:64;not null;default:''"`
	UpdatedTime int64  `gorm:"column:updated_time;not null;default:0"`
}

func (TrafficStreamCursor) TableName() string { return "traffic_stream_cursor" }

// ErrTrafficStreamConflict 表示同 (scope, sequence) 已入账不同内容。
var ErrTrafficStreamConflict = errors.New("traffic stream identity conflict")

// TrafficStreamGap 表示上报序号与游标不连续（预期 Expected 却收到
// 更大的序号）：游标被备份恢复回退或整行丢失过。
type TrafficStreamGap struct{ Expected int64 }

func (e *TrafficStreamGap) Error() string {
	return fmt.Sprintf("traffic stream sequence gap: expected %d", e.Expected)
}

// ApplyTrafficStreamAccounting 在单个事务内完成流游标判定＋入账：
// 恰好下一序号才入账（deltas 与 quota 同 tx，复用 v1 的入账内层），
// 并同 tx 推进游标；重放（同序号同摘要，或更旧序号）返回
// applied=false 且无错误，调用方据此只回确认、不做入账后副作用。
func (r *Repository) ApplyTrafficStreamAccounting(deltas []FlowUploadCounterDelta, quotaUsage map[int64]int64, now time.Time, scope string, sequence int64, digest string) (map[int64]*model.UserQuotaView, bool, error) {
	if r == nil || r.db == nil {
		return nil, false, errors.New("repository not initialized")
	}
	if scope == "" || len(scope) > 200 || sequence <= 0 || len(digest) != 64 {
		return nil, false, ErrTrafficStreamConflict
	}
	quotaViews := map[int64]*model.UserQuotaView{}
	applied := false
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&TrafficStreamCursor{Scope: scope}).Error; err != nil {
			return err
		}
		var cursor TrafficStreamCursor
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("scope = ?", scope).First(&cursor).Error; err != nil {
			return err
		}
		if sequence <= cursor.Sequence {
			if sequence == cursor.Sequence && digest != cursor.Digest {
				return ErrTrafficStreamConflict
			}
			return nil // 已入账的重放：确认即可，不再入账。
		}
		if sequence != cursor.Sequence+1 {
			return &TrafficStreamGap{Expected: cursor.Sequence + 1}
		}
		if err := applyFlowUploadDeltasTx(tx, deltas); err != nil {
			return err
		}
		var err error
		quotaViews, err = r.addUserQuotaUsageBatchTx(tx, quotaUsage, now)
		if err != nil {
			return err
		}
		if err := tx.Model(&TrafficStreamCursor{}).
			Where("scope = ?", scope).
			Updates(map[string]interface{}{
				"sequence":     sequence,
				"digest":       digest,
				"updated_time": now.UnixMilli(),
			}).Error; err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return quotaViews, applied, nil
}
