package handler

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// Remote sources are subscription URLs, or uploaded subscription files,
// whose nodes the panel keeps as imported nodes (SourceID set). A refresh
// (or a new upload) updates them in place, so relays and subscriptions
// that use them keep working.

var remoteSourceCheckInterval = 10 * time.Minute

// remoteSourceMu serializes refreshes (the loop and the refresh button).
var remoteSourceMu sync.Mutex

type remoteSourceView struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	Kind          string `json:"kind"`
	Role          string `json:"role"`
	IntervalHours int    `json:"intervalHours"`
	RelayOnly     bool   `json:"relayOnly"`
	Enabled       bool   `json:"enabled"`
	LastFetch     int64  `json:"lastFetch"`
	LastError     string `json:"lastError"`
	NodeCount     int    `json:"nodeCount"`
	Userinfo      string `json:"userinfo"`
}

func remoteSourceViewOf(s *model.RemoteSource) remoteSourceView {
	return remoteSourceView{
		ID: s.ID, Name: s.Name, URL: s.URL, Kind: sourceKind(s), Role: s.Role, IntervalHours: s.IntervalHours, RelayOnly: s.RelayOnly == 1,
		Enabled: s.Enabled == 1, LastFetch: s.LastFetch, LastError: s.LastError, NodeCount: s.NodeCount,
		Userinfo: s.Userinfo,
	}
}

func sourceKind(s *model.RemoteSource) string {
	if s.Kind == model.RemoteSourceFile {
		return model.RemoteSourceFile
	}
	return model.RemoteSourceURL
}

// remoteSyncResult.Status 的取值集（C1-05）：同步结果的机器可读
// 状态。Errors 是给人看的散文，消费方（前端、回执组装）做分支判
// 断只应看 Status，不得解析文案。
const (
	remoteSyncStatusOK        = "ok"
	remoteSyncStatusTruncated = "truncated"
)

// remoteSyncResult is what one refresh changed.
type remoteSyncResult struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Removed int `json:"removed"`
	// Kept are nodes gone from the source that still have relays (or
	// chain references); they are left alone so the relays keep their
	// target.
	Kept int `json:"kept"`
	// KeptUnknown are unmatched nodes kept because the relay guard
	// query itself failed: whether they still have relays is unknown
	// (fail-closed keep). Counted apart from Kept so the summary does
	// not claim "has relays" for them (R2-018/B-sub-07).
	KeptUnknown int `json:"keptUnknown"`
	// KeptStale are unmatched nodes kept only because this round was
	// incomplete (the integrity gate deferred their deletion to the
	// next clean round). Counted apart from Kept so the summary can
	// name the reason.
	KeptStale int `json:"keptStale"`
	// Shielded are source entries skipped because an admin deleted the
	// node by hand and the deletion shield (R10-04) keeps it deleted:
	// the entry is not re-created this round.
	Shielded int `json:"shielded"`
	// Status is the round's explicit machine-readable status:
	// remoteSyncStatusOK for a complete round, or
	// remoteSyncStatusTruncated when the maxImportNodes cap dropped
	// part of the source (C1-05 — truncation previously surfaced
	// only as Errors prose, which consumers could not branch on).
	Status string `json:"status"`
	// Truncated is how many parsed nodes the maxImportNodes cap
	// dropped this round (0 = no truncation). Sync and manual import
	// share the 500 cap but differ on purpose: import is a one-shot
	// admin action and rejects the whole submission over the cap
	// (inboundImport), while sync recurs automatically — rejecting
	// whole rounds would freeze the source's node set forever — so
	// sync keeps the first 500 and reports the truncation here
	// instead of only in prose.
	Truncated int      `json:"truncated"`
	Errors    []string `json:"errors"`
}

func (r remoteSyncResult) summary() string {
	parts := []string{fmt.Sprintf("新增 %d", r.Added), fmt.Sprintf("更新 %d", r.Updated), fmt.Sprintf("移除 %d", r.Removed)}
	if r.Kept > 0 {
		parts = append(parts, fmt.Sprintf("%d 个已不在订阅里但有中转，已保留", r.Kept))
	}
	if r.KeptUnknown > 0 {
		parts = append(parts, fmt.Sprintf("%d 个已不在订阅里的节点因中转查询失败无法确认，已保留待下轮确认", r.KeptUnknown))
	}
	if r.KeptStale > 0 {
		parts = append(parts, fmt.Sprintf("%d 个未匹配节点因本轮同步不完整，已保留待下轮确认", r.KeptStale))
	}
	if r.Shielded > 0 {
		parts = append(parts, fmt.Sprintf("%d 个节点已手工删除屏蔽，本轮同步未恢复", r.Shielded))
	}
	if r.Truncated > 0 {
		parts = append(parts, fmt.Sprintf("%d 个节点超出单轮同步上限 %d，本轮未同步", r.Truncated, maxImportNodes))
	}
	return strings.Join(parts, "，")
}

// importedNameKey is the name leg of the deletion-shield identity
// tuple: case-insensitive over the truncated name. (The sync's dedup
// does NOT use it — dedup follows identity only, see the pre-dedup
// block in syncRemoteSource; W3/I-003.)
//
// importedAddrKey is the address leg of the deletion-shield identity
// tuple only (R10-04): the bare externalKey form. The shield is
// deliberately coarse (see importedIdentityKey). The sync's dedup
// must NOT use it — dedup follows the identity semantics of the main
// loop and of manual import ("id:" + externalIdentityKey: address +
// protocol + credentials), so entries that share an address but
// differ in protocol or credentials are distinct nodes end to end
// (J7-01: the pre-dedup once used this bare key and silently dropped
// such entries before they ever reached the main loop).
func importedNameKey(name string) string {
	return "name:" + strings.ToLower(truncateRunes(name, 100))
}

