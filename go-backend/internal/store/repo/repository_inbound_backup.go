package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// sanitizeBackupShareHost applies the ShareHost write-surface standard
// (kernel.ValidateImportHost) to a share host coming back from a backup:
// a malformed value is cleared — the row then resolves by its mode/node
// address — instead of being restored verbatim (F-A-01/C-01). Callers
// count the clears and disclose them in the import warnings. A value
// whose only flaw is a single trailing dot (the FQDN root dot) is
// restored in normalized form instead of being cleared (B2-01), and
// any other normalizable form (a root dot around the brackets —
// [2001:db8::9]. — or stray surrounding whitespace) restores in the
// same canonical form the save and pull faces produce
// (kernel.CanonicalShareHostValue), so all three faces agree on one
// value (R3-A-01). Values already canonical restore verbatim.
func sanitizeBackupShareHost(host string) (string, bool) {
	if host == "" {
		return host, true
	}
	canonical := kernel.CanonicalShareHostValue(host)
	if kernel.ValidateImportHost(canonical) != nil {
		return "", false
	}
	if strings.TrimSpace(host) == host && canonical == strings.Trim(host, "[]") {
		return host, true
	}
	return canonical, true
}

// sanitizeBackupShareAddressMode whitelists the share address mode on
// the way in from a backup (R3-E-02): the save surface only ever
// writes the enum below, so anything else in a backup is foreign data
// — it restores as auto and reports false so the caller counts it into
// the import warnings. An unfiltered mode string would otherwise be
// stored verbatim and flow into views and pull-time log lines. The
// empty value (backups from before the mode existed) restores as-is:
// every read face already treats it as auto.
func sanitizeBackupShareAddressMode(mode string) (string, bool) {
	trimmed := strings.TrimSpace(mode)
	switch trimmed {
	case "":
		return "", true
	case "auto", "ipv4", "ipv6", "custom":
		return trimmed, true
	default:
		return "auto", false
	}
}

// sanitizeBackupWhitelistExtra applies the save surface's allow-list
// standard (kernel.ParseAllowList, handler inboundSave) to an inbound's
// WhitelistExtra coming back from a backup (R9X-008): the field was the
// one address carrier in the inbound backup section restored verbatim
// with no validation and no disclosure — a single bad entry makes the
// consumption face (inboundAllowList) silently ignore the whole extra
// list, fail-closed, with no receipt for the admin.
//
// Disposition is field-level sanitize, not a whole-row skip: the field
// is an auxiliary allow-list on top of the computed one, the inbound
// row itself stays fully usable without it — the same level as
// ShareHost clearing above, and the same shape as the dangling
// split-rules pruning in validateImportedInboundRefsTx (list fields
// prune bad entries, scalar fields clear). Within the list the
// disposition is per-entry pruning: entries the shared predicate
// accepts are kept with their original token text, bad entries are
// dropped and returned so the caller counts and discloses them. A
// fully legal list restores verbatim — validation is read-only and
// never rewrites the stored value with the normalized form (the
// validateImportedInboundObjects口径). A list ParseAllowList rejects
// for a reason no single entry carries (e.g. more than
// maxAllowEntries entries), or whose surviving entries still do not
// parse as a list, clears as a whole and returns the list error for
// disclosure.
//
// The verdict and the per-entry predicate are the save surface's own
// implementations — kernel.ParseAllowList for the list verdict and
// kernel.NormalizeAllowEntry (the exact per-entry check ParseAllowList
// itself delegates to) for pruning. No second allow-list predicate is
// written here; only the token split mirrors ParseAllowList's
// separator set, which has no exported splitter.
func sanitizeBackupWhitelistExtra(raw string) (restored string, bad []string, listErr error) {
	if _, err := kernel.ParseAllowList(raw); err == nil {
		return raw, nil, nil
	} else {
		listErr = err
	}
	tokens := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == ' ' || r == '\n' || r == '\t' || r == '\r'
	})
	kept := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if _, err := kernel.NormalizeAllowEntry(tok); err != nil {
			bad = append(bad, tok)
			continue
		}
		kept = append(kept, tok)
	}
	if len(bad) == 0 {
		// No single entry is at fault (entry-count overflow): the list
		// as a whole is invalid, clear it and let the caller disclose
		// the list-level error.
		return "", nil, listErr
	}
	cleaned := strings.Join(kept, ", ")
	if _, err := kernel.ParseAllowList(cleaned); err != nil {
		// Survivors still do not form a legal list (e.g. still over
		// the entry cap): clear as a whole rather than restore a list
		// the consumption face would ignore anyway.
		return "", bad, listErr
	}
	return cleaned, bad, nil
}

