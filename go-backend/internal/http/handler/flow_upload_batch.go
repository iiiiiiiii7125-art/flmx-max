package handler

import (
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

type flowPolicyTarget struct {
	UserID       int64
	UserTunnelID int64
}

// L3-01（SWEEP-CPU）：流量上报单批 Items 数上限，与连接日志批
// 上限 connlogMaxEventsPerBatch（1000）对齐——10MB 体上限 alone
// 可装约 25 万条 item，解析/校验/聚合/摘要全随 Items 线性无界。
// v1（flowUpload）与 v2（flowUploadV2）两路共用此值，超限整批
// 拒收、agent 保留批次自行拆分重报。
const flowUploadMaxItemsPerBatch = 1000

// L3-01（SWEEP-CPU）：单批入账后孤儿清理的发令上限。孤儿按
// distinct 服务名逐条发 DeleteService（每条 2–3 次 DB 读＋一次
// 最长 6s 的节点命令），无界时会随 Items 线性放大；超限部分
// 留待下一批——已删的孤儿不再上报，清理逐批推进，不丢项。
const flowUploadMaxOrphanDeletesPerBatch = 100

type flowUploadBatch struct {
	flowDeltas     []repo.FlowUploadCounterDelta
	quotaUsage     map[int64]int64
	policyTargets  []flowPolicyTarget
	forwardTraffic map[int64]tunnelTrafficDelta
	orphanServices map[string]struct{}
	// dropped 是单批上报的丢弃台账（A-中-3）：每一类静默丢弃都按
	// 条数＋字节数汇总，buildNodeFlowUploadBatch 每批打一行摘要
	// 日志。丢弃本身是设计行为（见各 reason 的裁定注释），但静
	// 默丢弃不是——管理员必须能从日志里看到每批丢了多少、为什么。
	dropped flowDropStats
	// seqAdvance X-P3-31：agent 上报路径设置——入账单事务提交时同
	// tx 推进 seq；非上报路径（策略单条/内部调用）为 nil。
	seqAdvance *repo.FlowSeqAdvance
}

// flowDropStats 是单批流量上报按原因汇总的丢弃计数（A-中-3）。
// 各原因的裁定：
//   - negative：负流量值，H-17 安全丢弃——负数会扣减计数器绕过配额。
//   - emptyOrWebAPI：空服务名或面板自身 API 流量。web_api 是面板
//     自己的 API 调用流量，无归属用户，按设计不计入任何用户账。
//   - unparsed：服务名解析不成 <forward>_<user>_<tunnel>。直连入站
//     与非转发服务的流量按设计不入用户/隧道账：无法归属到具体用
//     户与隧道；整机总量仍由 NIC 计数（node_traffic）覆盖。
//   - peerShare：联邦共享前缀 peer-share-。旧联邦协议，本地从不产
//     生；上报到本面板即错路。
//   - foreignForward：解析出的转发不属于本节点、或上报用户与转发
//     属主不一致（跨节点误报/旧配置残留；属主节点收到同一批时入账）。
//   - orphan：解析出的转发已不在本节点（已删转发的残留上报；面板
//     回 DeleteService 后 agent 停报）。与 foreignForward 的区别是
//     归属查询本身无此转发（metas 缺项），而非属主不一致。
type flowDropStats struct {
	negativeItems       int
	negativeBytes       int64
	emptyOrWebAPIItems  int
	emptyOrWebAPIBytes  int64
	unparsedItems       int
	unparsedBytes       int64
	peerShareItems      int
	peerShareBytes      int64
	foreignForwardItems int
	foreignForwardBytes int64
	orphanItems         int
	orphanBytes         int64
}

func (s flowDropStats) totalItems() int {
	return s.negativeItems + s.emptyOrWebAPIItems + s.unparsedItems + s.peerShareItems + s.foreignForwardItems + s.orphanItems
}

// logSummary 打一行单批丢弃摘要（A-中-3）：无丢弃时不打，避免
// 每批一条的日志噪音；有丢弃时一行写全六类条数与字节数。
func (s flowDropStats) logSummary(nodeID int64) {
	if s.totalItems() == 0 {
		return
	}
	log.Printf("flow upload dropped node_id=%d items=%d negative=%d/%dB empty_or_webapi=%d/%dB unparsed=%d/%dB peer_share=%d/%dB foreign_forward=%d/%dB orphan=%d/%dB",
		nodeID, s.totalItems(),
		s.negativeItems, s.negativeBytes,
		s.emptyOrWebAPIItems, s.emptyOrWebAPIBytes,
		s.unparsedItems, s.unparsedBytes,
		s.peerShareItems, s.peerShareBytes,
		s.foreignForwardItems, s.foreignForwardBytes,
		s.orphanItems, s.orphanBytes)
}

func (h *Handler) buildFlowUploadBatch(items []flowItem, metas map[int64]repo.FlowUploadForwardMeta) flowUploadBatch {
	batch := flowUploadBatch{
		quotaUsage:     make(map[int64]int64),
		forwardTraffic: make(map[int64]tunnelTrafficDelta),
		orphanServices: make(map[string]struct{}),
	}
	policySeen := map[flowPolicyTarget]struct{}{}

	// X-P3-18（L-05-1）：gost 合并/缩放溢出检查（对齐 nft 路径：溢出
	// 整条丢弃并记日志）。旧实现逐项直接累加，两条各 ≈9.2e18 的上报
	// 合并回绕为负、负增量落账。现本地转发先聚合到 pending（任一加法
	// 溢出即整条转发丢弃），最后统一落 batch。
	type pendingLocal struct {
		userID, userTunnelID int64
		rawIn, rawOut        int64
		scaledIn, scaledOut  int64
	}
	pending := make(map[int64]*pendingLocal)
	pendingOrder := make([]int64, 0)
	droppedLocal := make(map[int64]struct{})
	addOverflows := func(a, b int64) bool {
		return (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b)
	}

	for _, item := range items {
		// H-17: 丢弃负流量值。/flow/upload 仅凭节点 secret 鉴权，攻击者可伪造
		// {"d":-N} 经 gorm.Expr("in_flow + ?", 负数) 直接扣减计数器，绕过配额。
		// A-中-3：丢弃计入 batch.dropped（负值字节无意义，只计条数）。
		if item.U < 0 || item.D < 0 {
			batch.dropped.negativeItems++
			continue
		}
		serviceName := strings.TrimSpace(item.N)
		if serviceName == "" || serviceName == "web_api" {
			// A-中-3 裁定：web_api 是面板自身 API 流量、空名无归属，
			// 按设计不入任何用户账；丢弃计入 batch.dropped，见
			// flowDropStats 的原因注释。
			batch.dropped.emptyOrWebAPIItems++
			batch.dropped.emptyOrWebAPIBytes += item.U + item.D
			continue
		}
		forwardID, userID, userTunnelID, ok := parseFlowServiceIDs(serviceName)
		if !ok {
			// A-中-3 裁定：解析不成 <forward>_<user>_<tunnel> 的是
			// 直连入站/非转发服务流量，按设计不入用户/隧道账（无法
			// 归属）；整机总量仍由 NIC 计数覆盖。丢弃计入 batch.dropped。
			batch.dropped.unparsedItems++
			batch.dropped.unparsedBytes += item.U + item.D
			continue
		}
		meta, exists := metas[forwardID]
		if !exists {
			// R2A-1：孤儿服务（转发已删、agent 残留上报）此前只进
			// orphanServices 清理集、字节不入丢弃台账，单批摘要
			// 里 80B 级孤儿无影无踪；现按 R2A-1 计入 dropped（与
			// 其余五类同口径），清理集本身不动。
			batch.dropped.orphanItems++
			batch.dropped.orphanBytes += item.U + item.D
			batch.orphanServices[serviceName] = struct{}{}
			continue
		}
		// Local accounting uses database ownership, not foreign IDs embedded in
		// a service name (including stale user-tunnel IDs after reassignment).
		userID, userTunnelID = meta.UserID, meta.UserTunnelID

		if _, dropped := droppedLocal[forwardID]; dropped {
			continue
		}
		// N-01（方向钉死，勿再按字面交换）：agent 上报线格式的 U/D 与
		// 「上行/下行」字面相反。agent 在 go-gost/x/service/service.go 以
		// AddTraffic(name, outputBytes, inputBytes) 填充：U=gost
		// OutputBytes=远端→客户端（出），D=gost InputBytes=客户端→远端
		// （入）。真机实证（w10-run/N-01）：下载 12,582,912 字节时
		// U=12,583,527、D=267。故 InFlow（入）=item.D、OutFlow（出）
		// =item.U，与 nftables/realm 路径 BytesIn=to-target（renderer
		// 按目标地址计数）完全一致；raw.bytesIn/bytesOut 同此映射，
		// 隧道指标桶三条路径同口径。
		// 此前 O6-002 按「U=入」的字面假设交换过 scaledIn/scaledOut：
		// 双向计费时总额不变，但 TunnelFlow=1（单向，仅计入方向）时
		// gost 实际按出方向扣配额，与 realm/nftables 取入方向正好
		// 相反，且 in_flow/out_flow 归属与前端 ↑/↓ 展示颠倒。本次订正。
		// Flow=1 单向：仅入方向（InFlow）；Flow!=1 双向：入+出。
		// 历史 bug：曾把 Flow 当乘数用，导致 Flow=2 双倍扣费（1.0.7
		// M-001 已修复）；quotaUsage 始终为 scaledIn+scaledOut。
		// R7-F-01：缩放统一走共用守卫 scaleTrafficBytes，与 nft 路径
		// 同一语义（ratio==1 直通不走 float64 往返、乘积 >=2^63 /
		// NaN / Inf / 负值整条丢弃）。旧判据 `scaledF > float64
		// (MaxInt64)` 中 float64(MaxInt64) 本身即 2^63，乘积恰为
		// 2^63 时判据为假、int64(2^63) 回绕 MinInt64 并落入
		// user/user_tunnel 总量；本路径不得再自写缩放判据。
		scaledIn, scaleOK := scaleTrafficBytes(item.D, meta.TrafficRatio)
		var scaledOut int64
		if scaleOK && meta.TunnelFlow != 1 {
			scaledOut, scaleOK = scaleTrafficBytes(item.U, meta.TrafficRatio)
		}
		if !scaleOK {
			log.Printf("gost scale overflow discard forward_id=%d u=%d d=%d ratio=%v", forwardID, item.U, item.D, meta.TrafficRatio)
			delete(pending, forwardID)
			droppedLocal[forwardID] = struct{}{}
			continue
		}

		pl, seen := pending[forwardID]
		if !seen {
			pl = &pendingLocal{userID: userID, userTunnelID: userTunnelID}
			pending[forwardID] = pl
			pendingOrder = append(pendingOrder, forwardID)
		}
		if addOverflows(pl.rawIn, item.D) || addOverflows(pl.rawOut, item.U) ||
			addOverflows(pl.scaledIn, scaledIn) || addOverflows(pl.scaledOut, scaledOut) {
			log.Printf("gost merge overflow discard forward_id=%d u=%d d=%d", forwardID, item.U, item.D)
			delete(pending, forwardID)
			droppedLocal[forwardID] = struct{}{}
			continue
		}
		pl.rawIn += item.D
		pl.rawOut += item.U
		pl.scaledIn += scaledIn
		pl.scaledOut += scaledOut
	}

	// 落账：按首见顺序把 pending 写进 batch（quota 合计溢出同样整条丢弃）。
	for _, forwardID := range pendingOrder {
		pl, ok := pending[forwardID]
		if !ok {
			continue // 已因溢出丢弃
		}
		if addOverflows(pl.scaledIn, pl.scaledOut) || addOverflows(batch.quotaUsage[pl.userID], pl.scaledIn+pl.scaledOut) {
			log.Printf("gost quota overflow discard forward_id=%d scaled_in=%d scaled_out=%d", forwardID, pl.scaledIn, pl.scaledOut)
			continue
		}
		batch.forwardTraffic[forwardID] = tunnelTrafficDelta{bytesIn: pl.rawIn, bytesOut: pl.rawOut}
		// R5X-013：delta 拆 raw/scaled 两组——forward 行落 raw
		// （pl.rawIn/rawOut，与 forwardTraffic 指标桶同源），
		// user/user_tunnel 行与 quota 落 scaled。
		batch.flowDeltas = append(batch.flowDeltas, repo.FlowUploadCounterDelta{
			ForwardID:    forwardID,
			UserID:       pl.userID,
			UserTunnelID: pl.userTunnelID,
			InFlow:       pl.scaledIn,
			OutFlow:      pl.scaledOut,
			RawInFlow:    pl.rawIn,
			RawOutFlow:   pl.rawOut,
			HasRaw:       true,
		})
		batch.quotaUsage[pl.userID] += pl.scaledIn + pl.scaledOut
		target := flowPolicyTarget{UserID: pl.userID, UserTunnelID: pl.userTunnelID}
		if _, seen := policySeen[target]; !seen {
			policySeen[target] = struct{}{}
			batch.policyTargets = append(batch.policyTargets, target)
		}
	}

	sort.Slice(batch.policyTargets, func(i, j int) bool {
		if batch.policyTargets[i].UserID == batch.policyTargets[j].UserID {
			return batch.policyTargets[i].UserTunnelID < batch.policyTargets[j].UserTunnelID
		}
		return batch.policyTargets[i].UserID < batch.policyTargets[j].UserID
	})

	return batch
}

// applyFlowUploadBatch 返回入账错误（X-P3-31/V-08）：总量+quota 由
// repo 单事务原子入账（seqAdvance 非 nil 时 seq 同 tx 推进）；失败时
// 整批未入账，策略执行/孤儿清理一律不进行，调用方
// （flowUpload）据此整批拒收返非 ok，agent 同 seq 重报重新入账。
// 旧「两段分开写、一段失败另一段继续 + 逐条 fallback」取舍已按
// V-08 裁定废弃（断言更新登记见 flow_upload_batch_test.go 三测试）。
func (h *Handler) applyFlowUploadBatch(nodeID int64, batch flowUploadBatch, now time.Time) error {
	if h == nil || h.repo == nil {
		return nil
	}
	quotaViews, err := h.repo.ApplyFlowUploadAccounting(batch.flowDeltas, batch.quotaUsage, now, batch.seqAdvance)
	if err != nil {
		log.Printf("flow upload accounting failed node_id=%d err=%v", nodeID, err)
		return err
	}
	h.runFlowUploadSideEffects(nodeID, batch, quotaViews)
	return nil
}

// runFlowUploadSideEffects 是入账提交后的副作用面（配额强制、策略
// 执行、孤儿清理）：只应在批次确实入账后调用。
// v1（applyFlowUploadBatch）与 v2（flowUploadV2 的 applied 分支）
// 共用，保证两条上报路径入账后的面板行为一致。
func (h *Handler) runFlowUploadSideEffects(nodeID int64, batch flowUploadBatch, quotaViews map[int64]*model.UserQuotaView) {
	for userID, quota := range quotaViews {
		h.enforceUserQuotaIfNeeded(userID, quota)
	}
	for _, target := range batch.policyTargets {
		if target.UserID <= 0 || target.UserTunnelID <= 0 {
			continue
		}
		h.enforceFlowPolicies(target.UserID, target.UserTunnelID)
	}
	if len(batch.orphanServices) > 0 {
		serviceNames := make([]string, 0, len(batch.orphanServices))
		for serviceName := range batch.orphanServices {
			serviceNames = append(serviceNames, serviceName)
		}
		// L3-01：排序后取前 N（确定性顺序——同批重报不摇摆），
		// 超限的孤儿留待后续批次：已删服务不再上报，清理逐批
		// 推进；一次全量发令会把 6s 超时命令数随孤儿数放大。
		sort.Strings(serviceNames)
		if len(serviceNames) > flowUploadMaxOrphanDeletesPerBatch {
			log.Printf("flow upload orphan cleanup capped node_id=%d orphans=%d cap=%d deferred=%d",
				nodeID, len(serviceNames), flowUploadMaxOrphanDeletesPerBatch,
				len(serviceNames)-flowUploadMaxOrphanDeletesPerBatch)
			serviceNames = serviceNames[:flowUploadMaxOrphanDeletesPerBatch]
		}
		h.sendDeleteOrphanedForwardServices(nodeID, serviceNames)
	}
}