func importedAddrKey(host string, port int) string {
	return "addr:" + externalKey(host, port)
}

// importedIdentityKey is the deletion-shield identity tuple (R10-04):
// the composite of the same name and address keys the sync matches
// existing rows by. Renaming or moving the source entry changes the
// tuple, so the changed entry is a new node the shield does not cover;
// an entry identical in both is the same node and stays deleted.
func importedIdentityKey(name, host string, port int) string {
	return importedNameKey(name) + "|" + importedAddrKey(host, port)
}

func sameImported(row *model.NodeInbound, node kernel.ImportedNode, extra string) bool {
	// IMP-009：名称/地址匹配大小写不敏感。服务商改名或主机名大小写抖动时，
	// 严格相等会把同一节点误判为"已更新"（甚至重复导入）；地址侧 externalKey
	// 本就小写归一化，这里统一用 EqualFold 与之对齐。
	return strings.EqualFold(row.Name, truncateRunes(node.Name, 100)) &&
		strings.EqualFold(row.Listen, node.Host) && row.Port == node.Port && row.Extra == extra
}

// syncRemoteSource fetches the source (a file source takes the uploaded
// content) and brings its imported nodes in line: nodes are matched by
// identity first (address + protocol + credentials + shape), then by
// name (providers keep names when they move a server).
func (h *Handler) syncRemoteSource(ctx context.Context, src *model.RemoteSource, upload string) (remoteSyncResult, error) {
	remoteSourceMu.Lock()
	defer remoteSourceMu.Unlock()
	res := remoteSyncResult{Errors: []string{}, Status: remoteSyncStatusOK}
	// gateErrs 是完整性门的判定集：只有解析/转换/写入错误计入。展示
	// 专用提示（F-025 重复条目）经 noticeErr 只进 Errors——重复条目每
	// 个都被处理过（首条入册、余者跳过），不构成「本轮不完整」，计入
	// 门会让含重复条目的源永远等不到干净轮、已下线节点永不回收。
	gateErrs := 0
	gateErr := func(msg string) {
		res.Errors = append(res.Errors, msg)
		gateErrs++
	}
	noticeErr := func(msg string) {
		res.Errors = append(res.Errors, msg)
	}
	now := time.Now().UnixMilli()
	fail := func(err error) (remoteSyncResult, error) {
		// RAW291：last_error 经源列表 API 外显（remoteSourceView），
		// 仓储原文（已标 repoError）不得写入——原文只记日志，
		// 库里只留固定文案；fetch/解析类固定文案照常写回。
		lastErr := err.Error()
		if isRepoError(err) {
			log.Printf("remote source %d sync failed (storage): %v", src.ID, err)
			lastErr = "内部错误，请稍后重试"
		}
		_ = h.repo.UpdateRemoteSourceStatus(src.ID, map[string]interface{}{"last_fetch": now, "last_error": truncate(lastErr, 500)})
		return res, err
	}

	body, userinfo := upload, ""
	if sourceKind(src) == model.RemoteSourceFile {
		if strings.TrimSpace(upload) == "" {
			return fail(fmt.Errorf("文件订阅源不能自动更新，请重新上传文件"))
		}
	} else {
		var err error
		if body, userinfo, err = fetchSubscriptionInfo(ctx, src.URL); err != nil {
			return fail(fmt.Errorf("获取订阅失败：%v", err))
		}
	}
	nodes, parseErrs := kernel.ParseImport(body)
	if len(nodes) == 0 {
		msg := "订阅里没有可用的节点"
		if len(parseErrs) > 0 {
			msg += "：" + parseErrs[0]
		}
		// An empty answer is more likely a provider hiccup than a plan
		// without servers: keep the current nodes.
		return fail(fmt.Errorf("%s", msg))
	}
	// R2-017（B-sub-06）：截断判定改在批内去重之后。旧口径按解析后
	// 原始条数判，溢出部分纯为重复条目时（501 条/500 唯一）每轮恒触
	// 发截断门、下线节点永不回收。去重键与主循环的 F-025 判定同口径
	// （地址取 externalIdentityKey 身份键＝地址＋协议＋凭据＋形态），
	// 被跳过的重复条目在此即发同一条展示提示；主循环的 seen 判定保留
	// 作第二道防线（口径一致、正常轮次不再触发）。J7-01：地址腿旧实现
	// 误用裸 host:port 的 importedAddrKey（那是删除屏蔽元组的粗粒度
	// 腿），与主循环不同构——同址同端口靠协议/凭据区分的条目在预去重
	// 即被当重复条目吞掉、永远同步不进来，已改齐为身份键。
	// W3/I-003：去重谓词去掉名称腿——旧实现名称与身份任一命中即跳过，
	// 同名不同址（机场同名多线路是常态）第二条起整批被当重复条目
	// 吞掉。现在只有身份完全相同的才是重复条目；同名异身份条目照常
	// 进主循环，新建时经 disambiguateExternalName 后缀消解（与手动
	// 导入同口径），不丢节点也不建同名行。
	{
		seenPre := map[string]bool{}
		unique := make([]kernel.ImportedNode, 0, len(nodes))
		for _, node := range nodes {
			addrKey := "id:" + externalIdentityKey(node.Host, node.Port, node.Proxy)
			if seenPre[addrKey] {
				noticeErr(fmt.Sprintf("%s：订阅源内重复条目，已跳过", node.Name))
				continue
			}
			seenPre[addrKey] = true
			unique = append(unique, node)
		}
		nodes = unique
	}
	if len(nodes) > maxImportNodes {
		// C1-05：截断除 Errors 文案外必须落显式状态（Status/
		// Truncated），消费方据此分支——手动 refresh 与后台同步共
		// 用本路，两面同结果。语义与手动导入的整单拒绝刻意不同，
		// 理由见 remoteSyncResult.Truncated 字段注释。
		res.Truncated = len(nodes) - maxImportNodes
		res.Status = remoteSyncStatusTruncated
		nodes = nodes[:maxImportNodes]
		// 截断意味着确有条目未被处理，计入门（与 F-025 重复提示不同）。
		gateErr(fmt.Sprintf("只保留前 %d 个节点", maxImportNodes))
	}
	for _, e := range parseErrs {
		gateErr(e)
	}

	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		// RAW291：仓储直通错误标 repoError——fail() 的 last_error
		// 与 respondRemoteSync 的 Msg 凭此标记改走固定文案。
		return fail(markRepoError(err))
	}
	// R10-04：本源的删除屏蔽集（管理员手工删过的导入节点）。查询失
	// 败 fail-closed 中止整轮——屏蔽读不到时照常新建会把已删节点静
	// 默复活，与守卫查询失败同口径。
	shieldRows, err := h.repo.ListRemoteSourceShields(src.ID)
	if err != nil {
		// RAW291：同上。
		return fail(markRepoError(err))
	}
	shielded := make(map[string]bool, len(shieldRows))
	for _, s := range shieldRows {
		shielded[s.IdentityKey] = true
	}
	// X04-001：本轮同步的链式引用查询（更新分支重下发、移除分支守卫）
	// 共用这份已加载快照的索引——旧实现逐行经 chainReferrers /
	// applyChainReferrers 各重载一次全表。同步更新只改地址/名称等
	// 字段、不动 OutboundID 与分流，快照的引用边整轮有效；移除分支
	// 已删行另以 deletedIDs 滤除（与逐行现查口径一致）。
	guardIx := chainRefIndexFromRows(rows)
	var mine []*model.NodeInbound
	byName := map[string][]*model.NodeInbound{}
	byKey := map[string][]*model.NodeInbound{}
	for i := range rows {
		row := &rows[i]
		if row.SourceID != src.ID || !isExternalInbound(row) {
			continue
		}
		mine = append(mine, row)
		// IMP-009：按名称匹配时大小写不敏感（providers 会抖动名称大小写）。
		byName[strings.ToLower(row.Name)] = append(byName[strings.ToLower(row.Name)], row)
		// 地址侧索引用身份键（地址+协议+凭据）：同网关同端口、靠凭据
		// 区分的节点（住宅代理按用户名分会话）是不同节点，不得只按
		// host:port 互相顶替。
		byKey[externalRowKey(row)] = append(byKey[externalRowKey(row)], row)
	}
	used := map[int64]bool{}
	// W3/I-003：名称占用集（全部导入行、不限来源）——同名异身份条目
	// 新建时经 disambiguateExternalName 后缀消解，与手动导入同口径。
	usedNames := map[string]bool{}
	for i := range rows {
		if isExternalInbound(&rows[i]) {
			usedNames[strings.ToLower(rows[i].Name)] = true
		}
	}
	take := func(list []*model.NodeInbound) *model.NodeInbound {
		for _, row := range list {
			if !used[row.ID] {
				used[row.ID] = true
				return row
			}
		}
		return nil
	}

	// F-025: 批内去重。byName/byKey 只是同步开始时的快照，本轮新建
	// 的行不回填映射——源内重复条目（同身份）会第二条再走新建分支：
	// 重复建行（external 行 kernel_id=0，部分唯一索引不兜底），后续
	// 轮次还在两行间漂移匹配。命中批内已处理的身份即跳过并在结果里
	// 提示，不再重复建行。W3/I-003：去重只认身份键，不再认名称键——
	// 同名异身份是不同节点（见预去重块注释），靠新建分支的名称后缀
	// 消解保持可区分。
	seen := map[string]bool{}

	for _, node := range nodes {
		fresh, err := externalRow(node, src.RelayOnly == 1, now)
		if err != nil {
			// RAW291：externalRow 唯一错误源是 json.Marshal（纯函
			// 数、无仓储调用），文本源于订阅内容本身，原样进 Errors。
			gateErr(fmt.Sprintf("%s：%v", node.Name, err))
			continue
		}
		fresh.SourceID = src.ID
		addrKey := "id:" + externalIdentityKey(node.Host, node.Port, node.Proxy)
		if seen[addrKey] {
			noticeErr(fmt.Sprintf("%s：订阅源内重复条目，已跳过", node.Name))
			continue
		}
		seen[addrKey] = true
		// W3/I-003：匹配顺序改身份优先、名称次之——身份命中是无歧义
		// 的同一节点（服务商改名/改凭据都经它更新）；名称匹配保留给
		// 「同名搬家/换凭据」（providers keep names when they move a
		// server）。旧顺序名称优先在同名多节点时会把条目更到错误的行
		// 上（身份对不上、再被保存期去重复检拒收计入门）。
		row := take(byKey[externalIdentityKey(node.Host, node.Port, node.Proxy)])
		if row == nil {
			row = take(byName[strings.ToLower(fresh.Name)])
		}
		if row == nil {
			// R10-04：命中删除屏蔽的条目不新建——管理员手工删过的
			// 节点不被同步复活。按身份元组判（importedIdentityKey），
			// 改名/改址后的条目是新元组、不在此列。只计数点名，不
			// 计入完整性门：该条目已被处理（有意跳过），计入门会
			// 让含屏蔽条目的源永远等不到干净轮、下线节点永不回收
			// （与 F-025 重复提示同理）。
			if shielded[importedIdentityKey(node.Name, node.Host, node.Port)] {
				res.Shielded++
				continue
			}
			// W3/I-003：同名异身份条目走到新建分支时与已占用名消解
			// （后缀 (2)、(3)…），与手动导入 disambiguateExternalName
			// 同口径——不丢节点，也不建无法区分的同名行。
			fresh.Name = disambiguateExternalName(fresh.Name, usedNames)
			// AUDIT-R3 (F2X-001): per-node 端口分配锁（CreateNodeInbound 内部已去锁）。
			portMu := repo.AcquirePortAllocLock(fresh.NodeID)
			err := h.repo.CreateNodeInbound(&fresh)
			repo.ReleasePortAllocLock(portMu)
			if err != nil {
				// RAW291：建行错误只可能是仓储原文（本面无友好
				// 映射），原文只记日志、Errors 里只留固定文案。
				log.Printf("remote source %d sync create inbound failed node=%q err=%v", src.ID, node.Name, err)
				gateErr(fmt.Sprintf("%s：保存失败", node.Name))
				continue
			}
			res.Added++
			continue
		}
		// IMP-003：RelayOnly 变化也必须走更新分支，否则 sameImported 判"相同"
		// 直接跳过，订阅源的"只保留中转线路"开关不会同步到已有节点。
		if sameImported(row, node, fresh.Extra) && row.RelayOnly == fresh.RelayOnly {
			continue
		}
		moved := row.Listen != fresh.Listen || row.Port != fresh.Port
		row.Name, row.Protocol, row.Listen, row.Port = fresh.Name, fresh.Protocol, fresh.Listen, fresh.Port
		row.ShareHost, row.Extra, row.UpdatedTime = fresh.ShareHost, fresh.Extra, now
		// IMP-003：订阅源"只保留中转线路"开关变更要同步到已有节点。旧逻辑更新
		// 分支从不写 RelayOnly，改了开关后老节点行为不变，必须删源重建才生效。
		row.RelayOnly = fresh.RelayOnly
		// B4/F-132: 更新分支此前无锁无去重——地址变更可与手动导入/编辑
		// 并发撞出重复 (host,port)。在 0 号锁内复检去重后再保存（与
		// 导入、编辑路径同一临界区口径）。
		saveErr := func() error {
			portMu := repo.AcquirePortAllocLock(0)
			defer repo.ReleasePortAllocLock(portMu)
			// R4X-007 普查顺改（B 表二去重门）：复检查询失败 fail-closed
			// 中止本次保存并记错——旧实现 err 被 `err == nil` 门吞掉后
			// 无条件保存，并发下可撞出重复 (host,port) 行。去重是本
			// 临界区唯一防线，查不了就不写。
			rowsNow, err := h.repo.ListNodeInbounds()
			if err != nil {
				// RAW291：仓储直通错误标 repoError，调用点凭此
				// 标记改走固定文案；下方同址重复固定文案不标记。
				return markRepoError(err)
			}
			key := externalRowKey(row)
			for i := range rowsNow {
				other := &rowsNow[i]
				if other.ID != row.ID && isExternalInbound(other) && externalRowKey(other) == key {
					return fmt.Errorf("已存在地址、端口与凭据完全相同的导入节点（%s）", externalKey(row.Listen, row.Port))
				}
			}
			// RAW291：仓储直通错误标 repoError，调用点凭此标记
			// 改走固定文案；同址重复固定文案不标记。
			if saveErr := h.repo.SaveNodeInbound(row); saveErr != nil {
				return markRepoError(saveErr)
			}
			return nil
		}()
		if saveErr != nil {
			if isRepoError(saveErr) {
				log.Printf("remote source %d sync update inbound failed node=%q err=%v", src.ID, node.Name, saveErr)
				gateErr(fmt.Sprintf("%s：保存失败", node.Name))
			} else {
				gateErr(fmt.Sprintf("%s：%v", node.Name, saveErr))
			}
			continue
		}
		// W3/I-003：更新后的名字（可能被服务商改名）登记进占用集，
		// 本轮后续新建条目据此消解同名。
		usedNames[strings.ToLower(row.Name)] = true
		res.Updated++
		if moved {
			for _, p := range h.retargetRelays(row) {
				gateErr(row.Name + "：" + p)
			}
		}
		for _, p := range h.applyChainReferrersIx(guardIx, row.ID) {
			gateErr(row.Name + "：" + p)
		}
	}

	// 完整性门：本轮出现任何解析/转换/写入错误（gateErrs）时，「未
	// 匹配」不能证明节点已被订阅源删除——未匹配旧节点一律保留，
	// 等下一轮干净同步再裁决删除。判定集不含展示专用
	// 提示：批内去重（F-025）的重复条目每个都被处理过（首条入册、
	// 余者跳过），不是未处理条目，计入门会让这类源永远等不到干净
	// 轮；超量截断则确有条目未处理，仍计入。门在删除循环开始时按
	// 当时判定集判定；循环内守卫自身追加的错误不回溯影响本轮其余
	// 行（与逐行 fail-closed 守卫并存）。门保留计入 KeptStale，与中
	// 转/链式守卫保留（Kept）在计数与摘要上分开。
	complete := gateErrs == 0

	deletedIDs := make(map[int64]bool)
	for _, row := range mine {
		if used[row.ID] {
			continue
		}
		if !complete {
			res.KeptStale++
			continue
		}
		// R4X-007：守卫查询失败 fail-closed（与下方 chainReferrers
		// 分支同形）——旧实现 `err == nil &&` 把查询错误折叠为「无
		// 中转」，瞬时 DB 失败即级联删中转与用户分配（R3X-015 同一
		// 删除决策漏改的紧邻一行）。
		relays, relErr := h.repo.ListInboundRelays(row.ID)
		if relErr != nil {
			// R2-018（B-sub-07）：此分支里中转存在性恰是未知（查询
			// 失败），单独计 KeptUnknown，摘要不得与「有中转，已保留」
			// 混同。
			res.Errors = append(res.Errors, fmt.Sprintf("%s：%v", row.Name, relErr))
			res.KeptUnknown++
			continue
		}
		if len(relays) > 0 {
			res.Kept++
			continue
		}
		// R3X-015：守卫查询失败 fail-closed——无法确认无引用时保留
		// 该节点并记错，不得当作无引用删除。X04-001 后引用集取自
		// 本轮开局已成功加载的快照（加载失败在函数入口已中止整轮），
		// 本轮内已删行经 deletedIDs 滤除，与逐行现查口径一致。
		var refs []model.NodeInbound
		for _, ref := range guardIx.referrers(row.ID) {
			if !deletedIDs[ref.ID] {
				refs = append(refs, ref)
			}
		}
		if len(refs) > 0 {
			res.Kept++
			continue
		}
		if err := h.repo.DeleteNodeInbound(row.ID); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s：%v", row.Name, err))
			continue
		}
		deletedIDs[row.ID] = true
		res.Removed++
	}

	// B4-S3（随 R4X-007 批顺手）：末尾计数查询失败时跳过状态写回——
	// 旧实现失败时 count 保持 0 仍连同 last_error 清空一并写回，把
	// 卡片节点数误显 0；跳过写回后保留上轮状态，下轮同步自愈。
	rowsTail, tailErr := h.repo.ListNodeInbounds()
	if tailErr != nil {
		log.Printf("remote source %d sync: tail count failed, status write skipped: %v", src.ID, tailErr)
		res.Errors = append(res.Errors, fmt.Sprintf("同步完成但节点计数失败：%v", tailErr))
		return res, nil
	}
	count := 0
	for _, row := range rowsTail {
		if row.SourceID == src.ID {
			count++
		}
	}
	_ = h.repo.UpdateRemoteSourceStatus(src.ID, map[string]interface{}{
		"last_fetch": now, "last_error": "", "node_count": count, "userinfo": userinfo,
	})
	return res, nil
}