func (r *Repository) exportInbounds() (*model.InboundsBackup, error) {
	out := &model.InboundsBackup{
		Kernels:  []model.KernelBackup{},
		Inbounds: []model.InboundBackup{},
		Relays:   []model.InboundRelayBackup{},
	}

	var kernels []model.NodeKernel
	if err := r.db.Order("id ASC").Find(&kernels).Error; err != nil {
		return nil, err
	}
	for _, k := range kernels {
		out.Kernels = append(out.Kernels, model.KernelBackup{
			ID: k.ID, NodeID: k.NodeID, KernelType: k.KernelType, Version: k.Version,
			Enabled: k.Enabled, CreatedTime: k.CreatedTime, InstalledVersion: k.InstalledVersion,
		})
	}

	// SS 计量伴生行是派生态，不进备份：恢复后由 reconcile 按原入站的
	// metering_enabled 重新物化（其计数基线同理不带，恢复后首样本只立
	// 基线，与基线丢失的既有口径一致）。
	var inbounds []model.NodeInbound
	if err := r.db.Where("companion_of = ?", 0).Order("id ASC").Find(&inbounds).Error; err != nil {
		return nil, err
	}
	exportedInbound := make(map[int64]bool, len(inbounds))
	for _, i := range inbounds {
		exportedInbound[i.ID] = true
		out.Inbounds = append(out.Inbounds, model.InboundBackup{
			ID: i.ID, KernelID: i.KernelID, NodeID: i.NodeID, Name: i.Name, Protocol: i.Protocol,
			Listen: i.Listen, Port: i.Port, Transport: i.Transport, Security: i.Security,
			Settings: i.Settings, Extra: i.Extra, ShareHost: i.ShareHost, ShareAddressMode: i.ShareAddressMode, Remark: i.Remark,
			RelayOnly: i.RelayOnly, AcceptProxyProtocol: i.AcceptProxyProtocol,
			RelayWhitelist: i.RelayWhitelist, WhitelistExtra: i.WhitelistExtra, Enabled: i.Enabled,
			UploadBytes: i.UploadBytes, DownloadBytes: i.DownloadBytes, Inx: i.Inx,
			CreatedTime: i.CreatedTime, SourceID: i.SourceID, OutboundID: i.OutboundID, SplitRules: i.SplitRules,
			OutboundIPStrategy: i.OutboundIPStrategy, MeteringEnabled: i.MeteringEnabled,
			ConnIdleSeconds: i.ConnIdleSeconds,
		})
	}

	var relays []model.NodeInboundRelay
	if err := r.db.Order("id ASC").Find(&relays).Error; err != nil {
		return nil, err
	}
	for _, rel := range relays {
		out.Relays = append(out.Relays, model.InboundRelayBackup{
			ID: rel.ID, InboundID: rel.InboundID, ForwardID: rel.ForwardID, Name: rel.Name,
			ShareHost: rel.ShareHost, Managed: rel.Managed, CreatedTime: rel.CreatedTime,
		})
	}

	var counters []model.NodeInboundCounter
	if err := r.db.Order("id ASC").Find(&counters).Error; err != nil {
		return nil, err
	}
	for _, c := range counters {
		if !exportedInbound[c.InboundID] {
			continue // 伴生入站的计数基线随其行一同排除（见上方）
		}
		out.Counters = append(out.Counters, model.InboundCounterBackup{
			InboundID: c.InboundID, Protocol: c.Protocol, Direction: c.Direction,
			Epoch: c.Epoch, Bytes: c.Bytes,
		})
	}
	var sources []model.RemoteSource
	if err := r.db.Order("id ASC").Find(&sources).Error; err != nil {
		return nil, err
	}
	for _, src := range sources {
		out.Sources = append(out.Sources, model.RemoteSourceBackup{
			ID: src.ID, Name: src.Name, URL: src.URL, IntervalHours: src.IntervalHours,
			RelayOnly: src.RelayOnly, Kind: src.Kind, Role: src.Role, Enabled: src.Enabled, CreatedTime: src.CreatedTime,
			Userinfo: src.Userinfo,
		})
	}
	// A3-04：订阅计数基线与入站基线同等待遇随备份导出。
	var subCounters []model.SubscriptionCounter
	if err := r.db.Order("id ASC").Find(&subCounters).Error; err != nil {
		return nil, err
	}
	for _, c := range subCounters {
		out.SubscriptionCounters = append(out.SubscriptionCounters, model.SubscriptionCounterBackup{
			NodeID: c.NodeID, SubscriptionID: c.SubscriptionID, Direction: c.Direction,
			Epoch: c.Epoch, Bytes: c.Bytes,
		})
	}
	var subs []model.Subscription
	if err := r.db.Order("id ASC").Find(&subs).Error; err != nil {
		return nil, err
	}
	for _, sub := range subs {
		out.Subscriptions = append(out.Subscriptions, model.SubscriptionBackup{
			ID: sub.ID, Name: sub.Name, Kind: sub.Kind, Token: sub.Token, UUID: sub.UUID, Password: sub.Password,
			SourceAll: sub.SourceAll, InboundIDs: sub.InboundIDs, SourceIDs: sub.SourceIDs, Options: sub.Options, ExpireTime: sub.ExpireTime,
			Enabled: sub.Enabled, Remark: sub.Remark, Inx: sub.Inx, CreatedTime: sub.CreatedTime,
			UploadBytes: sub.UploadBytes, DownloadBytes: sub.DownloadBytes, TrafficLimit: sub.TrafficLimit,
			ResetDay: sub.ResetDay, TrafficResetTime: sub.TrafficResetTime, TrafficSince: sub.TrafficSince, PendingDelete: sub.PendingDelete,
		})
	}
	return out, nil
}

