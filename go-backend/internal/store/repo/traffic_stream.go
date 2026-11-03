package repo

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
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
//
// R2-037 固有取舍（显式记录，本轮不动代码）：恢复后首触必然走
// gap 路径，面板无从区分该批是「从未入账」还是「已入账但确认
// 回执在网络上丢失」——后者会在新 stream 下二次入账。歧义上界
// 为每节点一批，即 agent 一个采样周期（5 秒）的待确认量；两
// 状态在协议上不可区分，属已选自愈方案的固有取舍。根除需产品
// 决策（游标随备份走，或面板侧留批次摘要供恢复后比对）。
//
// C-C2-01（R2-037 的镜像方向）：序号严格小于游标的批次此前被
// 无条件按重放确认——agent spool 被整机快照恢复/旧备份还原/
// 整目录克隆回退时，回退段逐批被「确认并清批」静默少计且零可
// 观测。现在判定结果经 TrafficStreamAck 把游标位置与类别带回
// handler：handler 对 behind 落结构化日志、并把游标写进确认回
// 执，agent 据此当场轮换 stream，未入账字节改走新 stream 重新
// 入账（已确认字节绝不重发，无双计通道）。可区分维度：序号 ==
// 游标且摘要一致＝真重放（Replay）；序号 < 游标＝agent 状态落
// 后（Behind）。不可区分维度：游标只存最新序号的摘要，旧序号
// 批次的内容同一性无从复验——behind 批次究竟是「已入账原批的
// 重发」还是「回退后新计的同序号批次」，面板判不了，两者都只
// 确认不入账并同类落日志，由 agent 侧切流止损。
//
// RM-C2-2 收口（LEDGER5 §7 验收行）：上段的「判不了」在克隆双写
// 格留下了可观测性缺口——整目录克隆出的第二写者以旧序号上报分
// 岔内容时与单写者回退同落 agent_state_behind，面板日志无
// identity_conflict 专用行。本轮起游标额外保留最近
// trafficStreamRecentDigestWindow 个已入账批次的摘要（近窗）与
// 该 stream 最近入账写者的来源地址（clientip 可信链解析口径）：
// behind 批次同时满足「序号在近窗内且摘要与已入账摘要不同」
// （内容分岔可证）与「来源与记录写者不同」（并发写者可证）时，
// ack.IdentityConflict 置位，handler 改落 class=identity_conflict
// 专用行。回执（200 确认＋游标）、不入账语义一律不变，仅日志分
// 类。判不了的格全部维持 agent_state_behind：同写者回退（来源
// 相同，含原格）、异源但内容与已入账原批逐字一致（真重发）、
// 序号超出近窗（内容不可验）、写者记录缺失（升级前存量游标行）。
// 同主机克隆（来源相同）与单写者回退在协议面不可区分，按设计
// 同落 behind——分类边界即证据边界，不做推测性升级。

// TrafficStreamCursor 记录一个上报 stream 的入账游标。
// WriterAddr 与 RecentDigests 是 RM-C2-2 收口的分类证据面：前者
// 是最近一次入账写者的来源地址（clientip 解析口径，空＝未知），
// 后者是近窗摘要环（trafficStreamRecentDigestWindow 条，编码见
// appendRecentDigest）；两者只服务 behind 批次的日志分类，不参
// 与入账判定本身。
type TrafficStreamCursor struct {
	Scope         string `gorm:"primaryKey;column:scope;size:200"`
	Sequence      int64  `gorm:"column:sequence;not null;default:0"`
	Digest        string `gorm:"column:digest;size:64;not null;default:''"`
	WriterAddr    string `gorm:"column:writer_addr;size:64;not null;default:''"`
	RecentDigests string `gorm:"column:recent_digests;size:2048;not null;default:''"`
	UpdatedTime   int64  `gorm:"column:updated_time;not null;default:0"`
}

func (TrafficStreamCursor) TableName() string { return "traffic_stream_cursor" }