func remoteSourceDue(s *model.RemoteSource, now time.Time) bool {
	if s.Enabled != 1 || s.IntervalHours <= 0 || sourceKind(s) == model.RemoteSourceFile {
		return false
	}
	return now.Sub(time.UnixMilli(s.LastFetch)) >= time.Duration(s.IntervalHours)*time.Hour
}

func (h *Handler) refreshDueRemoteSources(ctx context.Context, now time.Time) {
	sources, err := h.repo.ListRemoteSources()
	if err != nil {
		return
	}
	for i := range sources {
		s := &sources[i]
		if !remoteSourceDue(s, now) {
			continue
		}
		if _, err := h.syncRemoteSource(ctx, s, ""); err != nil {
			log.Printf("remote source %d refresh failed: %v", s.ID, err)
		}
	}
}

func (h *Handler) runRemoteSourceLoop(ctx context.Context) {
	defer h.jobsWG.Done()
	ticker := time.NewTicker(remoteSourceCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		h.refreshDueRemoteSources(ctx, time.Now())
		h.refreshDueTemplates(ctx, time.Now())
	}
}

func (h *Handler) remoteSourceList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	sources, err := h.repo.ListRemoteSources()
	if err != nil {
		respondInternalError(w, "remoteSourceList", err)
		return
	}
	out := make([]remoteSourceView, 0, len(sources))
	for i := range sources {
		out = append(out, remoteSourceViewOf(&sources[i]))
	}
	response.WriteJSON(w, response.OK(out))
}

