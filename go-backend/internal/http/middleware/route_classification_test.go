package middleware

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 路由分类普查门（L8-F-06）：管理员门现行架构是「requiresAdmin
// 前缀/精确清单 + 未命中默认放行任意登录用户」，RequireAdmin 无
// 调用点——新路由忘加清单即默认对普通用户开放，且此前无结构门
// 兜底。本测试不改鉴权行为，只立机制门：
//
//   1. 机械枚举全部注册路由（handler.go 字面量 + 备份端点循环
//      展开 + ghMirrorPath 常量 + router.go 直注册，并全仓清扫
//      注册面只许出现在 handler.go 与 router.go 两处）；
//   2. 每条注册路由必须在下方分类表里显式归类为管理员 / 普通
//      用户 / 公开面之一，未归类（或表里有多余条目）即红；
//   3. 每条分类必须与中间件三表（shouldSkip / requiresAdmin /
//      allowsOptionalAuth）的实际判定对位，对不上即红——分类
//      不是自说自话的标签，而是与执行面互相钉死。
//   4. 首参非字符串字面量的注册调用点（常量/变量名注册）必须
//      逐一登记在 nonLiteralRegistrationWhitelist（RB3-03）：
//      第 1 步的枚举与清扫都只认 AST 调用点的字面量首参，此类
//      注册对它们不可见，只能靠白名单逐一相等兜底，多一处/少
//      一处即红。
//
// R7-L8-01（RB3-03 残留）：第 1 步清扫与第 4 步白名单扫描原先
// 只覆盖 handler.go 与 router.go——第三个文件里的非字面量注册
// 对枚举/清扫/白名单全不可见（字面量注册则会被清扫的文件面规
// 则逮住）。现两道扫描都改走 collectCallSites 的
// 全后端整树调用点普查（任何文件的 .Handle/.HandleFunc 调用点
// 无一例外进入扫描，字面量与非字面量同覆盖），白名单键改用
// 相对 backend 根的路径，第三文件新增任何注册（字面量撞文件
// 面规则、非字面量撞白名单逐一相等）都点名报红。
//
// R8-023（L1-01）：本门的枚举与清扫原先以原始文本正则为真值
// 源，注释形态与真注册不可区分——把注册行整行注释掉（L1 变异
// M4b）或把备份清单条目注释掉（M4），两道门保持全绿。现全部
// 改以 go/parser 的 AST 为真值源：注释不是 AST 节点、字符串
// 内容也不是调用点，注释/字符串里的注册文本永不构成注册。
// 迁移等价性以修前修后枚举集与调用点全集逐条差集 ∅ 实证
// （立门时点枚举条数见文末订正注；现行计数不写死在此，以
// TestRouteClassificationCensus 的活普查输出为准，C1-04）。
// 常设变异红证
// TestRouteClassificationGateMutations 在合成后端树上复跑
// M1/M2/M3/M4/M4b/M4c/M5，任一变异漏红即门退化、当场报红。
//
// R8-050 处置（L1 O-01）：/api/v1/captcha/verify 无仓内调用方，
// 裁定保留为兼容端点（公开面分类不变），随下个大版本再评估
// 清理；登记性处置，无代码变更。R8-051 处置（L1 O-02）：联邦
// 对等面 Bearer 为独立自理的对等鉴权体系，与面板裸 JWT 口径
// 各自一致、非缺陷，仅口径备查，无代码变更。
//
// 分类口径的计数法（audit-max 台账 §9.8 订正）：总数由四类
// 构成——handler.go 字面量注册 + ghMirrorPath 常量注册 + 备份
// 循环双前缀展开 + router.go 直注册（/system-info）。各类的实
// 计数随注册面增减而变，不写死在注释里（C1-04：旧注释把立门
// 后某时点的 204 条当作现行总数，与活普查实数矛盾）；现行总
// 数与分类条数以 TestRouteClassificationCensus 的日志输出为
// 准（AG-01 口径：活计数不写死在注释里）。L1 的四分法（免
// 检 20 + 可选鉴权 1 + 中间件管理员 144 + 登录＋handler 门 46）
// 到本表三分法的映射：免检 20 → 公开面（gateSkip)；可选鉴权 1
// （/api/v1/config/get）→ 公开面（gateOptional)；中间件管理员
// 144 → 管理员（gateMiddleware)；登录＋handler 门 46 中，
// monitor 的 nodes/services 共 10 条是 handler 内 ensureAdmin
// Access 的真管理员面 → 管理员（gateHandler)，/error 与
// /system-info 是非 /api/ 自理面 → 公开面（gateNonAPI)，其余
// 34 条 → 普通用户（gateLogin)。
//
// 注：上文 R8-023 段与 L1 四分法的条数（189/20/144/46 等）是
// 立门时点（L1/L8 期）的推导记录，冻结备查、不要随现行计数
// 回改；其总计数 211 已按 audit-max 台账 §9.8 订正作废；现行
// 分类条数以 TestRouteClassificationCensus 的日志输出为准
// （AG-01 口径：活计数不写死在注释里）。

type routeClass int

const (
	classAdmin routeClass = iota + 1
	classUser
	classPublic
)

