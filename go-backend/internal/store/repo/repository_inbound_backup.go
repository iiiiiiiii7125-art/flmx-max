package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"reflect"
	"sort"
	"strconv"
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

// sanitizeBackupSplitRules applies the save surface's split-rule domain
// standard (kernel.NormalizeSplitRules) to an inbound's SplitRules
// coming back from a backup (X-05): the field is a JSON string column,
// so its `domains` subfield escaped every earlier enumeration round —
// the restore face stored it verbatim while the write face rejects
// `a..b`, and both kernels render it verbatim (`domain:a..b` /
// `domain_suffix`) where it can never match, so the rule silently
// never fires (same harm class as R-08 AllowedDomains).
//
// Disposition is per-entry pruning, R-08 style: bad domains are
// dropped and reported, legal entries restore verbatim (never
// rewritten into normalized form, per the validateImportedInboundObjects
// 口径). A rule this pass itself emptied (its only selectors were bad
// domains) is dropped whole — the write face rejects such a rule, and
// both kernels emit zero routing fragments for it, so keeping it
// would only preserve a dead rule (same whole-rule drop as
// pruneDanglingSplitRulesTx). Rules that arrived selector-less are
// left alone (R2X-015 keeps them verbatim). When nothing is dropped the input
// restores verbatim; when no rule survives the field restores as ""
// (the write face's canonical empty, setInboundRouting). An
// unparseable SplitRules blob restores verbatim (parsed=false) —
// this pass must not invent a new failure surface, same as the
// dangling-split pruning.
func sanitizeBackupSplitRules(raw string) (restored string, droppedDomains []string, droppedRules int, parsed bool) {
	if strings.TrimSpace(raw) == "" {
		return raw, nil, 0, true
	}
	var rawRules []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &rawRules); err != nil {
		return raw, nil, 0, false
	}
	kept := make([]json.RawMessage, 0, len(rawRules))
	for _, rr := range rawRules {
		var rule kernel.SplitRule
		if err := json.Unmarshal(rr, &rule); err != nil {
			kept = append(kept, rr)
			continue
		}
		changed := false
		droppedHere := 0
		domains := make([]string, 0, len(rule.Domains))
		for _, d := range rule.Domains {
			if kernel.HasEmptyLabel(d) {
				droppedDomains = append(droppedDomains, d)
				changed = true
				droppedHere++
				continue
			}
			domains = append(domains, d)
		}
		rule.Domains = domains
		// Only drop a rule this pass itself emptied: a rule that
		// arrived with no selectors at all (e.g. {"action":"direct"}
		// or a bare proxy rule) is pre-existing stored shape — the
		// R2X-015 dangling-ref pruning keeps such rules verbatim,
		// and this pass must not re-decide their fate. A rule left
		// selector-less only because its bad domains were just
		// pruned can never match (both kernels emit zero routing
		// fragments for it), so dropping it loses nothing.
		if droppedHere > 0 && len(rule.Categories)+len(rule.Domains)+len(rule.IPs) == 0 {
			droppedRules++
			continue
		}
		if !changed {
			kept = append(kept, rr)
			continue
		}
		// Stored SplitRules is always struct-produced (NormalizeSplitRules
		// output), so re-marshalling the struct cannot drift fields.
		encoded, err := json.Marshal(rule)
		if err != nil {
			kept = append(kept, rr)
			continue
		}
		kept = append(kept, encoded)
	}
	if len(droppedDomains) == 0 && droppedRules == 0 {
		return raw, nil, 0, true
	}
	if len(kept) == 0 {
		return "", droppedDomains, droppedRules, true
	}
	out, err := json.Marshal(kept)
	if err != nil {
		return raw, nil, 0, false
	}
	return string(out), droppedDomains, droppedRules, true
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
	// F6-02：删除屏蔽登记随源一同导出——否则全量恢复后管理员手工
	// 删过的导入节点会被下一轮源同步复活，且回执无从知晓。
	var shields []model.RemoteSourceShield
	if err := r.db.Order("source_id ASC, id ASC").Find(&shields).Error; err != nil {
		return nil, err
	}
	for _, sh := range shields {
		out.Shields = append(out.Shields, model.RemoteSourceShieldBackup{
			SourceID: sh.SourceID, IdentityKey: sh.IdentityKey, Name: sh.Name, CreatedTime: sh.CreatedTime,
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
	var templates []model.SubscriptionTemplate
	if err := r.db.Order("id ASC").Find(&templates).Error; err != nil {
		return nil, err
	}
	for _, tp := range templates {
		out.Templates = append(out.Templates, model.SubscriptionTemplateBackup{
			ID: tp.ID, Name: tp.Name, Kind: tp.Kind, Content: tp.Content,
			BuilderConfig: tp.BuilderConfig, SourceURL: tp.SourceURL,
			LastFetch: tp.LastFetch, LastError: tp.LastError, CreatedTime: tp.CreatedTime,
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
// kernel.Normalize。kernel_id>0 的自建入站落库形态按对象裁定
// （RR4-F2-01 与 R5X-024/R11X-001 护栏的收口形态）：Normalize 对
// 凭据类字段是生成而非拒绝（ENC secret、UUID、Reality 钥、密码
// 缺失即补，auth/flow 仅 TrimSpace），只取
// error 做校验时这类靠愈合才合法的行会以未愈合形态落库（ENC 空
// secret 行下发复检无感、订阅派生直接报错）——被愈合改变的对象
// 改落愈合后的序列化形态（验什么存什么）；Normalize 未触碰的对
// 象逐字恢复备份原文（R5X-024 防过修护栏，含键序等字节形态），
// Transport 仅 XHTTP mode 大小写差亦逐字（渲染侧自带归一，
// R11X-001 护栏）。kernel_id=0 的导入型入站没有内核上下文、不经
// Normalize，但其凭据与地址全在 Extra 承载的代理对象里，须经
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
// 不豁免地址/端口门。例外之例外（R6-H-01）：导入型行 Listen 为 ::（含
// 原始空值经调用方归一）且 Extra 校验必跑时，行载体拨号校验跳过——
// 此时行载体不携带拨号地址，地址由 Extra 载体校验覆盖；其余情形恒检
// 不变（非空 Listen 的 facet A 校验、Extra 缺席行的行载体门均不受影响）。
// R6-H-01-R1：上述跳过仅豁免拨号主机门，行 Port 1–65535 门恒保留。导入型入站（kernel_id≤0）的行内 Listen/Port
// 是导入地址的行内第二载体（externalRow 恒以经 finishProxy 校验的
// Host/Port 双写行字段与 Extra），经 kernel.ValidateImportedProxyRowAddress
// 走与 finishProxy 共用的同一地址实现 validateImportedProxyAddress
// （禁另写第二份谓词，沿 R7X-003 口径；R7X-003 的关闭对象是 Extra
// 载体，本恒检不顶替其断言）；自建入站（kernel_id>0）以 Normalize
// 的行字段两检口径恒校验（Port 1–65535 + Listen 为 IP 字面量，
// listen 已由调用方完成空→"::" 归一）。不要求行内 Listen/Port 与
// Extra 的 server/port 相等（只设合法性门，防过修）。
// marshalRestoredInboundObject serializes one object of the validated
// copy back into stored form (RR4-F2-01). An originally content-less
// object whose healed form is still the empty object keeps its
// original spelling, so "" is not gratuitously rewritten to "{}".
func marshalRestoredInboundObject(obj interface{}, orig string) (string, error) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(orig) == "" && string(raw) == "{}" {
		return orig, nil
	}
	return string(raw), nil
}

func validateImportedInboundObjects(tx *gorm.DB, i model.InboundBackup, listen, strategy string) (settings, transport, security string, err error) {
	// RR4-F2-01：返回值是落库形态。自建入站（kernel_id>0）在
	// Normalize 通过后改为愈合形态（见函数头注）；其余路径（内容
	// 豁免、导入型、内核缺失兜底）恒为备份原文。
	settings, transport, security = i.Settings, i.Transport, i.Security
	// hasContent is computed up front only so the R6-H-01 carve-out below
	// can tell whether the Extra-carrier check is guaranteed to run; the
	// row-carrier gate itself is NOT content-gated (R8X-004) and still
	// runs for content-less rows except for that carve-out.
	hasContent := strings.TrimSpace(i.Settings) != "" || strings.TrimSpace(i.Transport) != "" ||
		strings.TrimSpace(i.Security) != ""
	if !hasContent {
		if extra := strings.TrimSpace(i.Extra); extra != "" && extra != "null" && extra != "{}" {
			hasContent = true
		}
	}
	if i.KernelID <= 0 {
		// R6-H-01: the caller normalizes an empty row Listen to "::"
		// (the canonical unspecified form of this codebase: DB default
		// and Normalize's "" -> "::"), so at this point an originally
		// empty Listen and a literal "::" are indistinguishable -- and
		// neither carries a dial address. The dial address of an
		// imported row lives in Extra and is validated below by
		// ValidateImportedProxy. Skip the row-carrier dial check for
		// "::" ONLY when that Extra check is guaranteed to run (Extra
		// present and the row has content, so the function reaches it);
		// legacy Extra-less rows keep the row gate as their only
		// address check, and any other Listen value (including
		// "0.0.0.0", which no code path here produces as an unspecified
		// form) stays under the R8X-004 facet A dial check.
		// R6-H-01-R1: the carve-out skips only the dial host check,
		// never the row Port gate (see below).
		extra := strings.TrimSpace(i.Extra)
		extraValidated := extra != "" && extra != "null" && hasContent
		if listen == "::" && extraValidated {
			// R6-H-01-R1: the carve-out skips only the row-carrier
			// dial host check. ValidateImportedProxyRowAddress
			// bundles that host check with the row Port 1-65535
			// gate, so skipping the whole call would exempt the
			// port too; retain the port gate here, mirroring the
			// self-built branch below.
			if i.Port < 1 || i.Port > 65535 {
				return settings, transport, security, errors.New("端口必须在 1-65535 之间")
			}
		} else if err := kernel.ValidateImportedProxyRowAddress(listen, i.Port); err != nil {
			return settings, transport, security, err
		}
	} else {
		if i.Port < 1 || i.Port > 65535 {
			return settings, transport, security, errors.New("端口必须在 1-65535 之间")
		}
		if net.ParseIP(listen) == nil {
			return settings, transport, security, errors.New("监听地址必须是 IP")
		}
	}
	if !hasContent {
		return settings, transport, security, nil
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
			return settings, transport, security, err
		}
	}
	if strings.TrimSpace(i.Extra) != "" {
		var probe interface{}
		if err := json.Unmarshal([]byte(i.Extra), &probe); err != nil {
			return settings, transport, security, err
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
				return settings, transport, security, err
			}
			if err := kernel.ValidateImportedProxy(p); err != nil {
				return settings, transport, security, err
			}
		}
		return settings, transport, security, nil
	}
	var krow model.NodeKernel
	res := tx.Where("id = ?", i.KernelID).Limit(1).Find(&krow)
	if res.Error != nil {
		return settings, transport, security, res.Error
	}
	if res.RowsAffected == 0 {
		return settings, transport, security, nil // 存在性校验已在调用方跳过该行，此处 unreachable 兜底
	}
	// Snapshot the parsed objects so the healed copy can be diffed
	// against them object by object below (Security's slices are
	// cloned; Normalize only ever reassigns them, but the clone keeps
	// the diff honest regardless).
	beforeSettings, beforeTransport, beforeSecurity := vin.Settings, vin.Transport, vin.Security
	beforeSecurity.ALPN = append([]string(nil), vin.Security.ALPN...)
	beforeSecurity.RealityShortIDs = append([]string(nil), vin.Security.RealityShortIDs...)
	if err := kernel.Normalize(krow.KernelType, &vin); err != nil {
		return settings, transport, security, err
	}
	// RR4-F2-01：验什么存什么——被愈合改变的对象改落其序列化形态
	// （生成型凭据、TrimSpace 等默认值均体现在差分里）；
	// 未被触碰的对象与仅 XHTTP mode 大小写差的 Transport 逐字恢复
	// （R5X-024 / R11X-001 护栏，见函数头注）。
	if !reflect.DeepEqual(beforeSettings, vin.Settings) {
		if settings, err = marshalRestoredInboundObject(vin.Settings, i.Settings); err != nil {
			return i.Settings, i.Transport, i.Security, err
		}
	}
	if transportHealedBeyondModeCase(beforeTransport, vin.Transport) {
		if transport, err = marshalRestoredInboundObject(vin.Transport, i.Transport); err != nil {
			return i.Settings, i.Transport, i.Security, err
		}
	}
	if !reflect.DeepEqual(beforeSecurity, vin.Security) {
		if security, err = marshalRestoredInboundObject(vin.Security, i.Security); err != nil {
			return i.Settings, i.Transport, i.Security, err
		}
	}
	return settings, transport, security, nil
}

// transportHealedBeyondModeCase reports whether Normalize changed a
// Transport in any way beyond the XHTTP mode's letter case. The mode
// case is canonicalized by the render face itself
// (canonicalXHTTPMode), so a case-only difference does not make the
// row depend on healing and the backup spelling restores verbatim
// (R11X-001 guardrail).
func transportHealedBeyondModeCase(before, after kernel.Transport) bool {
	if reflect.DeepEqual(before, after) {
		return false
	}
	if before.Type == kernel.TransportXHTTP && after.Type == kernel.TransportXHTTP &&
		after.Mode == strings.ToLower(before.Mode) {
		before.Mode = after.Mode
		return !reflect.DeepEqual(before, after)
	}
	return true
}

// inboundImportCounts carries the per-subsection landed-row counts of
// one inbounds-segment import (R13X-004): the ImportResult fields of
// the same names are filled from it, so the receipt can be checked
// subsection by subsection against the DB instead of only reporting
// the inbound-row total. Skipped rows are never counted here (they
// are reported through SkippedRows, as with inboundsImported).
// Sole exception: groupsIgnored counts the removed nodeGroups
// segment's dropped rows (R3-S2-01) — that segment never lands by
// design, and the receipt reports how many entries were ignored.
type inboundImportCounts struct {
	kernels             int
	inbounds            int
	relays              int
	counters            int
	subscriptions       int
	subscriptionCounter int
	sources             int
	shields             int
	groupsIgnored       int
	templates           int
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

// parseBackupRefIDList parses a comma-separated subscription reference
// list (inbound_ids / source_ids) with the save surface's parseIDList
// acceptance: positive ids only, deduplicated, first-seen order; junk
// tokens are ignored.
func parseBackupRefIDList(s string) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, part := range strings.Split(s, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err == nil && id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// formatBackupRefIDList renders ids in the save surface's stored form
// (ascending, comma-joined — formatIDList's shape).
func formatBackupRefIDList(ids []int64) string {
	sorted := append([]int64(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	parts := make([]string, len(sorted))
	for i, id := range sorted {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

// filterBackupRefIDList reconciles one subscription reference list
// against the ids that actually exist at restore time (RR4-F1-01 /
// RR4-F4-01): dangling ids are dropped and returned for the receipt;
// a list with nothing dangling restores verbatim.
func filterBackupRefIDList(raw string, valid map[int64]bool) (kept string, dropped []int64) {
	ids := parseBackupRefIDList(raw)
	if len(ids) == 0 {
		return raw, nil
	}
	surviving := make([]int64, 0, len(ids))
	for _, id := range ids {
		if valid[id] {
			surviving = append(surviving, id)
		} else {
			dropped = append(dropped, id)
		}
	}
	if len(dropped) == 0 {
		return raw, nil
	}
	return formatBackupRefIDList(surviving), dropped
}

// backupOptionsChainSourceIDs collects every source id a subscription
// Options blob references through its chain slots (landing/relay lists
// and the per-source filter keys).
func backupOptionsChainSourceIDs(raw string) []int64 {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return nil
	}
	var out []int64
	for _, key := range []string{"chainLandingSources", "chainRelaySources"} {
		rawList, ok := top[key]
		if !ok {
			continue
		}
		var ids []int64
		if err := json.Unmarshal(rawList, &ids); err == nil {
			out = append(out, ids...)
		}
	}
	if rawFilters, ok := top["chainSourceFilters"]; ok {
		var filters map[int64]string
		if err := json.Unmarshal(rawFilters, &filters); err == nil {
			for id := range filters {
				out = append(out, id)
			}
		}
	}
	return out
}

// existingIDSetTx batch-checks which candidate ids exist in one table
// inside the import transaction.
func existingIDSetTx(tx *gorm.DB, modelPtr interface{}, candidates map[int64]bool) (map[int64]bool, error) {
	out := make(map[int64]bool, len(candidates))
	if len(candidates) == 0 {
		return out, nil
	}
	ids := make([]int64, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	for _, chunk := range chunkInt64IDs(ids) {
		var found []int64
		if err := tx.Model(modelPtr).Where("id IN ?", chunk).Pluck("id", &found).Error; err != nil {
			return nil, err
		}
		for _, id := range found {
			out[id] = true
		}
	}
	return out, nil
}

// subscriptionRefUniverseTx resolves the source/inbound existence sets
// subscription references are reconciled against (RR4-F1-01 /
// RR4-F4-01): judged on the transaction's state at subscription
// landing time, so rows restored earlier in this same import and
// pre-existing target rows both count as existing (the save surface's
// existence semantics), while rows skipped by this import and ids
// that never existed do not.
func subscriptionRefUniverseTx(tx *gorm.DB, subs []model.SubscriptionBackup) (sources, inbounds map[int64]bool, err error) {
	srcCandidates := map[int64]bool{}
	inbCandidates := map[int64]bool{}
	for _, sb := range subs {
		for _, id := range parseBackupRefIDList(sb.SourceIDs) {
			srcCandidates[id] = true
		}
		for _, id := range parseBackupRefIDList(sb.InboundIDs) {
			inbCandidates[id] = true
		}
		for _, id := range backupOptionsChainSourceIDs(sb.Options) {
			srcCandidates[id] = true
		}
	}
	if sources, err = existingIDSetTx(tx, &model.RemoteSource{}, srcCandidates); err != nil {
		return nil, nil, err
	}
	if inbounds, err = existingIDSetTx(tx, &model.NodeInbound{}, inbCandidates); err != nil {
		return nil, nil, err
	}
	return sources, inbounds, nil
}

// importedTemplateSourceURLLegal 是写面 isSubscriptionURL 的
// repo 侧镜像（handler 包不可反向导入，本地同集实现）：无空白、
// url.Parse 成功、scheme ∈ {http, https}、host 非空。仅形态校
// 验——恢复面不联网抓取模板内容。
func importedTemplateSourceURLLegal(text string) bool {
	if strings.ContainsAny(text, " \n\t") {
		return false
	}
	u, err := url.Parse(text)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (r *Repository) importInbounds(tx *gorm.DB, b *model.InboundsBackup, now int64, subRemap map[int64]int64, rep *importReport) (inboundImportCounts, error) {
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
	// X-05：SplitRules.domains sanitize 的回执计数（受影响行数 + 被
	// 剔除的非法域名数 + 被丢弃的失效规则数）与逐行身份/坏域名披露
	// 清单（汇总 warning 在入站循环后统一发，形态同
	// invalidWhitelistRows；行照常落库，故不计 SkippedRows，同
	// R5X-014 sanitize 回执口径）。
	invalidSplitRows := 0
	invalidSplitDomains := 0
	invalidSplitRules := 0
	var invalidSplitIdentities []string
	// R5X-016：入站段返回实落计数（被跳过行不计入，与 SkippedRows
	// 不重复计数）；R5X-024 的校验跳过同样不计。
	var counts inboundImportCounts
	invalidInboundObjects := 0
	// B4-02 残面：声明口四源复检跳过的行数与身份清单（汇总
	// warning 在入站循环后统一发，形态同 invalidInboundObjects）。
	portConflictInbounds := 0
	var portConflictInboundIdentities []string
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
		// B8-08：名/间隔/角色镜像写面（remote_source.go 保存面）。
		// 此前仅 URL 空标签一门（R-06），其余原样落库：间隔集外
		// 值使消费面 remoteSourceDue 永不到期（999999 小时约 114
		// 年、负值同理）且回执无提示，订阅内容静默陈旧；任意
		// role 字符串绕过写面归一。处置镜像写面：名 trim 后空/
		// 超长跳行、间隔不在 0-168 跳行（写面同为拒收）、role
		// 集外归一 "" 并在原值非空时 warning。文件源先按写面口
		// 径强制 URL=""/间隔=0 再过门（写面保存时即如此强制，
		// 合法备份恒为清零形态）。
		url := src.URL
		intervalHours := src.IntervalHours
		if kind == model.RemoteSourceFile {
			url, intervalHours = "", 0
		}
		name := strings.TrimSpace(src.Name)
		if name == "" || len([]rune(name)) > 100 {
			rep.skip("sources")
			rep.warnf("备份恢复：订阅源 #%d 的名称为空或超过 100 个字符，已跳过该源未恢复", src.ID)
			continue
		}
		if intervalHours < 0 || intervalHours > 168 {
			rep.skip("sources")
			rep.warnf("备份恢复：订阅源 #%d（%q）的更新间隔 %d 小时不在 0-168 内，已跳过该源未恢复", src.ID, name, src.IntervalHours)
			continue
		}
		role := src.Role
		if role != "relay" && role != "landing" {
			if role != "" {
				rep.warnf("备份恢复：订阅源 #%d（%q）的角色 %q 不在合法集 {relay, landing} 内，已按普通源（无角色）恢复", src.ID, name, src.Role)
			}
			role = ""
		}
		// 复扫 R-06：恢复面与保存面同口径——空标签订阅源 URL 行跳过 +
		// warning（文件源无 URL，不判）。
		if kind == model.RemoteSourceURL && urlHostHasEmptyLabel(url) {
			rep.skip("sources")
			rep.warnf("备份恢复：订阅源 #%d（%q）的地址 %q 含空标签主机名，已跳过该源未恢复", src.ID, name, url)
			continue
		}
		item := model.RemoteSource{
			ID: src.ID, Name: name, URL: url, IntervalHours: intervalHours,
			RelayOnly: src.RelayOnly, Kind: kind, Role: role, Enabled: src.Enabled, CreatedTime: src.CreatedTime, UpdatedTime: now,
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

	// F6-02：删除屏蔽登记随源落库（源已按 ID 保号恢复，屏蔽键的
	// 源 ID 仍指向同一源）。（source_id, identity_key）唯一键 upsert，
	// 重复恢复幂等。源行不存在的屏蔽（源段被跳过/不在本备份内）
	// 跳过并计数披露，不写悬空登记。
	for _, sh := range b.Shields {
		if sh.IdentityKey == "" {
			rep.skip("shields")
			continue
		}
		shieldSourceExists, err := rowExistsTx(tx, &model.RemoteSource{}, sh.SourceID)
		if err != nil {
			return inboundImportCounts{}, err
		}
		if !shieldSourceExists {
			rep.skip("shields")
			continue
		}
		created := sh.CreatedTime
		if created == 0 {
			created = now
		}
		item := model.RemoteSourceShield{
			SourceID: sh.SourceID, IdentityKey: sh.IdentityKey, Name: sh.Name, CreatedTime: created,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "source_id"}, {Name: "identity_key"}},
			DoUpdates: clause.AssignmentColumns([]string{"name"}),
		}).Create(&item).Error; err != nil {
			return inboundImportCounts{}, err
		}
		counts.shields++
	}

	// 节点组段披露：节点组功能已移除（1.9.6），旧备份的 nodeGroups
	// 只计数点名、不再落库（InboundsBackup.NodeGroups 仅作解码壳）；
	// 被忽略条数同时计入回执的 InboundNodeGroupsIgnored（R3-S2-01，
	// 与上方 warnf 的文本披露同数）。
	if len(b.NodeGroups) > 0 {
		counts.groupsIgnored += len(b.NodeGroups)
		rep.warnf("备份恢复：节点组功能已移除，备份中的 nodeGroups 段（%d 条）已忽略未恢复", len(b.NodeGroups))
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
		// RR4-F2-01：自建入站落库用校验返回的三对象形态——被愈合
		// 改变的落愈合形态、未触碰的逐字（见 validateImportedInboundObjects）。
		healedSettings, healedTransport, healedSecurity, validateErr := validateImportedInboundObjects(tx, i, listen, strategy)
		if validateErr != nil {
			err := validateErr
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
		// X-05：SplitRules 是入站行内的一段 JSON 字符串，其 domains
		// 子字段不在顶层列枚举视野内——写面拒收 `a..b`，恢复面此前
		// 逐字落库。按 R-08 同口径逐项剔除坏域名 + warning；剔除后
		// 无任何匹配项的规则整条丢弃，合法条目逐字保留，入站行本身
		// 照常恢复（不整行跳过）。
		splitRules, splitBadDomains, splitDroppedRules, splitParsed := sanitizeBackupSplitRules(i.SplitRules)
		if splitParsed && (len(splitBadDomains) > 0 || splitDroppedRules > 0) {
			invalidSplitRows++
			invalidSplitDomains += len(splitBadDomains)
			invalidSplitRules += splitDroppedRules
			quoted := make([]string, 0, len(splitBadDomains))
			for _, d := range splitBadDomains {
				quoted = append(quoted, fmt.Sprintf("%q", d))
			}
			detail := "非法域名：" + strings.Join(quoted, "、")
			if splitDroppedRules > 0 {
				detail += fmt.Sprintf("；丢弃失效规则 %d 条", splitDroppedRules)
			}
			invalidSplitIdentities = append(invalidSplitIdentities,
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
		// B4-02 残面：入站声明口落库前四源占用复检（与转发面
		// R1-03-M2、链路面同型）——预检排除集按备份 ID 全集组
		// 装，被行门跳过的转发其存量端口行在导入后存活，入站声
		// 明与之同节点同口时预检放行、此前落库无门，可跨表双
		// 落。入站段在 forwards 段之后执行，转发/链路终态已落
		// 库，排除集只需含本行自身（将被按 ID 替换的存量行）；
		// 同批后序入站的存量行若处于漂移态（声明口与存量口不
		// 同），按占用计保守跳过 + 点名，重导即可自愈。冲突整
		// 行跳过 + 计数，身份随汇总 warning 披露。
		// node_id=0 的 external 入站端口在远端主机上，不参与本
		// 地节点端口空间（与预检同口径），不判。
		if i.NodeID > 0 && i.Port > 0 {
			source, occErr := r.NodePortOccupantTx(tx, i.NodeID, i.Port, PortExclusions{InboundIDs: []int64{i.ID}})
			if occErr != nil {
				return inboundImportCounts{}, occErr
			}
			if source != "" {
				rep.skip("inbounds")
				portConflictInbounds++
				portConflictInboundIdentities = append(portConflictInboundIdentities,
					fmt.Sprintf("#%d %s（节点 #%d 端口 %d 已被%s占用）", i.ID, i.Name, i.NodeID, i.Port, backupPortSourceLabel(source)))
				continue
			}
		}
		item := model.NodeInbound{
			ID: i.ID, KernelID: i.KernelID, NodeID: i.NodeID, Name: i.Name, Protocol: protocol,
			Listen: listen, Port: i.Port, Transport: healedTransport, Security: healedSecurity,
			Settings: healedSettings, Extra: i.Extra, ShareHost: shareHost, ShareAddressMode: shareMode, Remark: i.Remark,
			RelayOnly: i.RelayOnly, AcceptProxyProtocol: i.AcceptProxyProtocol,
			RelayWhitelist: i.RelayWhitelist, WhitelistExtra: whitelistExtra, Enabled: i.Enabled,
			UploadBytes: clampImportedCounter(rep, fmt.Sprintf("入站 #%d（%q）", i.ID, i.Name), "uploadBytes", i.UploadBytes), DownloadBytes: clampImportedCounter(rep, fmt.Sprintf("入站 #%d（%q）", i.ID, i.Name), "downloadBytes", i.DownloadBytes), Inx: i.Inx,
			CreatedTime: i.CreatedTime, UpdatedTime: now, SourceID: i.SourceID, OutboundID: i.OutboundID,
			SplitRules: splitRules, OutboundIPStrategy: strategy, MeteringEnabled: i.MeteringEnabled,
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
	if portConflictInbounds > 0 {
		// B4-02 残面：声明口复检跳过的汇总披露（截断口径同上）。
		identities := portConflictInboundIdentities
		suffix := ""
		if len(identities) > 10 {
			suffix = fmt.Sprintf(" 等共 %d 条", len(identities))
			identities = identities[:10]
		}
		rep.warnf("备份恢复：%d 条入站声明的端口已被占用，已整行跳过未恢复：%s%s，请核对端口占用后重试",
			portConflictInbounds, strings.Join(identities, "、"), suffix)
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
	if invalidSplitRows > 0 {
		// X-05：汇总披露受影响行数 + 被剔除非法域名数 + 被丢弃失效
		// 规则数 + 逐行身份与坏域名原文（前 10 条，超出以总数收尾，
		// 同 invalidWhitelistRows 的截断口径）。行已照常恢复，此处
		// 计数只入 ImportWarnings、不计 SkippedRows。
		identities := invalidSplitIdentities
		suffix := ""
		if len(identities) > 10 {
			suffix = fmt.Sprintf(" 等共 %d 条", len(identities))
			identities = identities[:10]
		}
		rep.warnf("备份恢复：%d 条入站的分流规则域名（SplitRules.domains）含空标签主机名，已剔除 %d 个非法域名、丢弃 %d 条失效规则后恢复（合法域名与规则逐字保留）：%s%s，请核对被剔除的域名后在入站分流设置中重新填写",
			invalidSplitRows, invalidSplitDomains, invalidSplitRules, strings.Join(identities, "、"), suffix)
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
		// F2-02：基线按节点键控，恢复行须归属到入站所属节点（备份
		// 格式不带 node_id，以恢复后入站行的 node_id 为准），否则
		// 与新唯一键冲突目标不符、且会被启动迁移按无主基线清掉。
		var counterInbound struct{ NodeID int64 }
		err := tx.Model(&model.NodeInbound{}).Select("node_id").Where("id = ?", c.InboundID).First(&counterInbound).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			rep.skip("inboundCounters")
			continue
		}
		if err != nil {
			return inboundImportCounts{}, err
		}
		item := model.NodeInboundCounter{
			NodeID: counterInbound.NodeID, InboundID: c.InboundID, Protocol: c.Protocol, Direction: c.Direction,
			Epoch: c.Epoch, Bytes: clampImportedCounter(rep, fmt.Sprintf("入站计数基线（入站 #%d）", c.InboundID), "bytes", c.Bytes), UpdatedTime: now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "node_id"}, {Name: "inbound_id"}, {Name: "protocol"}, {Name: "direction"}},
			DoUpdates: clause.AssignmentColumns([]string{"epoch", "bytes", "updated_time"}),
		}).Create(&item).Error; err != nil {
			return inboundImportCounts{}, err
		}
		counts.counters++
	}
	// Templates land before subscriptions: subscription Options carry
	// template ids, and the reference pass below keeps a slot only when
	// its template exists after this upsert (same dangling-reference
	// discipline as the inbound refs pass — the slot is dropped, the
	// subscription itself restores and renders built-in).
	for _, tp := range b.Templates {
		// B8-09：模板恢复面镜像写面（subscription_template.go
		// 保存面 content()/CheckTemplate 门）。此前逐字 upsert、
		// 无一门：kind 集外、内容不合 kind 语法、builder 与
		// sourceUrl 同设、坏 URL 形态均可落库，消费面渲染时才
		// 失败且回执无提示。处置镜像写面，逐行跳过 + warning：
		// 名 trim 后空/超 100 跳；kind ∉ kernel.TemplateFormats
		// {clash, singbox, surge} 跳；builderConfig 非空时按写面
		// content() 同路生成内容（builder 与 sourceUrl 同设跳、
		// 生成文本取代 content 字段——写面保存的就是生成文本）；
		// sourceUrl 非空时仅校验 http(s) 形态（镜像
		// isSubscriptionURL）——恢复面不联网抓取，内容取备份自
		// 带 content；最终内容过 kernel.CheckTemplate，失败跳行
		// 并在回执点名校验错误。被跳模板的订阅引用由下方既有
		// remapTemplateRefs 摘除（悬空引用纪律不变）。
		name := strings.TrimSpace(tp.Name)
		if name == "" || len([]rune(name)) > 100 {
			rep.skip("templates")
			rep.warnf("备份恢复：订阅模板 #%d 的名称为空或超过 100 个字符，已跳过该模板未恢复", tp.ID)
			continue
		}
		kindOK := false
		for _, k := range kernel.TemplateFormats {
			if k == tp.Kind {
				kindOK = true
				break
			}
		}
		if !kindOK {
			rep.skip("templates")
			rep.warnf("备份恢复：订阅模板 #%d（%q）的格式 %q 不在合法集 {clash, singbox, surge} 内，已跳过该模板未恢复", tp.ID, name, tp.Kind)
			continue
		}
		content := strings.ReplaceAll(tp.Content, "\r\n", "\n")
		sourceURL := strings.TrimSpace(tp.SourceURL)
		if strings.TrimSpace(tp.BuilderConfig) != "" {
			if sourceURL != "" {
				rep.skip("templates")
				rep.warnf("备份恢复：订阅模板 #%d（%q）同时带有规则配置与网址模板，与保存面同口径不可同设，已跳过该模板未恢复", tp.ID, name)
				continue
			}
			var builder kernel.TemplateBuilder
			if err := json.Unmarshal([]byte(tp.BuilderConfig), &builder); err != nil {
				rep.skip("templates")
				rep.warnf("备份恢复：订阅模板 #%d（%q）的规则配置无法解析（%v），已跳过该模板未恢复", tp.ID, name, err)
				continue
			}
			if builder.Groups == nil {
				builder.Groups = []kernel.TemplateBuilderGroup{}
			}
			if builder.Rules == nil {
				builder.Rules = []kernel.TemplateBuilderRule{}
			}
			built, err := kernel.BuildSubscriptionTemplate(tp.Kind, builder)
			if err != nil {
				rep.skip("templates")
				rep.warnf("备份恢复：订阅模板 #%d（%q）的规则配置生成失败（%v），已跳过该模板未恢复", tp.ID, name, err)
				continue
			}
			content = built
		} else if sourceURL != "" && !importedTemplateSourceURLLegal(sourceURL) {
			rep.skip("templates")
			rep.warnf("备份恢复：订阅模板 #%d（%q）的网址模板地址不是合法 http(s) 地址，已跳过该模板未恢复", tp.ID, name)
			continue
		}
		if _, err := kernel.CheckTemplate(tp.Kind, content, nil); err != nil {
			rep.skip("templates")
			rep.warnf("备份恢复：订阅模板 #%d（%q）的内容未通过保存面同口径校验（%v），已跳过该模板未恢复", tp.ID, name, err)
			continue
		}
		item := model.SubscriptionTemplate{
			ID: tp.ID, Name: name, Kind: tp.Kind, Content: content,
			BuilderConfig: tp.BuilderConfig, SourceURL: sourceURL,
			LastFetch: tp.LastFetch, LastError: tp.LastError,
			CreatedTime: tp.CreatedTime, UpdatedTime: now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"name", "kind", "content", "builder_config", "source_url", "last_fetch", "last_error", "updated_time"}),
		}).Create(&item).Error; err != nil {
			return inboundImportCounts{}, err
		}
		counts.templates++
	}
	danglingTemplateRefs := 0
	remapTemplateRefs := func(raw string) string {
		if strings.TrimSpace(raw) == "" {
			return raw
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &top); err != nil {
			return raw
		}
		rawTpl, ok := top["templates"]
		if !ok {
			return raw
		}
		var slots map[string]int64
		if err := json.Unmarshal(rawTpl, &slots); err != nil || len(slots) == 0 {
			return raw
		}
		changed := false
		for client, id := range slots {
			if id <= 0 {
				continue
			}
			exists, err := rowExistsTx(tx, &model.SubscriptionTemplate{}, id)
			if err != nil || exists {
				continue
			}
			delete(slots, client)
			danglingTemplateRefs++
			changed = true
		}
		if !changed {
			return raw
		}
		encoded, err := json.Marshal(slots)
		if err != nil {
			return raw
		}
		top["templates"] = encoded
		out, err := json.Marshal(top)
		if err != nil {
			return raw
		}
		return string(out)
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
	// 节点组引用披露计数：订阅行不再写入 groupIds（节点组功能已
	// 移除），携带非空引用的订阅在此计数、循环后统一披露。
	subsWithGroupIDs := 0
	subs := append([]model.SubscriptionBackup(nil), b.Subscriptions...)
	for _, u := range b.Users {
		subs = append(subs, model.SubscriptionBackup{
			Name: u.Name, Kind: model.SubscriptionShare, Token: u.Token, UUID: u.UUID, Password: u.Password,
			InboundIDs: u.InboundIDs, ExpireTime: u.ExpireTime, Enabled: u.Enabled, Remark: u.Remark,
			CreatedTime: u.CreatedTime,
		})
	}
	// RR4-F1-01 / RR4-F4-01：订阅行自身的引用对账。源与入站都可被
	// 同批跳过（源 URL 空标签、入站内核/节点缺失）、备份也可携从未
	// 存在的幽灵 id——此前订阅行逐字落库，悬空引用渲染时静默丢
	// 节点、且该订阅下次 UI 保存被保存面硬拒直至手动摘引用。存在
	// 集以本事务此刻表状态为准（subscriptionRefUniverseTx），剔除
	// 与披露口径同模板槽位（remapTemplateRefs）。
	validSourceIDs, validInboundIDs, err := subscriptionRefUniverseTx(tx, subs)
	if err != nil {
		return inboundImportCounts{}, err
	}
	// remapChainSourceRefs processes only the Options chain-by-source
	// slots, mirroring remapTemplateRefs: entries naming a source that
	// does not exist after this import are removed, and the removed
	// source ids are returned for the receipt.
	remapChainSourceRefs := func(raw string) (string, []int64) {
		if strings.TrimSpace(raw) == "" {
			return raw, nil
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &top); err != nil {
			return raw, nil
		}
		var dropped []int64
		changed := false
		for _, key := range []string{"chainLandingSources", "chainRelaySources"} {
			rawList, ok := top[key]
			if !ok {
				continue
			}
			var ids []int64
			if err := json.Unmarshal(rawList, &ids); err != nil {
				continue
			}
			kept := make([]int64, 0, len(ids))
			keyChanged := false
			for _, id := range ids {
				if id > 0 && !validSourceIDs[id] {
					dropped = append(dropped, id)
					keyChanged = true
					continue
				}
				kept = append(kept, id)
			}
			if !keyChanged {
				continue
			}
			encoded, err := json.Marshal(kept)
			if err != nil {
				return raw, nil
			}
			top[key] = encoded
			changed = true
		}
		if rawFilters, ok := top["chainSourceFilters"]; ok {
			var filters map[int64]string
			if err := json.Unmarshal(rawFilters, &filters); err == nil {
				filtersChanged := false
				for id := range filters {
					if id > 0 && !validSourceIDs[id] {
						delete(filters, id)
						dropped = append(dropped, id)
						filtersChanged = true
					}
				}
				if filtersChanged {
					encoded, err := json.Marshal(filters)
					if err != nil {
						return raw, nil
					}
					top["chainSourceFilters"] = encoded
					changed = true
				}
			}
		}
		if !changed {
			return raw, nil
		}
		out, err := json.Marshal(top)
		if err != nil {
			return raw, nil
		}
		return string(out), dropped
	}
	danglingSubRefSubs := 0
	danglingSourceRefCount := 0
	danglingInboundRefCount := 0
	var danglingSubRefDetails []string
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
		if strings.TrimSpace(sb.GroupIDs) != "" {
			subsWithGroupIDs++
		}
		// RR4-F1-01 / RR4-F4-01：落库前对账本行引用——剔除指向未
		// 恢复源/入站的 SourceIDs、InboundIDs 与 Options 链式按源
		// 引用（含 chainSourceFilters 键），受影响订阅循环后点名。
		keptInboundIDs, droppedInboundRefs := filterBackupRefIDList(sb.InboundIDs, validInboundIDs)
		keptSourceIDs, droppedSourceRefs := filterBackupRefIDList(sb.SourceIDs, validSourceIDs)
		cleanOptions, droppedChainRefs := remapChainSourceRefs(remapTemplateRefs(sb.Options))
		if len(droppedChainRefs) > 0 {
			seenDropped := make(map[int64]bool, len(droppedSourceRefs))
			for _, id := range droppedSourceRefs {
				seenDropped[id] = true
			}
			for _, id := range droppedChainRefs {
				if !seenDropped[id] {
					seenDropped[id] = true
					droppedSourceRefs = append(droppedSourceRefs, id)
				}
			}
		}
		if len(droppedSourceRefs) > 0 || len(droppedInboundRefs) > 0 {
			danglingSubRefSubs++
			danglingSourceRefCount += len(droppedSourceRefs)
			danglingInboundRefCount += len(droppedInboundRefs)
			label := sb.Name
			if label == "" {
				label = sb.Token
			}
			detail := fmt.Sprintf("「%s」", label)
			if len(droppedSourceRefs) > 0 {
				detail += fmt.Sprintf(" 源引用 %v", droppedSourceRefs)
			}
			if len(droppedInboundRefs) > 0 {
				detail += fmt.Sprintf(" 入站引用 %v", droppedInboundRefs)
			}
			danglingSubRefDetails = append(danglingSubRefDetails, detail)
		}
		item := model.Subscription{
			Name: sb.Name, Kind: sb.Kind, Token: sb.Token, UUID: sb.UUID, Password: sb.Password,
			SourceAll: sb.SourceAll, InboundIDs: keptInboundIDs, SourceIDs: keptSourceIDs, Options: cleanOptions, ExpireTime: sb.ExpireTime, Enabled: sb.Enabled, Remark: sb.Remark, Inx: sb.Inx, CreatedTime: sb.CreatedTime, UpdatedTime: now,
			UploadBytes: clampImportedCounter(rep, fmt.Sprintf("订阅 #%d（%q）", sb.ID, sb.Name), "uploadBytes", sb.UploadBytes), DownloadBytes: clampImportedCounter(rep, fmt.Sprintf("订阅 #%d（%q）", sb.ID, sb.Name), "downloadBytes", sb.DownloadBytes), TrafficLimit: sb.TrafficLimit,
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

	if subsWithGroupIDs > 0 {
		rep.warnf("备份恢复：节点组功能已移除，%d 条订阅携带的 groupIds 引用已忽略未恢复（订阅本身与其余来源照常恢复）", subsWithGroupIDs)
	}

	if danglingTemplateRefs > 0 {
		rep.warnf("备份恢复：%d 处订阅的模板引用指向不存在的模板，已剔除对应槽位（订阅改走内置渲染），请在订阅编辑中重新选择模板", danglingTemplateRefs)
	}

	if danglingSubRefSubs > 0 {
		// RR4-F1-01 / RR4-F4-01：汇总 warning 点名受影响订阅与被剔除
		// 的引用 id（前 10 条，超出以总数收尾，同 invalidInboundObjects
		// 的截断口径）；计数与文本同源真计数。
		details := danglingSubRefDetails
		suffix := ""
		if len(details) > 10 {
			suffix = fmt.Sprintf(" 等共 %d 条订阅", len(details))
			details = details[:10]
		}
		rep.warnf("备份恢复：%d 条订阅引用了未恢复的订阅源/入站，已剔除悬空引用 %d 处（源 %d、入站 %d）：%s%s，请在订阅编辑中按需重新选择",
			danglingSubRefSubs, danglingSourceRefCount+danglingInboundRefCount,
			danglingSourceRefCount, danglingInboundRefCount,
			strings.Join(details, "、"), suffix)
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
			Epoch: c.Epoch, Bytes: clampImportedCounter(rep, fmt.Sprintf("订阅计数基线（订阅 #%d）", subID), "bytes", c.Bytes), UpdatedTime: now,
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
	// R2-F2-C-B01：链式策略冲突只披露不改行。恢复出的「出站 IP 策略 ×
	// 链式下一跳字面 IP」冲突组合在保存面会被拒绝（validateChainVersion），
	// 导入面不改恢复语义（行照常恢复、路由列不动），但必须在回执点名——
	// 否则渲染期把链式出站静默标 unreachable 丢弃流量、回执无声。以剥离
	// 后的最终落库形态判定（悬空引用已清为直连的行不再算冲突）。
	conflictNames := []string{}
	for _, ib := range rows {
		var row model.NodeInbound
		res := tx.Where("id = ?", ib.ID).Limit(1).Find(&row)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 || row.OutboundIPStrategy == "" {
			continue
		}
		conflict, err := importedChainPolicyConflictTx(tx, &row)
		if err != nil {
			return err
		}
		if conflict {
			conflictNames = append(conflictNames, row.Name)
		}
	}
	if len(conflictNames) > 0 {
		names := conflictNames
		suffix := ""
		if len(names) > 10 {
			suffix = fmt.Sprintf(" 等共 %d 条", len(conflictNames))
			names = names[:10]
		}
		rep.warnf("备份恢复：%d 条入站的出站 IP 策略与其链式下一跳的地址冲突（%s%s），已照常恢复，但其链式流量将被丢弃，请在入站设置中调整出站 IP 策略或下一跳地址", len(conflictNames), strings.Join(names, "、"), suffix)
	}
	return nil
}

// importedChainPolicyConflictTx reports whether a restored inbound's
// outbound IP strategy conflicts with the literal address of any of
// its chained next hops — the combination the save surface refuses
// (handler validateChainVersion). Disclosure only; nothing is
// modified.
func importedChainPolicyConflictTx(tx *gorm.DB, row *model.NodeInbound) (bool, error) {
	targetIDs := []int64{}
	if row.OutboundID > 0 {
		targetIDs = append(targetIDs, row.OutboundID)
	}
	if strings.TrimSpace(row.SplitRules) != "" {
		var rawRules []json.RawMessage
		if err := json.Unmarshal([]byte(row.SplitRules), &rawRules); err == nil {
			for _, rr := range rawRules {
				var probe struct {
					OutboundID int64 `json:"outboundId"`
				}
				if err := json.Unmarshal(rr, &probe); err == nil && probe.OutboundID > 0 {
					targetIDs = append(targetIDs, probe.OutboundID)
				}
			}
		}
	}
	for _, tid := range targetIDs {
		var target model.NodeInbound
		res := tx.Where("id = ?", tid).Limit(1).Find(&target)
		if res.Error != nil {
			return false, res.Error
		}
		if res.RowsAffected == 0 {
			continue
		}
		host, err := importedChainHostTx(tx, row, &target)
		if err != nil {
			return false, err
		}
		if host == "" {
			continue
		}
		if err := kernel.ValidateOutboundIPServer(row.OutboundIPStrategy, host); err != nil {
			return true, nil
		}
	}
	return false, nil
}

// importedChainHostTx mirrors the host selection of the handler's
// chainEndpoint (the repo layer cannot import the handler; the
// handler stays authoritative — the same restatement discipline as
// meteringShapeBlockedBackup). Only the dialed host matters here.
// Cases where the handler's endpoint resolution errors out (a
// PROXY-protocol target, a loopback-only listen on another node, a
// missing share address) yield "": those fail the referrer's next
// apply loudly and by name, unlike the silent strategy drop this
// disclosure exists for.
func importedChainHostTx(tx *gorm.DB, referrer, target *model.NodeInbound) (string, error) {
	if target.KernelID == 0 { // imported (external) inbound
		if h := strings.TrimSpace(target.ShareHost); h != "" {
			return h, nil
		}
		return target.Listen, nil
	}
	if target.AcceptProxyProtocol == 1 {
		return "", nil
	}
	if target.NodeID == referrer.NodeID {
		if ip := net.ParseIP(target.Listen); ip != nil && !ip.IsUnspecified() {
			return ip.String(), nil
		}
		return "127.0.0.1", nil
	}
	if ip := net.ParseIP(target.Listen); ip != nil && ip.IsLoopback() {
		return "", nil
	}
	var node model.Node
	res := tx.Where("id = ?", target.NodeID).Limit(1).Find(&node)
	if res.Error != nil {
		return "", res.Error
	}
	var np *model.Node
	if res.RowsAffected > 0 {
		np = &node
	}
	return importedShareAddress(target, np), nil
}

// importedShareMode / importedShareAddress / importedNodeShareIP /
// importedStripShareHostPort mirror the handler's share-address
// resolution (handler/kernel_share_address.go) for the disclosure
// check above; keep them in agreement with the handler.
func importedShareMode(row *model.NodeInbound) string {
	mode := strings.TrimSpace(row.ShareAddressMode)
	if (mode == "" || mode == "auto") && strings.TrimSpace(row.ShareHost) != "" {
		return "custom"
	}
	switch mode {
	case "ipv4", "ipv6", "custom":
		return mode
	default:
		return "auto"
	}
}

func importedShareAddress(row *model.NodeInbound, node *model.Node) string {
	switch importedShareMode(row) {
	case "custom":
		return kernel.CanonicalShareHostValue(row.ShareHost)
	case "ipv4":
		return importedNodeShareIP(node, false)
	case "ipv6":
		return importedNodeShareIP(node, true)
	default:
		if node == nil {
			return ""
		}
		if h := importedStripShareHostPort(node.ServerIP); h != "" {
			return h
		}
		if h := importedNodeShareIP(node, false); h != "" {
			return h
		}
		return importedNodeShareIP(node, true)
	}
}

func importedNodeShareIP(node *model.Node, ipv6 bool) string {
	if node == nil {
		return ""
	}
	value := node.ServerIPV4.String
	if ipv6 {
		value = node.ServerIPV6.String
	}
	for _, host := range []string{value, node.ServerIP} {
		addr, err := netip.ParseAddr(importedStripShareHostPort(host))
		if err == nil && addr.Is6() == ipv6 && !addr.Is4In6() {
			return addr.String()
		}
	}
	return ""
}

func importedStripShareHostPort(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return strings.Trim(value, "[]")
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