// remoteSourceSave creates or edits a source; a new one (or a changed URL, a
// changed relayOnly flag — IMP-003, or an uploaded file) is synced right away.
func (h *Handler) remoteSourceSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		URL  string `json:"url"`
		Kind string `json:"kind"`
		Role string `json:"role"`
		// Content is an uploaded file (file sources).
		Content       string `json:"content"`
		IntervalHours int    `json:"intervalHours"`
		RelayOnly     bool   `json:"relayOnly"`
		Enabled       *bool  `json:"enabled"`
	}
	// A JSON-escaped file can be larger than the file itself. decodeJSON
	// 本就限 10MB，此处再以 2×5MB 同口径收紧一层，有效上限不变（仍 10MB）。
	if err := decodeJSON(http.MaxBytesReader(w, r.Body, 2*maxSubscriptionBytes), &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误或文件超过 5MB"))
		return
	}
	// R2X-004：仅 ID==0 新建分支接入 Idempotency-Key（机制见
	// idempotency.go；ID>0 为按 ID 定位的更新，不参与回放）。
	if req.ID == 0 {
		var finish func()
		var replayed bool
		w, finish, replayed = h.idemBegin(w, r, idemUserID(r), "remote-source:create")
		defer finish()
		if replayed {
			return
		}
	}
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)
	if req.Name == "" || len([]rune(req.Name)) > 100 {
		response.WriteJSON(w, response.ErrDefault("请填写名称（最多 100 个字）"))
		return
	}
	if req.Kind != model.RemoteSourceFile {
		req.Kind = model.RemoteSourceURL
	}
	if req.Role != "relay" && req.Role != "landing" {
		req.Role = ""
	}
	if len(req.Content) > maxSubscriptionBytes {
		response.WriteJSON(w, response.ErrDefault("文件超过 5MB"))
		return
	}
	if req.Kind == model.RemoteSourceURL && !isSubscriptionURL(req.URL) {
		response.WriteJSON(w, response.ErrDefault("请填写 http(s) 订阅地址"))
		return
	}
	// 终审 L1-F-08：订阅源 URL 保存期对主机名做语法层空标签判（只判
	// 语法、不做 DNS 解析；同步/拉取期 fail-closed 语义不动）。
	if req.Kind == model.RemoteSourceURL {
		if err := validateNoEmptyLabelURLHost(req.URL); err != nil {
			// RAW291 拼接形态（普查谓词漏计，本路补登）：保留原
			// 文——validateNoEmptyLabelURLHost 的唯一错误是固定格式
			// `主机名 %q 包含空标签`（security_utils.go），%q 内
			// 仅为管理员自填 URL 的主机名，无仓储/内部文本可泄漏；
			// 解析失败时该函数返回 nil、不产错误。见报告 §4。
			response.WriteJSON(w, response.ErrDefault("订阅地址主机名格式无效："+err.Error()))
			return
		}
	}
	if req.IntervalHours < 0 || req.IntervalHours > 24*7 {
		response.WriteJSON(w, response.ErrDefault("更新间隔不正确"))
		return
	}
	now := time.Now().UnixMilli()
	src := &model.RemoteSource{CreatedTime: now, Enabled: 1}
	if req.ID > 0 {
		var err error
		if src, err = h.repo.GetRemoteSource(req.ID); err != nil || src == nil {
			response.WriteJSON(w, response.ErrDefault("订阅源不存在"))
			return
		}
	}
	if req.Kind == model.RemoteSourceFile {
		req.URL, req.IntervalHours = "", 0
		if strings.TrimSpace(req.Content) == "" && (src.ID == 0 || sourceKind(src) != model.RemoteSourceFile) {
			response.WriteJSON(w, response.ErrDefault("请选择订阅文件"))
			return
		}
	} else {
		req.Content = ""
	}
	// IMP-003：RelayOnly 变更也触发立即同步（之前还需删源重建才生效）。
	fetch := src.ID == 0 || src.URL != req.URL || sourceKind(src) != req.Kind || req.Content != "" || src.RelayOnly != boolToInt(req.RelayOnly)
	// A6-001：文件源无新上传时唯一的 fetch 触发只可能是 RelayOnly 变化，
	// 而整源同步对文件源无 upload 必然失败（"文件订阅源不能自动更新"）—
	// 旧路径把假 last_error 写回源记录，且节点行 RelayOnly 永不更新，
	// 管理员与实际收紧状态分叉。覆写前先记住是否为这种「仅改
	// RelayOnly」的情形，保存后直接把新值落到导入节点行。
	relayOnlyOnlyChange := src.ID > 0 && sourceKind(src) == model.RemoteSourceFile &&
		req.Kind == model.RemoteSourceFile && strings.TrimSpace(req.Content) == "" &&
		src.RelayOnly != boolToInt(req.RelayOnly)
	src.Name, src.URL, src.IntervalHours, src.Kind, src.Role = req.Name, req.URL, req.IntervalHours, req.Kind, req.Role
	src.RelayOnly = boolToInt(req.RelayOnly)
	if req.Enabled != nil {
		src.Enabled = boolToInt(*req.Enabled)
	}
	src.UpdatedTime = now
	if err := h.repo.SaveRemoteSource(src); err != nil {
		respondInternalError(w, "remoteSourceSave", err)
		return
	}
	if relayOnlyOnlyChange {
		h.applyRemoteSourceRelayOnly(src)
		// 判空＋查错双条件取值：重读干净成功才以库内副本刷新 src，
		// 失败保留手头刚落库的 src（权威副本），不产生空集。
		if saved, rerr := h.repo.GetRemoteSource(src.ID); rerr == nil && saved != nil {
			src = saved
		}
		response.WriteJSON(w, response.OK(remoteSourceViewOf(src)))
		return
	}
	if !fetch {
		// R3-MR-01：与上方 relayOnly 分支同形判空＋查错取值——重读
		// 失败（或行已消失）时保留手头刚落库的 src，禁止以
		// (nil, err) 覆盖后在 remoteSourceViewOf 解引用（旧形态靠
		// 上层 recover 兜成 500，保存已成功却报内部错误）。
		if saved, rerr := h.repo.GetRemoteSource(src.ID); rerr == nil && saved != nil {
			src = saved
		}
		// IMP-006：保存时若为公网 http 地址，提前提示同步必败（SecureFetch 对
		// 公网强制 https）。精确判定在同步时由 SecureFetch 按解析 IP 做。
		if isLikelyPublicHTTP(req.URL) {
			response.WriteJSON(w, response.R{Code: 0, Msg: "订阅源已保存。注意：公网 http 地址在同步时会被拒绝，建议改用 https 地址。", Data: remoteSourceViewOf(src), TS: now})
			return
		}
		response.WriteJSON(w, response.OK(remoteSourceViewOf(src)))
		return
	}
	h.respondRemoteSync(w, r.Context(), src, req.Content)
}