func (c routeClass) String() string {
	switch c {
	case classAdmin:
		return "管理员"
	case classUser:
		return "普通用户"
	case classPublic:
		return "公开面"
	}
	return "未知"
}

type routeGate int

const (
	gateMiddleware routeGate = iota + 1 // requiresAdmin 清单命中
	gateHandler                         // 中间件放行、handler 内管理员门
	gateLogin                           // 登录 + handler 本人/归属门
	gateSkip                            // shouldSkip 免检（凭据自理或设计公开）
	gateOptional                        // allowsOptionalAuth 可选鉴权
	gateNonAPI                          // 非 /api/ 面，JWT 不覆盖、各自自理
)

type routeEntry struct {
	path  string
	class routeClass
	gate  routeGate
}

func adm(p string) routeEntry  { return routeEntry{p, classAdmin, gateMiddleware} }
func admH(p string) routeEntry { return routeEntry{p, classAdmin, gateHandler} }
func usr(p string) routeEntry  { return routeEntry{p, classUser, gateLogin} }
func pub(p string) routeEntry  { return routeEntry{p, classPublic, gateSkip} }
func pubOpt(p string) routeEntry {
	return routeEntry{p, classPublic, gateOptional}
}
func pubNA(p string) routeEntry { return routeEntry{p, classPublic, gateNonAPI} }

