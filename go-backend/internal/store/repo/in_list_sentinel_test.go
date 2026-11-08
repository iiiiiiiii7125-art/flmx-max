package repo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R3 K8 / X01-001 机械对账哨兵：无界 IN 家族三轮审计反复在「新点位
// 未归类」处复发（R1 T2-NEW-01 整表集 → R2 X01-001 监控集 → R3
// X01-001 单实体集）。本测试把 repo 包内全部含 IN ? / IN (?) /
// NOT IN 字面量的函数体钉成闭合登记表：
//   - 扫描发现未登记点位 → 红（新点位必须先归类：分块/字面量/
//     子查询/有界豁免/排除集反连接，并在此登记理由）；
//   - 登记项在源码中消失 → 红（防登记表与代码漂移）；
//   - 归类为分块/手切/反连接的点位，函数体必须出现对应的分块
//     证据串（chunkInt64IDs / [start:end] / inListChunkSize），
//     防「登记了分块、实现被改回整体回灌」。
//
// 上限口径：SQLite 32766 / PostgreSQL 65535 个 SQL 变量
// （repository_in_list.go 头注）。

type inListSiteClass struct {
	// want 是函数体源码中必须出现的证据串；空表示该归类无需
	// 体内证据（字面量/子查询/有界豁免，理由见 note）。
	want string
	note string
}

// inListSiteRegistry 是 IN 家族点位闭合清单（文件:函数 → 归类）。
// 与 lanes/X01/r3/in-list-enumeration.md（A/B 集）、
// lanes/X04/r3/enumeration.md（61 处归类）、lanes/K8/r3/rescan.md
// 三份枚举互为对照；增删点位须同步三处。
var inListSiteRegistry = map[string]inListSiteClass{
	// —— 分块（chunkInt64IDs）——
	"repository.go:ListNodes":                                           {"chunkInt64IDs", "整表节点集，T2-NEW-01 已分块"},
	"repository.go:ListForwards":                                        {"chunkInt64IDs", "整表转发集，已分块"},
	"repository.go:getTunnelEntryPortRanges":                            {"chunkInt64IDs", "整表隧道集，已分块"},
	"repository.go:Import":                                              {"chunkInt64IDs", "备份载荷全量，已分块"},
	"repository.go:importTunnels":                                       {"chunkInt64IDs", "备份载荷隧道集，已分块"},
	"repository_flow.go:GetFlowUploadForwardMetas":                      {"chunkFlowUploadForwardIDs", "整表转发集，经 flow 专用分块器（500/块）分块"},
	"repository_mutations.go:ListTunnelIPPreferences":                   {"chunkInt64IDs", "整轮探测隧道集，已分块"},
	"repository_mutations.go:validateIDsExistTx":                        {"chunkInt64IDs", "请求载荷无上限，已分块"},
	"repository_monitor_nodes.go:SetNodesLatencyTargets":                {"chunkInt64IDs", "R2 X01-001 已分块"},
	"repository_nft_pending_delete.go:GetForwardStatusMap":              {"chunkInt64IDs", "请求载荷无上限，已分块"},
	"repository_user_quota.go:ListUserQuotaViewsByUserIDs":              {"chunkInt64IDs", "整表用户集，已分块"},
	"repository_user_subscription.go:ListUserSubscriptions":             {"chunkInt64IDs", "K8/B6：单用户订阅分配集无上界，已分块+内存重排"},
	"repository_in_list.go:deleteByIDSliceInChunksTx":                   {"chunkInt64IDs", "K8：级联物化集分块删除助手本体"},
	"repository_inbound_backup.go:existingIDSetTx":                      {"chunkInt64IDs", "RR4-F1-01/F4-01：订阅恢复引用对账的候选 id 集（备份载荷引用全集），已分块"},
	"repository_kernel.go:pluckInboundRelayIDsByColumnTx":               {"chunkInt64IDs", "K8：relay 物化分块收口点"},
	"repository_kernel.go:deleteInboundRowsByIDsTx":                     {"chunkInt64IDs", "K8/B1+B2：删内核/删节点入站附属行分块清理"},
	"repository_cascade_cleanup.go:loadNftPendingDeletesByForwardIDsTx": {"chunkInt64IDs", "FIX-CASCADE/E1-01：墓碑台账按 forward 集物化，已分块"},
	"repository_cascade_cleanup.go:forwardIDsWithRemainingPortsTx":      {"chunkInt64IDs", "FIX-CASCADE/E1-11：受影响转发集的剩余端口判定，已分块"},

	// —— 手工切片分块（[start:end] 窗口）——
	"repository.go:exportForwardPortsBatch":       {"[start:end]", "整表转发口集，手工切片分块"},
	"repository.go:resolveForwardIngressBatch":    {"[start:end]", "请求载荷去重集，手工切片分块"},
	"repository_flow.go:MapForwardIDsToTunnelIDs": {"[start:end]", "整表转发集，手工切片分块"},

	// —— 代码字面量小清单（构造上有界）——
	"repository.go:seedData":                                {"", "字面量 3 元品牌名清单"},
	"repository.go:ensurePostgresIDDefaults":                {"", "字面量 2 元类型名清单"},
	"repository.go:ListEnabledServiceMonitors":              {"", "字面量 2 元协议清单"},
	"repository.go:importServiceMonitorLimits":              {"", "字面量 7 元服务监控限值配置键清单"},
	"repository_subscription.go:migrateSubscriptions":       {"", "字面量 5 元旧配置键清单"},
	"repository.go:migrateRemoveGistConfigKeys":             {"", "已下线 Gist 配置键清单已提升为 repo 包级单点 retiredConfigKeysList（config_policy.go），本函数与备份恢复过滤同源消费（R2-E3-F03）"},
	"repository_mutations.go:ListSoleSegmentTunnelsForNode": {"", "字面量 2 元链路段角色清单（入口/出口，L4-03）"},
	"repository_mutations.go:DeleteNodeCascadeWithOutcome":  {"", "字面量 2 元节点归属动态键清单（pending_realm_settle:<id> / kernel_orphan_recheck:<id>，FIX-CASCADE/E1-05）"},

	// —— 子查询形（IN (?) 传 *gorm.DB，不展开变量）——
	"repository_mutations.go:DeleteUserCascade": {"", "forwardIDs/userTunnelIDs 为 Select 子查询，X01 A 集已核"},

	// —— 有界豁免（前提经 R3 复核仍成立，见 rescan.md）——
	"repository.go:GetConfigsByNames":               {"", "全部调用方传代码字面量小清单（X01 A 集已核 5 处）"},
	"repository_kernel.go:applyInboundCounterChunk": {"", "调用方 ApplyInboundCounters 已按 200 条分片（X01 A 集）"},

	// —— 排除集反连接（K8/B5）——
	"repository_port_occupancy.go:existsOccupantNotExcludedTx": {"inListChunkSize", "小集合单条 NOT IN、大集合候选反连接；备份预检整单排除集经此收口"},
	"repository_port_occupancy.go:pluckPortsNotExcludedTx":     {"inListChunkSize", "同上（已用口清单形）"},
}