// importInbounds upserts kernels, inbounds and relay links by id. Kernels
// come back as pending with no applied config, so the panel pushes them to
// the nodes again (and installs the kernel where it is missing).
//
// subRemap (BUG-A2-4) collects backup subscription id -> restored row id.
// Subscriptions are matched by token, not by backup id: a subscription
// that was deleted after the backup comes back under a fresh id, and
// user_subscription rows in the same backup still carry the old id.
// Import() feeds this map to importUserSubscriptions so assignments are
// rewritten onto the restored rows instead of dangling.
// validateImportedInboundObjects 对备份入站的四整对象
// （settings/transport/security/extra）跑保存面等价校验（R5X-024）：
// 把备份原值解入 kernel.Inbound 副本后调用保存面共用的
// kernel.Normalize——只读副本做校验，落库仍用备份原值逐字恢复，
// 不以归一化结果覆写。kernel_id=0 的导入型入站没有内核上下文、
// 不经 Normalize，但其凭据与地址全在 Extra 承载的代理对象里，须经
// 与 ParseImport 共用的同一谓词 kernel.ValidateImportedProxy 校验
// （凭据列 R6X-008、地址列 R7X-003；只读校验副本，落库仍逐字恢复）。
//
// 适用面：四对象任一有实际内容才进校验门。四对象全空的行是旧式
// 简写/存量行（真实导出行在保存时已经 Normalize 填充，security
// 等对象恒有内容），无可校验的对象内容；若对全空行跑 Normalize，
// 其拒绝源于「保存时本应生成的数据缺席」（如 trojan 空 security
// 缺 SNI），不是对象坏值——该形态维持原恢复口径。此口径在
// R5-FIX-B5 批报告中显式登记。
//
// 例外（R8X-004）：行字段 Listen/Port 不属于任何被校验对象，恒检
// 与内容豁免解耦、置于 hasContent 判定之前——豁免只豁免内容、
// 不豁免地址/端口门。导入型入站（kernel_id≤0）的行内 Listen/Port
// 是导入地址的行内第二载体（externalRow 恒以经 finishProxy 校验的
// Host/Port 双写行字段与 Extra），经 kernel.ValidateImportedProxyRowAddress
// 走与 finishProxy 共用的同一地址实现 validateImportedProxyAddress
// （禁另写第二份谓词，沿 R7X-003 口径；R7X-003 的关闭对象是 Extra
// 载体，本恒检不顶替其断言）；自建入站（kernel_id>0）以 Normalize
// 的行字段两检口径恒校验（Port 1–65535 + Listen 为 IP 字面量，
// listen 已由调用方完成空→"::" 归一）。不要求行内 Listen/Port 与
// Extra 的 server/port 相等（只设合法性门，防过修）。
func validateImportedInboundObjects(tx *gorm.DB, i model.InboundBackup, listen, strategy string) error {
	if i.KernelID <= 0 {
		if err := kernel.ValidateImportedProxyRowAddress(listen, i.Port); err != nil {
			return err
		}
	} else {
		if i.Port < 1 || i.Port > 65535 {
			return errors.New("端口必须在 1-65535 之间")
		}
		if net.ParseIP(listen) == nil {
			return errors.New("监听地址必须是 IP")
		}
	}
	hasContent := strings.TrimSpace(i.Settings) != "" || strings.TrimSpace(i.Transport) != "" ||
		strings.TrimSpace(i.Security) != ""
	if !hasContent {
		if extra := strings.TrimSpace(i.Extra); extra != "" && extra != "null" && extra != "{}" {
			hasContent = true
		}
	}
	if !hasContent {
		return nil
	}
	vin := kernel.Inbound{ID: i.ID, Protocol: i.Protocol, Listen: listen, Port: i.Port,
		AcceptProxyProtocol: i.AcceptProxyProtocol == 1, OutboundIPStrategy: strategy,
		ConnIdleSeconds: i.ConnIdleSeconds}
	for _, f := range []struct {
		raw string
		out interface{}
	}{
		{i.Settings, &vin.Settings},
		{i.Transport, &vin.Transport},
		{i.Security, &vin.Security},
	} {
		if strings.TrimSpace(f.raw) == "" {
			continue
		}
		if err := json.Unmarshal([]byte(f.raw), f.out); err != nil {
			return err
		}
	}
	if strings.TrimSpace(i.Extra) != "" {
		var probe interface{}
		if err := json.Unmarshal([]byte(i.Extra), &probe); err != nil {
			return err
		}
		vin.Extra = json.RawMessage(i.Extra)
	}
	if i.KernelID <= 0 {
		// R6X-008 / R7X-003：导入型入站的凭据与地址不在四整对象
		// 里，而在 Extra 的代理对象中——落库前把 Extra 解为
		// kernel.Proxy 跑与 ParseImport 同一谓词（ValidateImportedProxy
		// 是其薄封装，凭据+地址双列，禁止在此另写等价校验）。校验只作用于解出的副本：谓词的
		// 大小写愈合（如 vmess cipher）不得回写，合法行逐字恢复；
		// 渲染谓词对 cipher 大小写不敏感，逐字恢复不构成新毒化面。
		if extra := strings.TrimSpace(i.Extra); extra != "" && extra != "null" {
			var p kernel.Proxy
			if err := json.Unmarshal([]byte(extra), &p); err != nil {
				return err
			}
			if err := kernel.ValidateImportedProxy(p); err != nil {
				return err
			}
		}
		return nil
	}
	var krow model.NodeKernel
	res := tx.Where("id = ?", i.KernelID).Limit(1).Find(&krow)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil // 存在性校验已在调用方跳过该行，此处 unreachable 兜底
	}
	return kernel.Normalize(krow.KernelType, &vin)
}

// inboundImportCounts carries the per-subsection landed-row counts of
// one inbounds-segment import (R13X-004): the ImportResult fields of
// the same names are filled from it, so the receipt can be checked
// subsection by subsection against the DB instead of only reporting
// the inbound-row total. Skipped rows are never counted here (they
// are reported through SkippedRows, as with inboundsImported).
type inboundImportCounts struct {
	kernels             int
	inbounds            int
	relays              int
	counters            int
	subscriptions       int
	subscriptionCounter int
	sources             int
	legacyUsers         int
}

// findReusableSeedDefaultTx 找可被一次备份导入复用的种子默认订阅
// （R31-L3-H02 / F-04，修法 (a)）：启动期 migrateSubscriptions 在
// 订阅数为 0 的库上建的「我的订阅」（随机 token）。全新恢复时备份
// 自带的默认订阅按 token 不匹配、会另起一行，导致默认订阅重复
// 两条且 DefaultSubscription() 指向从未分享过的种子行。
//
// 复用只认「种子形」行：名「我的订阅」+ SourceAll=1 + 零流量 + 无
// 任何引用（用户分配 user_subscription、计数基线
// subscription_counter、小时流量 subscription_traffic）。已投入
// 使用的默认订阅（有流量或有引用）不是种子，不复用——跨面板恢复
// 进已有面板时仍按既往口径另起一行，不夺取其链接。无候选行时
// 返回 0。调用方只复用一次（同一备份含多条默认订阅时其余仍另
// 起一行；备份自带重复是备份侧既有形状，不在本修复内合并）。
func findReusableSeedDefaultTx(tx *gorm.DB) (int64, error) {
	var seed model.Subscription
	res := tx.Where("name = ? AND source_all = ? AND upload_bytes = ? AND download_bytes = ?",
		"我的订阅", 1, 0, 0).Order("id ASC").Limit(1).Find(&seed)
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, nil
	}
	var n int64
	if err := tx.Model(&model.UserSubscription{}).Where("subscription_id = ?", seed.ID).Count(&n).Error; err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, nil
	}
	if err := tx.Model(&model.SubscriptionCounter{}).Where("subscription_id = ?", seed.ID).Count(&n).Error; err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, nil
	}
	if err := tx.Model(&model.SubscriptionTraffic{}).Where("subscription_id = ?", seed.ID).Count(&n).Error; err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, nil
	}
	return seed.ID, nil
}