// routeClassification 是全量注册路由的显式分类表。条数不在注释
// 写死：由 TestRouteClassificationCensus 机械普查并在日志输出，
// 注释里的裸数字无门可守、只会静默漂移（AG-01 教训：此处曾写
// 216、实数已涨过其上而无人察觉；同族计数一律改用 K11-COUNT
// 标记形态或不写数字）。
// 新增路由时：先在 handler.go/router.go 注册，再来这里加一行
// 分类；改某条路由的鉴权语义时同步改这里并让三表对位检查裁决。
var routeClassification = []routeEntry{
	// ---- 公开面：shouldSkip 免检（凭据自理或设计公开）----
	pub("/flow/config"), pub("/flow/test"), pub("/flow/upload"), pub("/flow/upload/v2"),
	pub("/api/v1/open_api/gh/"), pub("/api/v1/open_api/sub/"), pub("/api/v1/open_api/sub_store"),
	pub("/api/v1/captcha/check"), pub("/api/v1/captcha/verify"),
	pub("/api/v1/user/login"),
	pub("/api/v1/user/passkey/status"), pub("/api/v1/user/passkey/login/begin"), pub("/api/v1/user/passkey/login/finish"),
	pub("/api/v1/public/config/get"),
	// ---- 公开面：可选鉴权（无 token 可达，策略门在 handler 内）----
	pubOpt("/api/v1/config/get"),
	// ---- 公开面：非 /api/ 面（JWT 不覆盖）----
	pubNA("/error"), pubNA("/system-info"),

	// ---- 管理员：requiresAdmin 前缀/精确清单 ----
	adm("/api/v1/announcement/update"),
	adm("/api/v1/api/v1/backup/export"),
	adm("/api/v1/api/v1/backup/import"), adm("/api/v1/api/v1/backup/import-auto-backup"),
	adm("/api/v1/api/v1/backup/import-auto-backup/status"), adm("/api/v1/api/v1/backup/restore"),
	adm("/api/v1/backup/export"),
	adm("/api/v1/backup/import"), adm("/api/v1/backup/import-auto-backup"),
	adm("/api/v1/backup/import-auto-backup/status"), adm("/api/v1/backup/restore"),
	adm("/api/v1/config/update"), adm("/api/v1/config/update-single"),
	adm("/api/v1/inbound/batch-delete"), adm("/api/v1/inbound/batch-toggle"),
	adm("/api/v1/inbound/create"), adm("/api/v1/inbound/delete"),
	adm("/api/v1/inbound/external/update"), adm("/api/v1/inbound/import"),
	adm("/api/v1/inbound/list"), adm("/api/v1/inbound/probe"),
	adm("/api/v1/inbound/regenerate"), adm("/api/v1/inbound/relay/create"),
	adm("/api/v1/inbound/relay/delete"), adm("/api/v1/inbound/relay/list"),
	adm("/api/v1/inbound/relay/probe"), adm("/api/v1/inbound/reset-traffic"),
	adm("/api/v1/inbound/share"), adm("/api/v1/inbound/split"),
	adm("/api/v1/inbound/toggle"), adm("/api/v1/inbound/update"),
	adm("/api/v1/kernel/capabilities"), adm("/api/v1/kernel/control"),
	adm("/api/v1/kernel/install"), adm("/api/v1/kernel/list"),
	adm("/api/v1/kernel/logs"), adm("/api/v1/kernel/versions"),
	adm("/api/v1/monitor/permission/assign"), adm("/api/v1/monitor/permission/list"), adm("/api/v1/monitor/permission/remove"),
	adm("/api/v1/node/batch-delete"), adm("/api/v1/node/batch-set-group"),
	adm("/api/v1/node/batch-upgrade"),
	adm("/api/v1/node/check-status"), adm("/api/v1/node/create"),
	adm("/api/v1/node/delete"), adm("/api/v1/node/dismiss-expiry-reminder"),
	adm("/api/v1/node/group/create"), adm("/api/v1/node/group/delete"),
	adm("/api/v1/node/group/rename"),
	adm("/api/v1/node/install"), adm("/api/v1/node/latency/history"),
	adm("/api/v1/node/latency/probe"), adm("/api/v1/node/latency/targets"),
	adm("/api/v1/node/list"), adm("/api/v1/node/nftables/clear"),
	adm("/api/v1/node/nftables/pending-deletes"), adm("/api/v1/node/nftables/reconcile"),
	adm("/api/v1/node/nftables/test"), adm("/api/v1/node/releases"),
	adm("/api/v1/node/restart-schedule"),
	adm("/api/v1/node/rollback"), adm("/api/v1/node/route"),
	adm("/api/v1/node/route/probe"), adm("/api/v1/node/traffic/month"),
	adm("/api/v1/node/tune"), adm("/api/v1/node/update"),
	adm("/api/v1/node/update-order"), adm("/api/v1/node/upgrade"),
	adm("/api/v1/notify/test"),
	adm("/api/v1/speed-limit/create"), adm("/api/v1/speed-limit/delete"),
	adm("/api/v1/speed-limit/list"), adm("/api/v1/speed-limit/update"),
	adm("/api/v1/subscription/delete"),
	adm("/api/v1/subscription/list"),
	adm("/api/v1/subscription/preview"), adm("/api/v1/subscription/reset"),
	adm("/api/v1/subscription/reset-traffic"), adm("/api/v1/subscription/save"),
	adm("/api/v1/subscription/source/delete"), adm("/api/v1/subscription/source/list"),
	adm("/api/v1/subscription/source/refresh"), adm("/api/v1/subscription/source/save"),
	adm("/api/v1/subscription/template/catalog"), adm("/api/v1/subscription/template/check"),
	adm("/api/v1/subscription/template/delete"), adm("/api/v1/subscription/template/list"),
	adm("/api/v1/subscription/template/refresh"), adm("/api/v1/subscription/template/save"),
	adm("/api/v1/subscription/traffic"),
	adm("/api/v1/system/check-updates"), adm("/api/v1/system/storage"),
	adm("/api/v1/system/upgrade"), adm("/api/v1/system/upgrade/status"),
	adm("/api/v1/system/version"),
	adm("/api/v1/tunnel/batch-delete"), adm("/api/v1/tunnel/batch-delete-preview"),
	adm("/api/v1/tunnel/batch-delete-with-forwards"), adm("/api/v1/tunnel/batch-redeploy"),
	adm("/api/v1/tunnel/create"), adm("/api/v1/tunnel/delete"),
	adm("/api/v1/tunnel/delete-preview"), adm("/api/v1/tunnel/delete-with-forwards"),
	adm("/api/v1/tunnel/diagnose"), adm("/api/v1/tunnel/diagnose/stream"),
	adm("/api/v1/tunnel/get"), adm("/api/v1/tunnel/list"),
	adm("/api/v1/tunnel/update"), adm("/api/v1/tunnel/update-order"),
	adm("/api/v1/tunnel/user/assign"), adm("/api/v1/tunnel/user/batch-assign"),
	adm("/api/v1/tunnel/user/list"), adm("/api/v1/tunnel/user/remove"),
	adm("/api/v1/tunnel/user/update"),
	adm("/api/v1/user/create"), adm("/api/v1/user/delete"), adm("/api/v1/user/list"),
	adm("/api/v1/user/quota/reset"), adm("/api/v1/user/reset"), adm("/api/v1/user/update"),
	adm("/api/v1/user-inbound/list"), adm("/api/v1/user-inbound/set"),
	adm("/api/v1/user-subscription/list"), adm("/api/v1/user-subscription/set"),

	// ---- 管理员：handler 内门（monitoring 的 ensureAdminAccess）----
	admH("/api/v1/monitor/nodes"), admH("/api/v1/monitor/nodes/"),
	admH("/api/v1/monitor/services"), admH("/api/v1/monitor/services/"),
	admH("/api/v1/monitor/services/create"), admH("/api/v1/monitor/services/delete"),
	admH("/api/v1/monitor/services/latest-results"), admH("/api/v1/monitor/services/limits"),
	admH("/api/v1/monitor/services/run"), admH("/api/v1/monitor/services/update"),
	// C1-03：forward/force-delete 的管理员门在 handler 内
	// （forwardForceDelete 对 roleID!=0 回 403），与 monitoring
	// 同形态归 gateHandler 管理员面——旧表误登记为普通用户面，
	// 与 handler 实况矛盾。
	admH("/api/v1/forward/force-delete"),

	// ---- 普通用户：登录 + handler 本人/归属门 ----
	usr("/api/v1/announcement/get"),
	usr("/api/v1/config/list"),
	usr("/api/v1/forward/batch-change-tunnel"), usr("/api/v1/forward/batch-delete"),
	usr("/api/v1/forward/batch-pause"), usr("/api/v1/forward/batch-redeploy"),
	usr("/api/v1/forward/batch-resume"), usr("/api/v1/forward/create"),
	usr("/api/v1/forward/delete"), usr("/api/v1/forward/diagnose"),
	usr("/api/v1/forward/diagnose/stream"),
	usr("/api/v1/forward/list"), usr("/api/v1/forward/pause"),
	usr("/api/v1/forward/reset-flow"), usr("/api/v1/forward/resume"),
	usr("/api/v1/forward/update"), usr("/api/v1/forward/update-order"),
	usr("/api/v1/inbound/sub/info"),
	usr("/api/v1/monitor/access"),
	usr("/api/v1/monitor/tunnels"), usr("/api/v1/monitor/tunnels/"), usr("/api/v1/monitor/tunnels/quality"),
	usr("/api/v1/tunnel/user/tunnel"),
	usr("/api/v1/user/package"),
	usr("/api/v1/user/subscription/traffic"), usr("/api/v1/user/updatePassword"),
	usr("/api/v1/user/logout"),
	usr("/api/v1/system-info/ticket"),
	usr("/api/v1/user/passkey/list"), usr("/api/v1/user/passkey/delete"),
	usr("/api/v1/user/passkey/register/begin"), usr("/api/v1/user/passkey/register/finish"),
	usr("/api/v1/user-inbound/mine"),
	usr("/api/v1/user-subscription/mine"),
}