// isLikelyPublicHTTP 粗略判断订阅地址是否为公网 http（IMP-006，保存时提示用）。
// 域名在保存时不做 DNS 解析，一律按"可能公网"提示；IP 字面量则按私网/回环豁免。
func isLikelyPublicHTTP(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !isPrivateIP(ip)
	}
	return true
}

// applyRemoteSourceRelayOnly 把订阅源的 RelayOnly 新值直接落到其导入
// 节点行（A6-001）：与同步路径的更新分支同口径——改值后对引用了该
// 入站的链式中转补一次 applyChainReferrers（地址未变，无需 retarget）。
// 与整源同步共用 remoteSourceMu 串行化，避免与定时刷新竞态。
func (h *Handler) applyRemoteSourceRelayOnly(src *model.RemoteSource) {
	remoteSourceMu.Lock()
	defer remoteSourceMu.Unlock()
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		log.Printf("remote source %d relay-only apply list failed: %v", src.ID, err)
		return
	}
	// X04-001：引用方查询共用这份已加载快照（旧实现逐行重载全表）；
	// 本路径只改 RelayOnly，引用边整轮不变。
	guardIx := chainRefIndexFromRows(rows)
	now := time.Now().UnixMilli()
	for i := range rows {
		row := &rows[i]
		if row.SourceID != src.ID || !isExternalInbound(row) {
			continue
		}
		if row.RelayOnly == src.RelayOnly {
			continue
		}
		row.RelayOnly = src.RelayOnly
		row.UpdatedTime = now
		if err := h.repo.SaveNodeInbound(row); err != nil {
			log.Printf("remote source %d relay-only apply inbound %d failed: %v", src.ID, row.ID, err)
			continue
		}
		for _, problem := range h.applyChainReferrersIx(guardIx, row.ID) {
			log.Printf("remote source %d relay-only chain re-apply inbound %d: %s", src.ID, row.ID, problem)
		}
	}
}