func importInbounds(tx *gorm.DB, b *model.InboundsBackup, now int64, subRemap map[int64]int64, rep *importReport) (inboundImportCounts, error) {
	kernelRepointed := int64(0)
	invalidShareHosts := 0
	invalidShareModes := 0
	// R9X-008：WhitelistExtra sanitize 的回执计数（受影响行数 + 被
	// 剔除的非法条目数）与逐行身份/坏条目披露清单（汇总 warning 在
	// 入站循环后统一发，形态同 invalidInboundObjects 的点名披露；
	// 行照常落库，故不计 SkippedRows，同 R5X-014 sanitize 回执口径）。
	invalidWhitelistRows := 0
	invalidWhitelistEntries := 0
	var invalidWhitelistIdentities []string
	// R5X-016：入站段返回实落计数（被跳过行不计入，与 SkippedRows
	// 不重复计数）；R5X-024 的校验跳过同样不计。
	var counts inboundImportCounts
	invalidInboundObjects := 0
	// R6X-009：内容校验跳过的行在恢复结果中完全消失，计数-only 的
	// 汇总 warning 无从定位——收集被跳过行身份（#{id} {name}）与
	// 逐行错误摘要，随汇总 warning 一并披露（清单截断见循环后）。
	var invalidInboundIdentities []string
	for _, k := range b.Kernels {
		// D4-03：引用的节点不存在时跳过该内核并计数。
		kernelNodeExists, err := rowExistsTx(tx, &model.Node{}, k.NodeID)
		if err != nil {
			return inboundImportCounts{}, err
		}
		if !kernelNodeExists {
			rep.skip("kernels")
			continue
		}
		// R2X-046：同节点同类型的旧内核行被新行替换前，先把引用旧
		// 行的存量入站（多为备份之后新建、不在本备份内的行）重指向
		// 新行 id。旧实现裸删旧行，这批入站 kernel_id 悬空、列表
		// kernelType 解析为空；不采级联删（静默删掉备份外入站比
		// 悬空更糟）。重指向计数入 ImportWarnings（循环后统一发）。
		var replacedKernels []model.NodeKernel
		if err := tx.Where("node_id = ? AND kernel_type = ? AND id <> ?", k.NodeID, k.KernelType, k.ID).
			Find(&replacedKernels).Error; err != nil {
			return inboundImportCounts{}, err
		}
		for _, oldKernel := range replacedKernels {
			res := tx.Model(&model.NodeInbound{}).Where("kernel_id = ?", oldKernel.ID).Update("kernel_id", k.ID)
			if res.Error != nil {
				return inboundImportCounts{}, res.Error
			}
			kernelRepointed += res.RowsAffected
		}
		// One kernel per node and type: drop a different row in the way.
		if err := tx.Where("node_id = ? AND kernel_type = ? AND id <> ?", k.NodeID, k.KernelType, k.ID).
			Delete(&model.NodeKernel{}).Error; err != nil {
			return inboundImportCounts{}, err
		}
		version := k.Version
		if version == "" {
			version = "latest"
		}
		item := model.NodeKernel{
			ID: k.ID, NodeID: k.NodeID, KernelType: k.KernelType, Version: version,
			Status: "pending", Enabled: k.Enabled, CreatedTime: k.CreatedTime, UpdatedTime: now,
			InstalledVersion: k.InstalledVersion,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"node_id", "kernel_type", "version", "installed_version", "status", "last_error",
				"config_hash", "applied_hash", "enabled", "updated_time",
			}),
		}).Create(&item).Error; err != nil {
			return inboundImportCounts{}, err
		}
		if err := restoreZeroValuedInt(tx, &model.NodeKernel{}, k.ID, "enabled", k.Enabled); err != nil {
			return inboundImportCounts{}, err
		}
		counts.kernels++
	}
	if kernelRepointed > 0 {
		rep.warnf("备份恢复：内核行替换时已将 %d 条存量入站的内核引用重指向恢复后的内核行（旧内核行已被同节点同类型的新行替换）", kernelRepointed)
	}

	// Sources first: imported nodes point at them. A refresh fills in the
	// status fields.
	for _, src := range b.Sources {
		kind := src.Kind
		if kind != model.RemoteSourceFile {
			kind = model.RemoteSourceURL // backups from before file sources
		}
		// 复扫 R-06：恢复面与保存面同口径——空标签订阅源 URL 行跳过 +
		// warning（文件源无 URL，不判）。
		if kind == model.RemoteSourceURL && urlHostHasEmptyLabel(src.URL) {
			rep.skip("sources")
			rep.warnf("备份恢复：订阅源 #%d（%q）的地址 %q 含空标签主机名，已跳过该源未恢复", src.ID, src.Name, src.URL)
			continue
		}
		item := model.RemoteSource{
			ID: src.ID, Name: src.Name, URL: src.URL, IntervalHours: src.IntervalHours,
			RelayOnly: src.RelayOnly, Kind: kind, Role: src.Role, Enabled: src.Enabled, CreatedTime: src.CreatedTime, UpdatedTime: now,
			Userinfo: src.Userinfo,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"name", "url", "interval_hours", "relay_only", "kind", "role", "enabled", "updated_time", "userinfo"}),
		}).Create(&item).Error; err != nil {
			return inboundImportCounts{}, err
		}
		counts.sources++
	}

	for _, i := range b.Inbounds {
		// D4-03：引用的内核/节点不存在时跳过该入站并计数。注意
		// kernel_id=0 / node_id=0 是合法的「未绑定」语义（导入型
		// 入站，见 v1 迁移对 kernel_id>0 部分索引的口径），只校验
		// 非零引用。
		kernelExists := true
		if i.KernelID > 0 {
			var err error
			kernelExists, err = rowExistsTx(tx, &model.NodeKernel{}, i.KernelID)
			if err != nil {
				return inboundImportCounts{}, err
			}
		}
		inboundNodeExists := true
		if i.NodeID > 0 {
			var err error
			inboundNodeExists, err = rowExistsTx(tx, &model.Node{}, i.NodeID)
			if err != nil {
				return inboundImportCounts{}, err
			}
		}
		if !kernelExists || !inboundNodeExists {
			rep.skip("inbounds")
			continue
		}
		listen := i.Listen
		if listen == "" {
			listen = "::"
		}
		// 旧备份没有该字段；空值按 auto（与 Normalize 口径一致）。
		strategy := i.OutboundIPStrategy
		if strategy == "" {
			strategy = "auto"
		}
		// R5X-024：四整对象落库前跑 Normalize 等价校验（校验副本，
		// 见 validateImportedInboundObjects）——坏行整行跳过 + 计数，
		// 汇总 warning 在入站循环后统一发（同 ShareHost 披露口径）。
		if err := validateImportedInboundObjects(tx, i, listen, strategy); err != nil {
			rep.skip("inbounds")
			invalidInboundObjects++
			invalidInboundIdentities = append(invalidInboundIdentities,
				fmt.Sprintf("#%d %s（%v）", i.ID, i.Name, err))
			continue
		}
		shareHost, ok := sanitizeBackupShareHost(i.ShareHost)
		if !ok {
			invalidShareHosts++
		}
		shareMode, modeOK := sanitizeBackupShareAddressMode(i.ShareAddressMode)
		if !modeOK {
			invalidShareModes++
		}
		// R9X-008：WhitelistExtra 经保存面同口径校验（ParseAllowList）
		// 做字段级 sanitize——合法清单逐字恢复，非法条目逐项剔除、
		// 整表不合法时整表清空，计数与坏条目随汇总 warning 披露，
		// 入站行本身照常恢复（不整行跳过）。
		whitelistExtra, whitelistBad, whitelistErr := sanitizeBackupWhitelistExtra(i.WhitelistExtra)
		if whitelistErr != nil || len(whitelistBad) > 0 {
			invalidWhitelistRows++
			invalidWhitelistEntries += len(whitelistBad)
			detail := ""
			if len(whitelistBad) > 0 {
				quoted := make([]string, 0, len(whitelistBad))
				for _, b := range whitelistBad {
					quoted = append(quoted, fmt.Sprintf("%q", b))
				}
				detail = "非法条目：" + strings.Join(quoted, "、")
			}
			if whitelistErr != nil {
				if detail != "" {
					detail += "；"
				}
				detail += fmt.Sprintf("整表清空（%v）", whitelistErr)
			}
			invalidWhitelistIdentities = append(invalidWhitelistIdentities,
				fmt.Sprintf("#%d %s（%s）", i.ID, i.Name, detail))
		}
		// R13X-006：导入型入站（kernel_id<=0）的 Protocol 是派生字段，
		// 须以 Extra 承载的代理对象重推导（与创建路径 externalRow /
		// 编辑路径 row.Protocol = kernel.ProxyProtocol(proxy) 同口径），
		// 不再逐字信任备份的 Protocol 字段（如 trojan 标签配 ss
		// Extra）。Extra 本体已在上方 validateImportedInboundObjects
		// 经 ValidateImportedProxy 校验，此处只在解析成功且推导结果
		// 非空时覆写；Extra 为空或推导不出时保留原值。Extra 逐字
		// 落库不变，故订阅端点与链式目标等数据面（均经 importedProxy
		// 构造）行为不变；整行跳过备选不采。
		protocol := i.Protocol
		if i.KernelID <= 0 {
			if extra := strings.TrimSpace(i.Extra); extra != "" && extra != "null" {
				var p kernel.Proxy
				if err := json.Unmarshal([]byte(extra), &p); err == nil {
					if derived := kernel.ProxyProtocol(p); derived != "" {
						protocol = derived
					}
				}
			}
		}
		item := model.NodeInbound{
			ID: i.ID, KernelID: i.KernelID, NodeID: i.NodeID, Name: i.Name, Protocol: protocol,
			Listen: listen, Port: i.Port, Transport: i.Transport, Security: i.Security,
			Settings: i.Settings, Extra: i.Extra, ShareHost: shareHost, ShareAddressMode: shareMode, Remark: i.Remark,
			RelayOnly: i.RelayOnly, AcceptProxyProtocol: i.AcceptProxyProtocol,
			RelayWhitelist: i.RelayWhitelist, WhitelistExtra: whitelistExtra, Enabled: i.Enabled,
			UploadBytes: i.UploadBytes, DownloadBytes: i.DownloadBytes, Inx: i.Inx,
			CreatedTime: i.CreatedTime, UpdatedTime: now, SourceID: i.SourceID, OutboundID: i.OutboundID,
			SplitRules: i.SplitRules, OutboundIPStrategy: strategy, MeteringEnabled: i.MeteringEnabled,
			ConnIdleSeconds: i.ConnIdleSeconds,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"kernel_id", "node_id", "name", "protocol", "listen", "port", "transport", "security",
				"settings", "extra", "share_host", "share_address_mode", "remark", "relay_only", "accept_proxy_protocol",
				"relay_whitelist", "whitelist_extra", "enabled", "upload_bytes", "download_bytes",
				"inx", "updated_time", "source_id", "outbound_id", "split_rules", "outbound_ip_strategy",
				"metering_enabled", "companion_of", "conn_idle_seconds",
			}),
		}).Create(&item).Error; err != nil {
			return inboundImportCounts{}, err
		}
		if err := restoreZeroValuedInt(tx, &model.NodeInbound{}, i.ID, "enabled", i.Enabled); err != nil {
			return inboundImportCounts{}, err
		}
		// F6 (B-07): restored rows are always origins — backups never
		// carry companions (they are derived state). Without the
		// explicit write-back, a restored row whose id collides with
		// an existing companion row (cross-panel restore) would keep
		// the squatter's companion_of marker: hidden from lists and
		// exposed to another origin's reconcile lifecycle. The
		// item's CompanionOf is intentionally always 0; the zero-value
		// write-back mirrors the enabled handling above.
		if err := restoreZeroValuedInt(tx, &model.NodeInbound{}, i.ID, "companion_of", 0); err != nil {
			return inboundImportCounts{}, err
		}
		counts.inbounds++
	}
	if invalidInboundObjects > 0 {
		// R6X-009：汇总 warning 附被跳过行身份清单（前 10 条，超出
		// 以总数收尾），管理员可按名修正备份后重试。
		identities := invalidInboundIdentities
		suffix := ""
		if len(identities) > 10 {
			suffix = fmt.Sprintf(" 等共 %d 条", len(identities))
			identities = identities[:10]
		}
		rep.warnf("备份恢复：%d 条入站的 settings/transport/security/extra 未通过保存面同口径校验（Normalize），已整行跳过未恢复：%s%s，请修正备份内容后重试",
			invalidInboundObjects, strings.Join(identities, "、"), suffix)
	}
	if invalidWhitelistRows > 0 {
		// R9X-008：汇总披露受影响行数 + 被剔除非法条目数 + 逐行身份
		// 与坏条目原文（前 10 条，超出以总数收尾，同 invalidInboundObjects
		// 的截断口径）。行已照常恢复，此处计数只入 ImportWarnings、
		// 不计 SkippedRows。
		identities := invalidWhitelistIdentities
		suffix := ""
		if len(identities) > 10 {
			suffix = fmt.Sprintf(" 等共 %d 条", len(identities))
			identities = identities[:10]
		}
		rep.warnf("备份恢复：%d 条入站的白名单附加清单（WhitelistExtra）未通过保存面同口径校验（ParseAllowList），已逐项剔除 %d 个非法条目后恢复（合法条目逐字保留，整表不合法时整表清空）：%s%s，请核对被剔除条目后在入站设置中重新填写",
			invalidWhitelistRows, invalidWhitelistEntries, strings.Join(identities, "、"), suffix)
	}

	// R2X-015：入站附属引用后置校验。出站目标可能是同批入站列表的
	// 后序行，逐行校验会误杀合法链式入站——循环全部落库后统一校验：
	// 有效集 = 本轮落库的备份行 ∪ 目标库现存行（即校验时点的 DB
	// 全集）。source_id 无效置 0（手工导入语义）、outbound_id 无效
	// 置 0（直连）、splitRules 剔除指向无效 outbound 的规则后回写，
	// 均计 ImportWarnings，不跳整行（verdicts §2.7）。
	if err := validateImportedInboundRefsTx(tx, b.Inbounds, rep); err != nil {
		return inboundImportCounts{}, err
	}

	relayRepointed := int64(0)
	for _, rel := range b.Relays {
		// D4-03：引用的入站/转发不存在时跳过该中转并计数。
		relayInboundExists, err := rowExistsTx(tx, &model.NodeInbound{}, rel.InboundID)
		if err != nil {
			return inboundImportCounts{}, err
		}
		relayForwardExists, err := rowExistsTx(tx, &model.Forward{}, rel.ForwardID)
		if err != nil {
			return inboundImportCounts{}, err
		}
		if !relayInboundExists || !relayForwardExists {
			rep.skip("relays")
			continue
		}
		// R3X-023：同 forward 的异 id 旧中转行被继任行替换前，先把
		// 引用旧行的存量 user_inbound（备份外分配）重指向继任行
		// （与 R2X-046 内核重指向同型）；裸删会让分配行 relay_id
		// 悬空、订阅端点静默消失。计数入 ImportWarnings（循环后
		// 统一发）。
		var replacedRelays []model.NodeInboundRelay
		if err := tx.Where("forward_id = ? AND id <> ?", rel.ForwardID, rel.ID).
			Find(&replacedRelays).Error; err != nil {
			return inboundImportCounts{}, err
		}
		for _, oldRelay := range replacedRelays {
			res := tx.Model(&model.UserInbound{}).Where("relay_id = ?", oldRelay.ID).Update("relay_id", rel.ID)
			if res.Error != nil {
				return inboundImportCounts{}, res.Error
			}
			relayRepointed += res.RowsAffected
		}
		if err := tx.Where("forward_id = ? AND id <> ?", rel.ForwardID, rel.ID).
			Delete(&model.NodeInboundRelay{}).Error; err != nil {
			return inboundImportCounts{}, err
		}
		relayShareHost, ok := sanitizeBackupShareHost(rel.ShareHost)
		if !ok {
			invalidShareHosts++
		}
		item := model.NodeInboundRelay{
			ID: rel.ID, InboundID: rel.InboundID, ForwardID: rel.ForwardID, Name: rel.Name,
			ShareHost: relayShareHost, Managed: rel.Managed, CreatedTime: rel.CreatedTime,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"inbound_id", "forward_id", "name", "share_host", "managed"}),
		}).Create(&item).Error; err != nil {
			return inboundImportCounts{}, err
		}
		if err := restoreZeroValuedInt(tx, &model.NodeInboundRelay{}, rel.ID, "managed", rel.Managed); err != nil {
			return inboundImportCounts{}, err
		}
		counts.relays++
	}
	if relayRepointed > 0 {
		rep.warnf("备份恢复：中转行替换时已将 %d 条存量用户入站分配的中转引用重指向恢复后的中转行（旧中转行已被同转发的新行替换）", relayRepointed)
	}

	if invalidShareHosts > 0 {
		rep.warnf("备份恢复：%d 条分享地址（入站/中转）不符合格式要求，已清空，恢复后按节点地址下发，可在入站设置中重新填写", invalidShareHosts)
	}
	if invalidShareModes > 0 {
		rep.warnf("备份恢复：%d 条入站的分享地址模式不合法，已按自动（auto）恢复", invalidShareModes)
	}

	// Counter baselines keep the next traffic sample from being counted
	// twice when the restored panel talks to the same nodes.
	for _, c := range b.Counters {
		// D4-03：引用的入站不存在时跳过该基线并计数。
		counterInboundExists, err := rowExistsTx(tx, &model.NodeInbound{}, c.InboundID)
		if err != nil {
			return inboundImportCounts{}, err
		}
		if !counterInboundExists {
			rep.skip("inboundCounters")
			continue
		}
		item := model.NodeInboundCounter{
			InboundID: c.InboundID, Protocol: c.Protocol, Direction: c.Direction,
			Epoch: c.Epoch, Bytes: c.Bytes, UpdatedTime: now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "inbound_id"}, {Name: "protocol"}, {Name: "direction"}},
			DoUpdates: clause.AssignmentColumns([]string{"epoch", "bytes", "updated_time"}),
		}).Create(&item).Error; err != nil {
			return inboundImportCounts{}, err
		}
		counts.counters++
	}
	// Subscriptions keep their links and credentials, matched by token.
	// AppliedActive starts at 0 so the expiry loop pushes the active share
	// subscriptions to the kernels.
	//
	// R31-L3-H02 (F-04, 修法 (a))：导入默认订阅时复用种子行，见
	// findReusableSeedDefaultTx。种子 id 在循环前一次取定；同一备
	// 份内只复用一次，其余默认订阅仍按 token 口径另起一行。
	seedDefaultID, err := findReusableSeedDefaultTx(tx)
	if err != nil {
		return inboundImportCounts{}, err
	}
	seedReused := false
	subs := append([]model.SubscriptionBackup(nil), b.Subscriptions...)
	for _, u := range b.Users {
		subs = append(subs, model.SubscriptionBackup{
			Name: u.Name, Kind: model.SubscriptionShare, Token: u.Token, UUID: u.UUID, Password: u.Password,
			InboundIDs: u.InboundIDs, ExpireTime: u.ExpireTime, Enabled: u.Enabled, Remark: u.Remark,
			CreatedTime: u.CreatedTime,
		})
	}
	for subIdx, sb := range subs {
		if sb.Token == "" {
			// R1 H-O2（随 X-P2-7 同批，V-23）：空 token 订阅跳过
			// 必须计入回执 SkippedRows，不得静默丢弃。
			rep.skip("subscriptions")
			continue
		}
		var existing model.Subscription
		res := tx.Where("token = ?", sb.Token).Limit(1).Find(&existing)
		if res.Error != nil {
			return inboundImportCounts{}, res.Error
		}
		item := model.Subscription{
			Name: sb.Name, Kind: sb.Kind, Token: sb.Token, UUID: sb.UUID, Password: sb.Password,
			SourceAll: sb.SourceAll, InboundIDs: sb.InboundIDs, SourceIDs: sb.SourceIDs, Options: sb.Options, ExpireTime: sb.ExpireTime,
			Enabled: sb.Enabled, Remark: sb.Remark, Inx: sb.Inx, CreatedTime: sb.CreatedTime, UpdatedTime: now,
			UploadBytes: sb.UploadBytes, DownloadBytes: sb.DownloadBytes, TrafficLimit: sb.TrafficLimit,
			ResetDay: sb.ResetDay, TrafficResetTime: sb.TrafficResetTime, TrafficSince: sb.TrafficSince, PendingDelete: sb.PendingDelete,
		}
		// Every subscription has its own credentials (older backups may
		// carry owner-credential ones).
		item.Kind = model.SubscriptionShare
		if item.UUID == "" || item.Password == "" {
			uuid, pw, credErr := subscriptionCredentials()
			if credErr != nil {
				return inboundImportCounts{}, credErr
			}
			item.UUID, item.Password = uuid, pw
		}
		if res.RowsAffected > 0 {
			item.ID = existing.ID
		} else if sb.SourceAll == 1 && seedDefaultID > 0 && !seedReused {
			// R31-L3-H02 (F-04)：无 token 命中、且备份行是默认订阅
			// 时，复用种子默认订阅行：就地以备份字段覆写（token 以
			// 备份为准），不另起一行。种子行 id 保留，引用它的分配
			// 行不受影响；subRemap 仍登记备份 id -> 复用后 id，订
			// 阅 id 不保真仍为 BUG-A2-4 设计。
			item.ID = seedDefaultID
			seedReused = true
		}
		if err := tx.Save(&item).Error; err != nil {
			return inboundImportCounts{}, err
		}
		if subIdx < len(b.Subscriptions) {
			counts.subscriptions++
		} else {
			counts.legacyUsers++
		}
		// BUG-A2-4：登记备份 id -> 实际落库 id（按 token 命中旧行时
		// 两者可不同；用户派生订阅 sb.ID=0 不参与分配引用，跳过）。
		if sb.ID > 0 && subRemap != nil {
			subRemap[sb.ID] = item.ID
		}
	}

	// A3-04：订阅计数基线随恢复落位。订阅 id 先经 subRemap 按 token
	// 重映射（与分配同口径）；节点/订阅任一不存在则跳过并计数
	//（D4-03）。基线与累计同恢复后，首个样本按差分计，不再整段
	// 重计；旧备份无此段时消费者侧「缺失只建基线」兜底。
	for _, c := range b.SubscriptionCounters {
		subID := c.SubscriptionID
		if mapped, ok := subRemap[subID]; ok {
			subID = mapped
		}
		counterNodeExists, err := rowExistsTx(tx, &model.Node{}, c.NodeID)
		if err != nil {
			return inboundImportCounts{}, err
		}
		counterSubExists, err := rowExistsTx(tx, &model.Subscription{}, subID)
		if err != nil {
			return inboundImportCounts{}, err
		}
		if !counterNodeExists || !counterSubExists {
			rep.skip("subscriptionCounters")
			continue
		}
		item := model.SubscriptionCounter{
			NodeID: c.NodeID, SubscriptionID: subID, Direction: c.Direction,
			Epoch: c.Epoch, Bytes: c.Bytes, UpdatedTime: now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "node_id"}, {Name: "subscription_id"}, {Name: "direction"}},
			DoUpdates: clause.AssignmentColumns([]string{"epoch", "bytes", "updated_time"}),
		}).Create(&item).Error; err != nil {
			return inboundImportCounts{}, err
		}
		counts.subscriptionCounter++
	}
	return counts, nil
}