// regCallSite 是整树普查到的一处 .Handle/.HandleFunc 调用点
// （AST 口径，R8-023）：注释与字符串内容不是 AST 节点，永不
// 构成调用点。
type regCallSite struct {
	relFile string // 相对 backend 根的路径（slash 分隔）
	line    int
	arg     string // 首参原文（空白归一化）；参数不足两个时为空串
	literal bool   // 首参是否为纯字符串字面量
	pattern string // literal 时的字面量值
	recv    string // 接收者为简单标识符时的名字（如 mux），否则为空
	method  string // Handle 或 HandleFunc
}

const (
	handlerRelFile  = "internal/http/handler/handler.go"
	ghMirrorRelFile = "internal/http/handler/gh_mirror.go"
	routerRelFile   = "internal/http/router.go"
)

// parseGoFileAST 解析一个 Go 源文件为 AST（不载注释：注释永不
// 成为节点，这正是 R8-023 的真值源口径）。
func parseGoFileAST(path string) (*token.FileSet, *ast.File, []byte, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, nil, err
	}
	return fset, file, src, nil
}

// collectCallSites 整树普查 root 下全部非测试 .go 文件的
// .Handle/.HandleFunc 调用点（R7-L8-01 起整树口径，R8-023 起
// AST 口径）：不预设注册只发生在哪两个文件——任何文件里的真
// 调用点都进入结果，文件面规则（清扫）与白名单逐一相等都在这
// 份全集上判定。测试文件除外：测试自建的 mux 从不接生产
// router（handler 包测试以自有 mux 调 Register 的情形，其注册
// 点仍是 handler.go 的生产调用点，已在普查内）。
func collectCallSites(root string) ([]regCallSite, error) {
	var sites []regCallSite
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		fset, file, src, perr := parseGoFileAST(path)
		if perr != nil {
			return fmt.Errorf("census: parse %s: %w", path, perr)
		}
		rel = filepath.ToSlash(rel)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
				return true
			}
			site := regCallSite{
				relFile: rel,
				line:    fset.Position(call.Pos()).Line,
				method:  sel.Sel.Name,
			}
			if id, ok := sel.X.(*ast.Ident); ok {
				site.recv = id.Name
			}
			if len(call.Args) >= 2 {
				argExpr := call.Args[0]
				if lit, ok := argExpr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					site.literal = true
					site.arg = lit.Value
					if v, uerr := strconv.Unquote(lit.Value); uerr == nil {
						site.pattern = v
					} else {
						site.pattern = strings.Trim(lit.Value, "\"`")
					}
				} else {
					start := fset.Position(argExpr.Pos()).Offset
					end := fset.Position(argExpr.End()).Offset
					site.arg = strings.Join(strings.Fields(string(src[start:end])), " ")
				}
			}
			sites = append(sites, site)
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sites, nil
}

// backupEntriesFromAST 从 handler.go 的 AST 提取备份端点清单
// 条目：复合字面量元素（结构体字面量）的首字段为
// /api/v1/backup/ 前缀字符串者。注释里的清单条目文本不在
// AST 中，永不命中（R8-023 的 M4 形态）。
func backupEntriesFromAST(file *ast.File) []string {
	var entries []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, elt := range lit.Elts {
			inner, ok := elt.(*ast.CompositeLit)
			if !ok || len(inner.Elts) == 0 {
				continue
			}
			first, ok := inner.Elts[0].(*ast.BasicLit)
			if !ok || first.Kind != token.STRING {
				continue
			}
			if v, err := strconv.Unquote(first.Value); err == nil && strings.HasPrefix(v, "/api/v1/backup/") {
				entries = append(entries, v)
			}
		}
		return true
	})
	return entries
}

// constStringValue 取文件中名为 name 的字符串常量值。
func constStringValue(file *ast.File, name string) (string, bool) {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, n := range vs.Names {
				if n.Name != name || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if v, err := strconv.Unquote(lit.Value); err == nil {
						return v, true
					}
				}
			}
		}
	}
	return "", false
}

// censusOutcome 是一次普查的全部产物：注册集、调用点全集、
// 门违规清单与提取口径损坏（fatal）。
type censusOutcome struct {
	routes map[string]bool
	sites  []regCallSite
	errs   []string
	fatal  string
}