func TestInListSiteRegistry(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	found := make(map[string]string) // key -> 函数体源码
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(".", name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			hasIN := false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if ok && lit.Kind == token.STRING &&
					(strings.Contains(lit.Value, "IN ?") || strings.Contains(lit.Value, "IN (?)")) {
					hasIN = true
				}
				return true
			})
			if !hasIN {
				continue
			}
			start := fset.Position(fd.Body.Lbrace).Offset
			end := fset.Position(fd.Body.Rbrace).Offset
			if start < 0 || end > len(src) || start > end {
				t.Fatalf("body offsets out of range for %s:%s", name, fd.Name.Name)
			}
			found[name+":"+fd.Name.Name] = string(src[start:end])
		}
	}

	for key, body := range found {
		class, ok := inListSiteRegistry[key]
		if !ok {
			t.Errorf("未登记的 IN 点位 %s：新点位必须先归类（分块/字面量/子查询/有界豁免/反连接）并登记到 inListSiteRegistry", key)
			continue
		}
		if class.want != "" && !strings.Contains(body, class.want) {
			t.Errorf("IN 点位 %s 归类要求函数体含 %q，实际未见（实现可能已回退为整体回灌）", key, class.want)
		}
	}
	for key := range inListSiteRegistry {
		if _, ok := found[key]; !ok {
			t.Errorf("登记项 %s 在源码中已消失：请同步更新登记表与枚举清单", key)
		}
	}
}