// ErrTrafficStreamConflict 表示同 (scope, sequence) 已入账不同内容。
var ErrTrafficStreamConflict = errors.New("traffic stream identity conflict")

// trafficStreamRecentDigestWindow 是游标行为 behind 分类保留的近
// 窗摘要条数（RM-C2-2 收口）：覆盖克隆/回退分岔的现实滞后深度
// （真机两景滞后 1–2 个序号），窗外序号的内容同一性回到不可验
// 口径。条数与编码长度均有界（每条 ≤ 序号 19 位＋摘要 64 位），
// 游标行随 stream 清扫回收，不构成无界增长面。
const trafficStreamRecentDigestWindow = 16

type recentDigestEntry struct {
	sequence int64
	digest   string
}

// parseRecentDigests 解游标行的近窗编码（"seq:digest;seq:digest"，
// 按序号升序）；编码缺失/损坏时按已解析部分处理，坏条目跳过——
// 分类证据面损坏只降级为不可验，绝不影响入账判定。
func parseRecentDigests(raw string) []recentDigestEntry {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ";")
	entries := make([]recentDigestEntry, 0, len(parts))
	for _, part := range parts {
		seqText, digest, ok := strings.Cut(part, ":")
		if !ok || len(digest) != 64 {
			continue
		}
		seq, err := strconv.ParseInt(seqText, 10, 64)
		if err != nil || seq <= 0 {
			continue
		}
		entries = append(entries, recentDigestEntry{sequence: seq, digest: digest})
	}
	return entries
}

func recentDigestFor(entries []recentDigestEntry, sequence int64) (string, bool) {
	for _, entry := range entries {
		if entry.sequence == sequence {
			return entry.digest, true
		}
	}
	return "", false
}

// appendRecentDigest 把新入账批次的摘要并入近窗编码：同序号以新
// 摘要为准（构造上序号单调递增、不会重入），超出窗口时丢弃最旧
// 条目。
func appendRecentDigest(raw string, sequence int64, digest string) string {
	entries := parseRecentDigests(raw)
	kept := entries[:0]
	for _, entry := range entries {
		if entry.sequence != sequence {
			kept = append(kept, entry)
		}
	}
	kept = append(kept, recentDigestEntry{sequence: sequence, digest: digest})
	if len(kept) > trafficStreamRecentDigestWindow {
		kept = kept[len(kept)-trafficStreamRecentDigestWindow:]
	}
	var b strings.Builder
	for i, entry := range kept {
		if i > 0 {
			b.WriteByte(';')
		}
		fmt.Fprintf(&b, "%d:%s", entry.sequence, entry.digest)
	}
	return b.String()
}