// runCensus 在 root（backend 根）上机械枚举全量注册路由，并
// 清扫注册面只许出现在 handler.go 与 router.go（新文件注册
// 路由即红）。全部判定走 AST，注释形态永不构成注册（R8-023)。
func runCensus(root string) censusOutcome {
	out := censusOutcome{routes: map[string]bool{}}
	sites, err := collectCallSites(root)
	if err != nil {
		out.fatal = err.Error()
		return out
	}
	out.sites = sites

	// 1) handler.go / router.go 中接收者为 mux 的字面量注册。
	for _, s := range sites {
		if s.literal && s.recv == "mux" && (s.relFile == handlerRelFile || s.relFile == routerRelFile) {
			out.routes[s.pattern] = true
		}
	}

	_, handlerAST, _, err := parseGoFileAST(filepath.Join(root, filepath.FromSlash(handlerRelFile)))
	if err != nil {
		out.fatal = fmt.Sprintf("census: parse handler.go: %v", err)
		return out
	}
	// 2) 备份端点循环：清单内每条以原前缀与 /api/v1 双前缀各注册一次。
	backupEntries := backupEntriesFromAST(handlerAST)
	if len(backupEntries) == 0 {
		out.fatal = "census: no backup route entries found in handler.go (extraction caliber broken)"
		return out
	}
	for _, e := range backupEntries {
		out.routes[e] = true
		out.routes["/api/v1"+e] = true
	}
	// 3) ghMirrorPath 常量注册。
	_, ghAST, _, err := parseGoFileAST(filepath.Join(root, filepath.FromSlash(ghMirrorRelFile)))
	if err != nil {
		out.fatal = fmt.Sprintf("census: parse gh_mirror.go: %v", err)
		return out
	}
	gh, ok := constStringValue(ghAST, "ghMirrorPath")
	if !ok {
		out.fatal = "census: ghMirrorPath constant not found in gh_mirror.go"
		return out
	}
	ghRegistered := false
	for _, s := range sites {
		if s.relFile == handlerRelFile && s.recv == "mux" && s.method == "HandleFunc" && s.arg == "ghMirrorPath" {
			ghRegistered = true
		}
	}
	if !ghRegistered {
		out.fatal = "census: handler.go no longer registers ghMirrorPath"
		return out
	}
	out.routes[gh] = true

	// 4) 注册面清扫：整树调用点里带字面量路径的只许出现在
	//    handler.go 与 router.go，且清扫所得字面量集必须被上面
	//    的枚举覆盖（防换个接收者名字在别处注册绕过普查）。
	//    非字面量调用点不在此判，由白名单逐一相等兜底——同为整
	//    树扫描，第三文件两类注册都无处遁形。
	allowed := map[string]bool{
		handlerRelFile: true,
		routerRelFile:  true,
	}
	swept := map[string]bool{}
	for _, site := range sites {
		if !site.literal {
			continue
		}
		if !allowed[site.relFile] {
			out.errs = append(out.errs, fmt.Sprintf(
				"census: route registration surface outside handler.go/router.go: %s:%d", site.relFile, site.line))
			continue
		}
		swept[site.pattern] = true
	}
	var uncovered []string
	for p := range swept {
		if p == "/api/v1" {
			// 备份循环的拼接前缀，非独立路由（L1 口径）。
			continue
		}
		if !out.routes[p] {
			uncovered = append(uncovered, p)
		}
	}
	sort.Strings(uncovered)
	for _, p := range uncovered {
		out.errs = append(out.errs, fmt.Sprintf("census: swept registration %q not covered by enumeration", p))
	}
	return out
}

// nonLiteralRegSite 是一处「首参非纯字符串字面量」的注册调用点
// 登记：file 为相对 backend 根的路径，arg 为首参表达式（空白
// 归一化后），note 说明该调用点注册的路径如何被 runCensus 的
// 特例展开覆盖。
type nonLiteralRegSite struct {
	file string
	arg  string
	note string
}

// nonLiteralRegistrationWhitelist 是全后端全部非常量注册调用点
// 的显式白名单（RB3-03，R7-L8-01 起整树口径）。新增此类注册时：
// 先在此登记（含覆盖口径说明），再让 runCensus 的特例展开覆盖
// 其路径、并在 routeClassification 里归类。
var nonLiteralRegistrationWhitelist = []nonLiteralRegSite{
	{
		"internal/http/handler/handler.go", "route.path",
		"备份端点循环注册：路径取自同一清单条目，runCensus 第 2 步以 AST 展开清单（原前缀＋/api/v1 双前缀）覆盖其全部路径",
	},
	{
		"internal/http/handler/handler.go", `"/api/v1"+route.path`,
		"备份端点循环的双前缀别名注册：与上一处同一清单、同一展开口径",
	},
	{
		"internal/http/handler/handler.go", "ghMirrorPath",
		"GitHub 镜像代理注册：路径为 gh_mirror.go 的 ghMirrorPath 常量，runCensus 第 3 步提取常量值覆盖",
	},
}