// inboundChainTargetUsableTx reports whether id names a row that may
// serve as a chained-exit / split target for a restored inbound: the
// row must exist and must not be a metering companion. The save
// surface refuses companions as targets outright (validateChain —
// chain assembly dials the target row itself, and a companion's bare
// identity PSK is rejected in multi-user mode; a later companion
// recycle would also leave the reference dangling and fail the whole
// kernel payload build), so for restored references a companion
// counts as missing (X-02).
func inboundChainTargetUsableTx(tx *gorm.DB, id int64) (bool, error) {
	if id <= 0 {
		return false, nil
	}
	var n int64
	if err := tx.Model(&model.NodeInbound{}).Where("id = ? AND companion_of = ?", id, 0).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// meteringShapeBlockedBackup mirrors the save surface's metering
// shape gate (handler ssMeteringShapeBlock) over the four restored
// fields: an origin serving through the PROXY protocol, relay-only,
// a chained exit or split rules can never be metered — a companion
// does not inherit that routing, so materializing one would silently
// reroute (or never happen). The repo layer cannot import the
// handler's predicate, so the condition is restated here over the
// model row; the two must be kept in agreement, with the save
// surface remaining the authority for the human-readable reasons.
func meteringShapeBlockedBackup(acceptProxyProtocol, relayOnly int, outboundID int64, splitRules string) bool {
	return acceptProxyProtocol == 1 || relayOnly == 1 || outboundID != 0 ||
		strings.TrimSpace(splitRules) != ""
}

// validateImportedInboundRefsTx 是 R2X-015 的入站后置校验：只处理
// 本轮备份清单内的入站行（被 kernel/node 身份校验跳过的行在库中不
// 存在，自动略过；备份外存量行不动）。剥离计数合并为一条 warning。
func validateImportedInboundRefsTx(tx *gorm.DB, rows []model.InboundBackup, rep *importReport) error {
	srcStripped, outStripped, rulesStripped := 0, 0, 0
	meteringStripped := 0
	// R14-A5-08：恢复出的 Snell 毒化行（带链式出站/分流）剥离计数。
	snellStripped := 0
	for _, ib := range rows {
		var row model.NodeInbound
		res := tx.Where("id = ?", ib.ID).Limit(1).Find(&row)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			continue
		}
		updates := map[string]interface{}{}
		if row.SourceID > 0 {
			exists, err := rowExistsTx(tx, &model.RemoteSource{}, row.SourceID)
			if err != nil {
				return err
			}
			if !exists {
				updates["source_id"] = 0
				srcStripped++
			}
		}
		// Effective routing shape after this pass: references stripped
		// above no longer shape the row (a dangling chained exit
		// pruned to direct must not condemn the metering switch).
		effOutboundID := row.OutboundID
		if row.OutboundID > 0 {
			usable, err := inboundChainTargetUsableTx(tx, row.OutboundID)
			if err != nil {
				return err
			}
			if !usable {
				updates["outbound_id"] = 0
				outStripped++
				effOutboundID = 0
			}
		}
		effSplitRules := row.SplitRules
		if strings.TrimSpace(row.SplitRules) != "" {
			pruned, removed, parsed, err := pruneDanglingSplitRulesTx(tx, row.SplitRules)
			if err != nil {
				return err
			}
			// 解析失败的 splitRules 原样保留，不因本校验引入新失败面。
			if parsed && removed > 0 {
				updates["split_rules"] = pruned
				rulesStripped += removed
				effSplitRules = pruned
			}
		}
		// R14-A5-08：Snell 内核的行带链式出站/分流是毒化组合（渲染期整
		// 内核拒绝；保存期已拒，见 handler.validateChainVersion）——
		// 备份里带出来的旧毒化行在恢复时即剥离为直连（与上方悬空引用
		// 剥离同口径：行照常恢复、只清路由列、计数披露），不得原样落
		// 库后阻塞整内核后续下发。以前置悬空剥离后的有效值判定（目标
		// 已悬空的行已在上方清为直连，不重复计数）。
		if row.KernelID > 0 && (effOutboundID != 0 || strings.TrimSpace(effSplitRules) != "") {
			var krow model.NodeKernel
			if kerr := tx.Where("id = ?", row.KernelID).Limit(1).Find(&krow).Error; kerr == nil && krow.KernelType == kernel.TypeSnell {
				if effOutboundID != 0 {
					updates["outbound_id"] = 0
					effOutboundID = 0
				}
				if strings.TrimSpace(effSplitRules) != "" {
					updates["split_rules"] = ""
					effSplitRules = ""
				}
				snellStripped++
			}
		}
		// X-01 (G1)：恢复出的「计量开 + 形态被排除」行在保存面是被
		// 拒绝/自愈关开关的组合（B-01 的静默不计量形态：开关显示
		// 开、伴生永不物化、无任何提示）。导入面按最终落库形态判
		// 定，同口径关开关并披露，而不是把静默失效原样恢复回来。
		if row.MeteringEnabled == 1 &&
			meteringShapeBlockedBackup(row.AcceptProxyProtocol, row.RelayOnly, effOutboundID, effSplitRules) {
			updates["metering_enabled"] = 0
			meteringStripped++
		}
		if len(updates) > 0 {
			if err := tx.Model(&model.NodeInbound{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
	}
	if total := srcStripped + outStripped + rulesStripped; total > 0 {
		rep.warnf("备份恢复：入站附属引用后置校验共剥离悬空引用 %d 处（来源 source_id %d 处已置 0、出站 outbound_id %d 处已置 0 改直连、分流规则 %d 条已剔除），入站本身已照常恢复", total, srcStripped, outStripped, rulesStripped)
	}
	if meteringStripped > 0 {
		rep.warnf("备份恢复：%d 条入站开启了订阅计量但形态不兼容（PROXY 协议/仅中转/链式出口/分流，计量伴生无法服务），已关闭其计量开关，入站本身已照常恢复，改为直连形态后可重新开启计量", meteringStripped)
	}
	if snellStripped > 0 {
		rep.warnf("备份恢复：%d 条 Snell 内核入站带有链式出站/分流（Snell 内核不支持，已清除其出站与分流配置改直连），入站本身已照常恢复", snellStripped)
	}
	return nil
}

// pruneDanglingSplitRulesTx 剔除 splitRules 中 outboundId 指向不可用
// 目标（不存在、或计量伴生行——同链式出口的 X-02 口径）的规则。按 json.RawMessage 逐条保留原始字节，未被剔除的规则
// 逐字不变（不做结构体重序列化，避免字段漂移）。整体解析失败时
// parsed=false，调用方原样保留原文。
func pruneDanglingSplitRulesTx(tx *gorm.DB, raw string) (pruned string, removed int, parsed bool, err error) {
	var rawRules []json.RawMessage
	if uerr := json.Unmarshal([]byte(raw), &rawRules); uerr != nil {
		return "", 0, false, nil
	}
	kept := make([]json.RawMessage, 0, len(rawRules))
	for _, rr := range rawRules {
		var probe struct {
			OutboundID int64 `json:"outboundId"`
		}
		if uerr := json.Unmarshal(rr, &probe); uerr != nil {
			kept = append(kept, rr)
			continue
		}
		if probe.OutboundID > 0 {
			usable, cerr := inboundChainTargetUsableTx(tx, probe.OutboundID)
			if cerr != nil {
				return "", 0, false, cerr
			}
			if !usable {
				removed++
				continue
			}
		}
		kept = append(kept, rr)
	}
	if removed == 0 {
		return raw, 0, true, nil
	}
	out, merr := json.Marshal(kept)
	if merr != nil {
		return "", 0, false, merr
	}
	return string(out), removed, true, nil
}