func (h *Handler) respondRemoteSync(w http.ResponseWriter, ctx context.Context, src *model.RemoteSource, upload string) {
	res, err := h.syncRemoteSource(ctx, src, upload)
	saved, _ := h.repo.GetRemoteSource(src.ID)
	if saved == nil {
		saved = src
	}
	data := map[string]interface{}{"source": remoteSourceViewOf(saved), "result": res}
	if err != nil {
		// RAW291：syncRemoteSource 的仓储直通错误（已标 repoError）
		// 只记日志、回执通用文案；fetch/解析类固定文案原样回。
		// 形态保持 Code -1 + Data 不变（前端按 -1 识别同步失败
		// 并展示 result.Errors），只换 Msg。
		msg := err.Error()
		if isRepoError(err) {
			log.Printf("respondRemoteSync: syncRemoteSource failed (storage): %v", err)
			msg = "Internal error"
		}
		response.WriteJSON(w, response.R{Code: -1, Msg: msg, Data: data, TS: time.Now().UnixMilli()})
		return
	}
	response.WriteJSON(w, response.R{Code: 0, Msg: res.summary(), Data: data, TS: time.Now().UnixMilli()})
}

func (h *Handler) remoteSourceRefresh(w http.ResponseWriter, r *http.Request) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	src, err := h.repo.GetRemoteSource(id)
	if err != nil || src == nil {
		response.WriteJSON(w, response.ErrDefault("订阅源不存在"))
		return
	}
	if sourceKind(src) == model.RemoteSourceFile {
		response.WriteJSON(w, response.ErrDefault("文件订阅源请在编辑里重新上传文件"))
		return
	}
	h.respondRemoteSync(w, r.Context(), src, "")
}