// whitelistViolations 返回调用点全集与白名单逐一相等的全部
// 违规（多一处/少一处/白名单自重都点名）。
func whitelistViolations(sites []regCallSite) []string {
	found := map[string]string{} // key: file + "\x00" + arg → 定位 file:line
	for _, site := range sites {
		if site.arg == "" || site.literal {
			continue
		}
		found[site.relFile+"\x00"+site.arg] = site.relFile + ":" + strconv.Itoa(site.line)
	}

	allowed := map[string]nonLiteralRegSite{}
	var violations []string
	for _, s := range nonLiteralRegistrationWhitelist {
		key := s.file + "\x00" + s.arg
		if _, dup := allowed[key]; dup {
			violations = append(violations, fmt.Sprintf(
				"non-literal registration whitelist has duplicate entry %q in %s", s.arg, s.file))
		}
		allowed[key] = s
	}
	var keys []string
	for key := range found {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := allowed[key]; !ok {
			arg := strings.SplitN(key, "\x00", 2)[1]
			violations = append(violations, fmt.Sprintf(
				"non-literal route registration at %s (first arg %s) is NOT in nonLiteralRegistrationWhitelist — "+
					"register it there with a note on how its paths are enumerated, or use a plain string literal (RB3-03 gate)",
				found[key], arg))
		}
	}
	for _, s := range nonLiteralRegistrationWhitelist {
		key := s.file + "\x00" + s.arg
		if _, ok := found[key]; !ok {
			violations = append(violations, fmt.Sprintf(
				"non-literal registration whitelist entry %q in %s no longer matches any call site — remove it (RB3-03 gate)",
				s.arg, s.file))
		}
	}
	return violations
}

// classificationDiff 返回注册集与分类表的双向差集违规（点名
// 逐条，排序确定性）。
func classificationDiff(routes map[string]bool, table []routeEntry) []string {
	classified := map[string]bool{}
	for _, e := range table {
		classified[e.path] = true
	}
	var unclassified, stale []string
	for p := range routes {
		if !classified[p] {
			unclassified = append(unclassified, p)
		}
	}
	for p := range classified {
		if !routes[p] {
			stale = append(stale, p)
		}
	}
	sort.Strings(unclassified)
	sort.Strings(stale)
	var violations []string
	for _, p := range unclassified {
		violations = append(violations, fmt.Sprintf(
			"registered route %q has NO classification — classify it in routeClassification (L8-F-06 gate)", p))
	}
	for _, p := range stale {
		violations = append(violations, fmt.Sprintf(
			"classified route %q is no longer registered — remove or fix its classification", p))
	}
	return violations
}

// gateViolations 在给定 root 与分类表上跑齐门的三道判定
// （普查清扫＋白名单逐一相等＋分类差集），供常设变异红证在
// 合成树上复用同一判定逻辑。
func gateViolations(root string, table []routeEntry) []string {
	out := runCensus(root)
	var v []string
	if out.fatal != "" {
		v = append(v, out.fatal)
	}
	v = append(v, out.errs...)
	v = append(v, whitelistViolations(out.sites)...)
	v = append(v, classificationDiff(out.routes, table)...)
	return v
}

// TestRouteRegistrationNonLiteralWhitelist 机械扫描全后端整树
// 的全部 .Handle/.HandleFunc 调用点（R7-L8-01 起整树口径，
// R8-023 起 AST 口径，原先只开 handler.go/router.go 两文件，
// 第三文件的非字面量注册全盲）：首参非纯字符串字面量的集合
// 必须与 nonLiteralRegistrationWhitelist 逐一相等——多一处
// （新非常量注册未登记）或少一处（登记已失效）都点名报红
// （RB3-03)。
func TestRouteRegistrationNonLiteralWhitelist(t *testing.T) {
	backendRoot := filepath.Join("..", "..", "..")
	out := runCensus(backendRoot)
	if out.fatal != "" {
		t.Fatal(out.fatal)
	}
	for _, msg := range whitelistViolations(out.sites) {
		t.Error(msg)
	}
}