// DeleteStaleTrafficStreamCursors 删除 updated_time 早于 cutoffMs
// 的游标行（R2-038 对账清扫）：节点每次换 stream 都在本表留一行，
// 唯一删除点原是删节点级联，存活节点的孤儿行单调累积。按保留期
// 删除是安全的：活跃 stream 每批入账都会刷新 updated_time，静默
// 超过保留期的 stream 即使之后恢复上报，游标缺失也只触发一次
// gap→reset_stream 自愈（agent 以新 stream 重报其待确认批次）；
// 不产生超出 R2-037 固有歧义的新风险（上界仍是每节点一批）。
// 返回删除行数。
func (r *Repository) DeleteStaleTrafficStreamCursors(cutoffMs int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	res := r.db.Where("updated_time < ?", cutoffMs).Delete(&TrafficStreamCursor{})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// TrafficStreamGap 表示上报序号与游标不连续（预期 Expected 却收到
// 更大的序号）：游标被备份恢复回退或整行丢失过。
type TrafficStreamGap struct{ Expected int64 }

func (e *TrafficStreamGap) Error() string {
	return fmt.Sprintf("traffic stream sequence gap: expected %d", e.Expected)
}

// TrafficStreamAck 是 ApplyTrafficStreamAccounting 的判定结果：
// 除本批是否入账外，把判定后的游标位置与重放类别一并带回调用
// 方——handler 据 Behind/Replay 落可区分的结构化日志，并把
// Cursor 写进确认回执供 agent 识别自身状态落后（C-C2-01）。
type TrafficStreamAck struct {
	Applied bool  // 本批恰为下一序号、已入账
	Replay  bool  // sequence == 游标且摘要一致：已入账批次的真重放
	Behind  bool  // sequence < 游标：agent 状态落后于面板游标
	Cursor  int64 // 判定后面板该 stream 的游标序号
	// IdentityConflict 仅在 Behind 时有意义（RM-C2-2 收口）：该
	// behind 批次已被双证据证明是并发克隆写者的分岔批次（近窗
	// 摘要不同＋来源与记录写者不同），handler 据此改落
	// class=identity_conflict 日志；回执仍是 behind 确认、不入账。
	IdentityConflict bool
}

// ApplyTrafficStreamAccounting 在单个事务内完成流游标判定＋入账：
// 恰好下一序号才入账（deltas 与 quota 同 tx，复用 v1 的入账内层），
// 并同 tx 推进游标；重放（同序号同摘要，或更旧序号）返回
// Applied=false 且无错误，调用方据此只回确认、不做入账后副作用。
// writerAddr 是本批来源地址（clientip 解析口径，空＝未解析）：入
// 账时记为该 stream 的最近写者，behind 时与记录写者比对，作为
// RM-C2-2 分类判据的写者维度；不参与任何入账/拒绝判定。
func (r *Repository) ApplyTrafficStreamAccounting(deltas []FlowUploadCounterDelta, quotaUsage map[int64]int64, now time.Time, scope string, sequence int64, digest string, writerAddr string) (map[int64]*model.UserQuotaView, TrafficStreamAck, error) {
	if r == nil || r.db == nil {
		return nil, TrafficStreamAck{}, errors.New("repository not initialized")
	}
	if scope == "" || len(scope) > 200 || sequence <= 0 || len(digest) != 64 {
		return nil, TrafficStreamAck{}, ErrTrafficStreamConflict
	}
	quotaViews := map[int64]*model.UserQuotaView{}
	ack := TrafficStreamAck{}
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
			// 已入账段的确认：不入账。序号恰等于游标且摘要一致
			// 是真重放；严格小于游标是 agent 状态落后（C-C2-01）。
			ack.Cursor = cursor.Sequence
			ack.Replay = sequence == cursor.Sequence
			ack.Behind = sequence < cursor.Sequence
			if ack.Behind && writerAddr != "" && cursor.WriterAddr != "" &&
				writerAddr != cursor.WriterAddr {
				// RM-C2-2：异源 behind 再核内容维度——近窗内摘
				// 要与已入账不同才证明是第二写者的分岔批次；
				// 摘要一致是已入账原批的异源真重发，窗外则不
				// 可验，两者都维持 behind 分类。
				if bookedDigest, ok := recentDigestFor(parseRecentDigests(cursor.RecentDigests), sequence); ok && bookedDigest != digest {
					ack.IdentityConflict = true
				}
			}
			return nil
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
		updates := map[string]interface{}{
			"sequence":       sequence,
			"digest":         digest,
			"recent_digests": appendRecentDigest(cursor.RecentDigests, sequence, digest),
			"updated_time":   now.UnixMilli(),
		}
		if writerAddr != "" {
			// 写者记录只被可解析的来源刷新：未解析批次不抹掉
			// 已知写者（分类证据宁缺毋假）。
			updates["writer_addr"] = writerAddr
		}
		if err := tx.Model(&TrafficStreamCursor{}).
			Where("scope = ?", scope).
			Updates(updates).Error; err != nil {
			return err
		}
		ack.Applied = true
		ack.Cursor = sequence
		return nil
	})
	if err != nil {
		return nil, TrafficStreamAck{}, err
	}
	return quotaViews, ack, nil
}