// shieldDeletedImportedRow登记一条删除屏蔽（R10-04）：管理员手工删除
// 带源的导入行后，其身份元组记入该源的屏蔽集，订阅源后续同步不再
// 自动恢复它。无源行（SourceID<=0，普通手工导入）没有会复活它的同
// 步，跳过。单删与批删共用本助手。
func (h *Handler) shieldDeletedImportedRow(row *model.NodeInbound) error {
	if row == nil || row.SourceID <= 0 {
		return nil
	}
	return h.repo.AddRemoteSourceShield(&model.RemoteSourceShield{
		SourceID:    row.SourceID,
		IdentityKey: importedIdentityKey(row.Name, row.Listen, row.Port),
		Name:        row.Name,
		CreatedTime: time.Now().UnixMilli(),
	})
}

// remoteSourceDelete removes a source and, unless keepNodes is set, its
// nodes (with their relays).
func (h *Handler) remoteSourceDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		ID        int64 `json:"id"`
		KeepNodes bool  `json:"keepNodes"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	remoteSourceMu.Lock()
	defer remoteSourceMu.Unlock()
	// B10-001（R3-K9，分诊 §5.1 裁定 (a)）：与同面 refresh/save 同口径，
	// 先校验订阅源存在性——旧实现对不存在 id 静默返回成功（不在 S-1
	// 单删幂等清单内），管理员删错 id 仍得成功回执、目标源继续被刷新。
	if src, err := h.repo.GetRemoteSource(req.ID); err != nil || src == nil {
		response.WriteJSON(w, response.ErrDefault("订阅源不存在"))
		return
	}
	if req.KeepNodes {
		if err := h.repo.DetachSourceInbounds(req.ID); err != nil {
			respondInternalError(w, "remoteSourceDelete", err)
			return
		}
	} else {
		// R3X-015：守卫数据源（入站列表）查询失败 fail-closed 中止
		// 删除——旧实现 err != nil 时整段跳过、仍删来源行；守卫本身
		// 查询失败同理中止，不得当作无引用放行。
		rows, err := h.repo.ListNodeInbounds()
		if err != nil {
			respondInternalError(w, "remoteSourceDelete", err)
			return
		}
		// X04-001：守卫共用这份已加载快照（旧实现逐行 chainInUseMessage
		// 各重载一次全表）；加载已成功，逐行不再有查询失败面，删除在
		// 守卫全过后才开始，快照与逐行现查等价。
		guardIx := chainRefIndexFromRows(rows)
		for i := range rows {
			if rows[i].SourceID != req.ID {
				continue
			}
			if msg := h.chainInUseMessageFromRefs(&rows[i], guardIx.referrers(rows[i].ID)); msg != "" {
				response.WriteJSON(w, response.ErrDefault(msg+"，或删除时保留节点"))
				return
			}
		}
		for _, row := range rows {
			if row.SourceID != req.ID {
				continue
			}
			h.removeInboundRelays(row.ID)
			if err := h.repo.DeleteNodeInbound(row.ID); err != nil {
				respondInternalError(w, "remoteSourceDelete", err)
				return
			}
		}
	}
	// R4-F1-01：删源同步清理订阅引用并在回执点名受影响订阅——此前
	// 订阅的 SourceIDs/链式按源留死 ID、渲染静默变空，管理员选
	// 「保留节点」时更以为节点仍在订阅内。与屏蔽清理同序（删源行
	// 之前、失败中止），语义取「清理＋点名」：死引用在渲染面本就
	// 按无此源处理，清理只是把存储与既定行为对齐；点名保证受影
	// 响订阅不静默减少（keepNodes 与连节点删两路同口径，源行删
	// 除这一事实不因节点去向而变）。
	affectedSubs, err := h.stripRemoteSourceFromSubscriptions(req.ID)
	if err != nil {
		respondInternalError(w, "remoteSourceDelete", err)
		return
	}
	// R10-04：删除屏蔽随源删除而清（keepNodes 与连节点删两路同口径）；
	// 在删源行之前清、失败中止——屏蔽不得比源本身活得久。
	if err := h.repo.DeleteRemoteSourceShields(req.ID); err != nil {
		respondInternalError(w, "remoteSourceDelete", err)
		return
	}
	if err := h.repo.DeleteRemoteSource(req.ID); err != nil {
		respondInternalError(w, "remoteSourceDelete", err)
		return
	}
	if len(affectedSubs) > 0 {
		quoted := make([]string, 0, len(affectedSubs))
		for _, name := range affectedSubs {
			quoted = append(quoted, "「"+name+"」")
		}
		response.WriteJSON(w, response.OKWithWarning(nil, fmt.Sprintf("该订阅源还被订阅 %s 引用，已从这些订阅中移除该源引用，相关订阅内容已减少", strings.Join(quoted, "、"))))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

// stripRemoteSourceFromSubscriptions 把已删除订阅源的 ID 从全部订阅
// 的源引用中摘除（SourceIDs），返回受影响的订阅名（订阅 ID 序）。
// （2.1.2 前 Options 内的链式按源引用一并在此摘除；订阅链式删除后
// Options 不再持有源引用，本函数只剩 SourceIDs 一面。）
func (h *Handler) stripRemoteSourceFromSubscriptions(sourceID int64) ([]string, error) {
	subs, err := h.repo.ListSubscriptions()
	if err != nil {
		return nil, err
	}
	var affected []string
	for i := range subs {
		s := &subs[i]
		changed := false
		if ids := parseIDList(s.SourceIDs); int64SliceContains(ids, sourceID) {
			s.SourceIDs = formatIDList(int64SliceRemove(ids, sourceID))
			changed = true
		}
		if !changed {
			continue
		}
		if err := h.repo.SaveSubscription(s); err != nil {
			return affected, err
		}
		affected = append(affected, s.Name)
	}
	return affected, nil
}

func int64SliceContains(ids []int64, target int64) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func int64SliceRemove(ids []int64, target int64) []int64 {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id != target {
			out = append(out, id)
		}
	}
	return out
}