func TestRouteClassificationCensus(t *testing.T) {
	backendRoot := filepath.Join("..", "..", "..")
	out := runCensus(backendRoot)
	if out.fatal != "" {
		t.Fatal(out.fatal)
	}
	for _, msg := range out.errs {
		t.Error(msg)
	}
	registered := out.routes

	// 分类表自身卫生：无重复、无空类。
	classified := map[string]routeEntry{}
	for _, e := range routeClassification {
		if e.class != classAdmin && e.class != classUser && e.class != classPublic {
			t.Errorf("route %q has no explicit class", e.path)
		}
		if _, dup := classified[e.path]; dup {
			t.Errorf("route %q classified twice", e.path)
		}
		classified[e.path] = e
	}

	// 完备性：注册集与分类集必须逐条相等（差集双向点名）。
	for _, msg := range classificationDiff(registered, routeClassification) {
		t.Error(msg)
	}

	// 三表对位：每条分类必须与中间件实际判定一致。
	counts := map[routeClass]int{}
	for _, e := range routeClassification {
		counts[e.class]++
		switch e.gate {
		case gateMiddleware:
			if e.class != classAdmin {
				t.Errorf("route %q: gateMiddleware entries must be classAdmin", e.path)
			}
			if !requiresAdmin(e.path) {
				t.Errorf("route %q classified admin via requiresAdmin, but requiresAdmin() = false", e.path)
			}
			if shouldSkip(e.path) {
				t.Errorf("route %q classified admin via requiresAdmin, but shouldSkip() = true", e.path)
			}
		case gateHandler:
			if e.class != classAdmin {
				t.Errorf("route %q: gateHandler entries must be classAdmin", e.path)
			}
			if requiresAdmin(e.path) || shouldSkip(e.path) {
				t.Errorf("route %q classified admin via handler gate, but middleware disagrees (requiresAdmin=%v shouldSkip=%v)",
					e.path, requiresAdmin(e.path), shouldSkip(e.path))
			}
		case gateLogin:
			if e.class != classUser {
				t.Errorf("route %q: gateLogin entries must be classUser", e.path)
			}
			if requiresAdmin(e.path) || shouldSkip(e.path) {
				t.Errorf("route %q classified as normal-user, but middleware disagrees (requiresAdmin=%v shouldSkip=%v)",
					e.path, requiresAdmin(e.path), shouldSkip(e.path))
			}
		case gateSkip:
			if e.class != classPublic {
				t.Errorf("route %q: gateSkip entries must be classPublic", e.path)
			}
			if !shouldSkip(e.path) {
				t.Errorf("route %q classified public via shouldSkip, but shouldSkip() = false", e.path)
			}
		case gateOptional:
			if e.class != classPublic {
				t.Errorf("route %q: gateOptional entries must be classPublic", e.path)
			}
			if !allowsOptionalAuth(e.path) || shouldSkip(e.path) {
				t.Errorf("route %q classified public via optional auth, but middleware disagrees", e.path)
			}
		case gateNonAPI:
			if e.class != classPublic {
				t.Errorf("route %q: gateNonAPI entries must be classPublic", e.path)
			}
			if strings.HasPrefix(e.path, "/api/") {
				t.Errorf("route %q classified as non-API surface but lives under /api/", e.path)
			}
		default:
			t.Errorf("route %q has no explicit gate", e.path)
		}
	}
	t.Logf("route census: %d registered, %d classified (管理员 %d / 普通用户 %d / 公开面 %d)",
		len(registered), len(routeClassification), counts[classAdmin], counts[classUser], counts[classPublic])
}

// ---- 常设变异红证（R8-023）：合成后端树 ----

const syntheticHandlerGo = `package handler

import "net/http"

type probeHandler struct{}

func (probeHandler) a(http.ResponseWriter, *http.Request) {}

func (h probeHandler) Register(mux *http.ServeMux) {
    mux.HandleFunc("/api/v1/probe/keep", h.a)
    mux.HandleFunc("/api/v1/captcha/verify", h.a)
    for _, route := range []struct {
        path string
        fn   http.HandlerFunc
    }{
        {"/api/v1/backup/export", h.a},
        {"/api/v1/backup/import", h.a},
    } {
        mux.HandleFunc(route.path, route.fn)
        mux.HandleFunc("/api/v1"+route.path, route.fn)
    }
    mux.HandleFunc(ghMirrorPath, h.a)
}
`

const syntheticGHMirrorGo = "package handler\n\nconst ghMirrorPath = \"/api/v1/open_api/gh/\"\n"

const syntheticRouterGo = `package httpserver

import "net/http"

func registerProbe(mux *http.ServeMux, h http.Handler) {
    mux.Handle("/system-info", h)
}
`

const syntheticProbeM1Go = `package handler

import "net/http"

func (h probeHandler) registerProbeM1(mux *http.ServeMux) {
    mux.HandleFunc("/api/v1/probe/m1", h.a)
}
`

const syntheticProbeM2Go = `package handler

import "net/http"

const probeM2Path = "/api/v1/probe/m2"

func (h probeHandler) registerProbeM2(mux *http.ServeMux) {
    mux.HandleFunc(probeM2Path, h.a)
}
`

const syntheticProbeM3Go = `
func (h probeHandler) registerProbeM3() {
    s2 := http.NewServeMux()
    s2.HandleFunc("/api/v1/probe/m3", h.a)
    _ = s2
}
`

const syntheticProbeM5Go = `
func (h probeHandler) registerProbeM5(mux *http.ServeMux) {
    mux.HandleFunc("GET /api/v1/probe/m5", h.a)
}
`

func writeSyntheticBackendTree(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"internal/http/handler/handler.go":   syntheticHandlerGo,
		"internal/http/handler/gh_mirror.go": syntheticGHMirrorGo,
		"internal/http/router.go":            syntheticRouterGo,
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeTreeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mutateTreeFile 把 rel 文件中第一处 old 替换为 new 并返回变异
// 后的全文（供幽灵文本仍在的断言）。
func mutateTreeFile(t *testing.T, root, rel, old, next string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if !strings.Contains(src, old) {
		t.Fatalf("mutation anchor %q not found in %s", old, rel)
	}
	src = strings.Replace(src, old, next, 1)
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

func appendTreeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append(data, content...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRouteClassificationGateMutations 是分类门的常设变异红证
// （R8-023）：在合成后端树上先证基线全绿（8 条合成路由、三处
// 白名单调用点），再逐一施加 L1 的变异集——每一变异都必须让
// 同一判定逻辑（gateViolations）报红并点名探针。M4/M4b/M4c
// 是注释幽灵三形态：变异后注册文本仍逐字留在文件里，但 AST
// 枚举必须不再计入该路由（文本正则时代三者全部漏红，即
// L1-01 本体）。
func TestRouteClassificationGateMutations(t *testing.T) {
	wantRoutes := []string{
		"/api/v1/probe/keep", "/api/v1/captcha/verify",
		"/api/v1/backup/export", "/api/v1/api/v1/backup/export",
		"/api/v1/backup/import", "/api/v1/api/v1/backup/import",
		"/api/v1/open_api/gh/", "/system-info",
	}
	var table []routeEntry
	for _, p := range wantRoutes {
		table = append(table, routeEntry{path: p})
	}

	base := t.TempDir()
	writeSyntheticBackendTree(t, base)
	if v := gateViolations(base, table); len(v) != 0 {
		t.Fatalf("synthetic baseline must pass the gate, got violations: %v", v)
	}
	baseOut := runCensus(base)
	if baseOut.fatal != "" {
		t.Fatalf("synthetic baseline census fatal: %s", baseOut.fatal)
	}
	if len(baseOut.routes) != len(wantRoutes) {
		t.Fatalf("synthetic baseline routes = %v, want %v", baseOut.routes, wantRoutes)
	}
	for _, p := range wantRoutes {
		if !baseOut.routes[p] {
			t.Errorf("synthetic baseline missing route %q", p)
		}
	}

	const handlerRel = "internal/http/handler/handler.go"
	cases := []struct {
		name        string
		apply       func(t *testing.T, root string) string // 返回变异后 handler.go 全文（无则空串）
		wantText    string                                 // 违规清单必须点名的探针文本
		absentRoute string                                 // 枚举必须不再包含的路由（注释幽灵形态）
		ghostText   string                                 // 变异后仍须逐字留在 handler.go 里的幽灵文本
	}{
		{
			name: "M1 literal registration in a third file",
			apply: func(t *testing.T, root string) string {
				writeTreeFile(t, root, "internal/http/handler/zz_probe_m1.go", syntheticProbeM1Go)
				return ""
			},
			wantText: "zz_probe_m1.go",
		},
		{
			name: "M2 non-literal registration in a third file",
			apply: func(t *testing.T, root string) string {
				writeTreeFile(t, root, "internal/http/handler/zz_probe_m2.go", syntheticProbeM2Go)
				return ""
			},
			wantText: "probeM2Path",
		},
		{
			name: "M3 renamed receiver in handler.go",
			apply: func(t *testing.T, root string) string {
				appendTreeFile(t, root, handlerRel, syntheticProbeM3Go)
				return ""
			},
			wantText: "/api/v1/probe/m3",
		},
		{
			name: "M4 backup entry commented out",
			apply: func(t *testing.T, root string) string {
				return mutateTreeFile(t, root, handlerRel,
					`{"/api/v1/backup/import", h.a},`,
					`// {"/api/v1/backup/import", h.a},`)
			},
			wantText:    "/api/v1/backup/import",
			absentRoute: "/api/v1/backup/import",
			ghostText:   `// {"/api/v1/backup/import", h.a},`,
		},
		{
			name: "M4b registration line commented out",
			apply: func(t *testing.T, root string) string {
				return mutateTreeFile(t, root, handlerRel,
					`mux.HandleFunc("/api/v1/captcha/verify", h.a)`,
					`// mux.HandleFunc("/api/v1/captcha/verify", h.a)`)
			},
			wantText:    "/api/v1/captcha/verify",
			absentRoute: "/api/v1/captcha/verify",
			ghostText:   `// mux.HandleFunc("/api/v1/captcha/verify", h.a)`,
		},
		{
			name: "M4c registration wrapped in block comment",
			apply: func(t *testing.T, root string) string {
				return mutateTreeFile(t, root, handlerRel,
					`mux.HandleFunc("/api/v1/probe/keep", h.a)`,
					`/* mux.HandleFunc("/api/v1/probe/keep", h.a) */`)
			},
			wantText:    "/api/v1/probe/keep",
			absentRoute: "/api/v1/probe/keep",
			ghostText:   `/* mux.HandleFunc("/api/v1/probe/keep", h.a) */`,
		},
		{
			name: "M5 method-prefixed pattern registration",
			apply: func(t *testing.T, root string) string {
				appendTreeFile(t, root, handlerRel, syntheticProbeM5Go)
				return ""
			},
			wantText: "GET /api/v1/probe/m5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeSyntheticBackendTree(t, root)
			mutated := tc.apply(t, root)
			if tc.ghostText != "" && !strings.Contains(mutated, tc.ghostText) {
				t.Fatalf("mutation %s: ghost text %q not present in mutated handler.go", tc.name, tc.ghostText)
			}
			out := runCensus(root)
			if out.fatal != "" {
				t.Fatalf("mutation %s: unexpected census fatal: %s", tc.name, out.fatal)
			}
			if tc.absentRoute != "" && out.routes[tc.absentRoute] {
				t.Errorf("mutation %s: route %q still enumerated — comment ghost survived", tc.name, tc.absentRoute)
			}
			violations := gateViolations(root, table)
			if !strings.Contains(strings.Join(violations, "\n"), tc.wantText) {
				t.Errorf("mutation %s must turn the gate red naming %q; violations: %v", tc.name, tc.wantText, violations)
			}
		})
	}
}
