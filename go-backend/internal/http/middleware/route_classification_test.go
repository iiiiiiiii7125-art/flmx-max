package middleware

import (
	"os"
	"path/filepath"
	"regexp"
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
//      第 1 步的枚举与清扫正则都只认字面量，此类注册对它们不
//      可见，只能靠白名单逐一相等兜底，多一处/少一处即红。
//
// R7-L8-01（RB3-03 残留）：第 1 步清扫与第 4 步白名单扫描原先
// 只覆盖 handler.go 与 router.go——第三个文件里的非字面量注册
// 对枚举/清扫/白名单全不可见（字面量注册则会被清扫的文件面规
// 则逮住）。现两道扫描都改走 scanRouteRegistrationCallSites 的
// 全后端整树调用点普查（任何文件的 .Handle/.HandleFunc 调用点
// 无一例外进入扫描，字面量与非字面量同覆盖），白名单键改用
// 相对 backend 根的路径，第三文件新增任何注册（字面量撞文件
// 面规则、非字面量撞白名单逐一相等）都点名报红。
//
// 分类口径与 L1 的 211 条机械清单对齐（L8 报告 §④的「~180 条」
// 是 handler.go Register 段的约数；差额逐项为：handler.go 字面
// 量 189 条 + 备份循环双前缀展开 20 条 + ghMirrorPath 常量 1 条
// + router.go 直注册 /system-info 1 条 = 211)。L1 的四分法（免
// 检 20 + 可选鉴权 1 + 中间件管理员 144 + 登录＋handler 门 46）
// 到本表三分法的映射：免检 20 → 公开面（gateSkip)；可选鉴权 1
// （/api/v1/config/get）→ 公开面（gateOptional)；中间件管理员
// 144 → 管理员（gateMiddleware)；登录＋handler 门 46 中，
// monitor 的 nodes/services 共 10 条是 handler 内 ensureAdmin
// Access 的真管理员面 → 管理员（gateHandler)，/error 与
// /system-info 是非 /api/ 自理面 → 公开面（gateNonAPI)，其余
// 34 条 → 普通用户（gateLogin)。

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

// routeClassification 是全量注册路由的显式分类表（216 条）。
// 新增路由时：先在 handler.go/router.go 注册，再来这里加一行
// 分类；改某条路由的鉴权语义时同步改这里并让三表对位检查裁决。
var routeClassification = []routeEntry{
	// ---- 公开面：shouldSkip 免检（凭据自理或设计公开）----
	pub("/flow/config"), pub("/flow/test"), pub("/flow/upload"),
	pub("/api/v1/open_api/gh/"), pub("/api/v1/open_api/sub/"), pub("/api/v1/open_api/sub_store"),
	pub("/api/v1/captcha/check"), pub("/api/v1/captcha/verify"),
	pub("/api/v1/user/login"),
	pub("/api/v1/user/passkey/status"), pub("/api/v1/user/passkey/login/begin"), pub("/api/v1/user/passkey/login/finish"),
	pub("/api/v1/public/config/get"),
	pub("/api/v1/federation/connect"), pub("/api/v1/federation/tunnel/create"),
	pub("/api/v1/federation/runtime/apply-role"), pub("/api/v1/federation/runtime/command"),
	pub("/api/v1/federation/runtime/diagnose"), pub("/api/v1/federation/runtime/release-role"),
	pub("/api/v1/federation/runtime/reserve-port"),
	// ---- 公开面：可选鉴权（无 token 可达，策略门在 handler 内）----
	pubOpt("/api/v1/config/get"),
	// ---- 公开面：非 /api/ 面（JWT 不覆盖）----
	pubNA("/error"), pubNA("/system-info"),

	// ---- 管理员：requiresAdmin 前缀/精确清单 ----
	adm("/api/v1/announcement/update"),
	adm("/api/v1/api/v1/backup/export"), adm("/api/v1/api/v1/backup/gist/restore"),
	adm("/api/v1/api/v1/backup/gist/save"), adm("/api/v1/api/v1/backup/gist/status"),
	adm("/api/v1/api/v1/backup/gist/upload"), adm("/api/v1/api/v1/backup/gist/versions"),
	adm("/api/v1/api/v1/backup/import"), adm("/api/v1/api/v1/backup/import-auto-backup"),
	adm("/api/v1/api/v1/backup/import-auto-backup/status"), adm("/api/v1/api/v1/backup/restore"),
	adm("/api/v1/backup/export"), adm("/api/v1/backup/gist/restore"),
	adm("/api/v1/backup/gist/save"), adm("/api/v1/backup/gist/status"),
	adm("/api/v1/backup/gist/upload"), adm("/api/v1/backup/gist/versions"),
	adm("/api/v1/backup/import"), adm("/api/v1/backup/import-auto-backup"),
	adm("/api/v1/backup/import-auto-backup/status"), adm("/api/v1/backup/restore"),
	adm("/api/v1/config/update"), adm("/api/v1/config/update-single"),
	adm("/api/v1/federation/node/import"),
	adm("/api/v1/federation/share/create"), adm("/api/v1/federation/share/delete"),
	adm("/api/v1/federation/share/list"), adm("/api/v1/federation/share/remote-usage/list"),
	adm("/api/v1/federation/share/reset-flow"), adm("/api/v1/federation/share/update"),
	adm("/api/v1/group/permission/assign"), adm("/api/v1/group/permission/list"), adm("/api/v1/group/permission/remove"),
	adm("/api/v1/group/tunnel/assign"), adm("/api/v1/group/tunnel/create"), adm("/api/v1/group/tunnel/delete"),
	adm("/api/v1/group/tunnel/list"), adm("/api/v1/group/tunnel/update"),
	adm("/api/v1/group/user/assign"), adm("/api/v1/group/user/create"), adm("/api/v1/group/user/delete"),
	adm("/api/v1/group/user/list"), adm("/api/v1/group/user/update"),
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
	adm("/api/v1/node/batch-delete"), adm("/api/v1/node/batch-upgrade"),
	adm("/api/v1/node/check-status"), adm("/api/v1/node/create"),
	adm("/api/v1/node/delete"), adm("/api/v1/node/dismiss-expiry-reminder"),
	adm("/api/v1/node/install"), adm("/api/v1/node/latency/history"),
	adm("/api/v1/node/latency/probe"), adm("/api/v1/node/latency/targets"),
	adm("/api/v1/node/list"), adm("/api/v1/node/nftables/clear"),
	adm("/api/v1/node/nftables/pending-deletes"), adm("/api/v1/node/nftables/reconcile"),
	adm("/api/v1/node/nftables/test"), adm("/api/v1/node/releases"),
	adm("/api/v1/node/rollback"), adm("/api/v1/node/route"),
	adm("/api/v1/node/route/probe"), adm("/api/v1/node/traffic/month"),
	adm("/api/v1/node/tune"), adm("/api/v1/node/update"),
	adm("/api/v1/node/update-order"), adm("/api/v1/node/upgrade"),
	adm("/api/v1/notify/test"),
	adm("/api/v1/speed-limit/create"), adm("/api/v1/speed-limit/delete"),
	adm("/api/v1/speed-limit/list"), adm("/api/v1/speed-limit/update"),
	adm("/api/v1/subscription/delete"),
	adm("/api/v1/subscription/group/delete"), adm("/api/v1/subscription/group/list"),
	adm("/api/v1/subscription/group/order"), adm("/api/v1/subscription/group/preview"),
	adm("/api/v1/subscription/group/save"), adm("/api/v1/subscription/list"),
	adm("/api/v1/subscription/preview"), adm("/api/v1/subscription/reset"),
	adm("/api/v1/subscription/reset-traffic"), adm("/api/v1/subscription/save"),
	adm("/api/v1/subscription/source/delete"), adm("/api/v1/subscription/source/list"),
	adm("/api/v1/subscription/source/refresh"), adm("/api/v1/subscription/source/save"),
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

	// ---- 普通用户：登录 + handler 本人/归属门 ----
	usr("/api/v1/announcement/get"),
	usr("/api/v1/config/list"),
	usr("/api/v1/forward/batch-change-tunnel"), usr("/api/v1/forward/batch-delete"),
	usr("/api/v1/forward/batch-pause"), usr("/api/v1/forward/batch-redeploy"),
	usr("/api/v1/forward/batch-resume"), usr("/api/v1/forward/create"),
	usr("/api/v1/forward/delete"), usr("/api/v1/forward/diagnose"),
	usr("/api/v1/forward/diagnose/stream"), usr("/api/v1/forward/force-delete"),
	usr("/api/v1/forward/list"), usr("/api/v1/forward/pause"),
	usr("/api/v1/forward/reset-flow"), usr("/api/v1/forward/resume"),
	usr("/api/v1/forward/update"), usr("/api/v1/forward/update-order"),
	usr("/api/v1/inbound/sub/info"),
	usr("/api/v1/monitor/access"),
	usr("/api/v1/monitor/tunnels"), usr("/api/v1/monitor/tunnels/"), usr("/api/v1/monitor/tunnels/quality"),
	usr("/api/v1/tunnel/user/tunnel"),
	usr("/api/v1/user/groups"), usr("/api/v1/user/package"),
	usr("/api/v1/user/subscription/traffic"), usr("/api/v1/user/updatePassword"),
	usr("/api/v1/user/passkey/list"), usr("/api/v1/user/passkey/delete"),
	usr("/api/v1/user/passkey/register/begin"), usr("/api/v1/user/passkey/register/finish"),
	usr("/api/v1/user-inbound/mine"),
	usr("/api/v1/user-subscription/mine"),
}

var (
	reMuxLiteral    = regexp.MustCompile(`mux\.HandleFunc\("([^"]+)",`)
	reMuxHandle     = regexp.MustCompile(`mux\.Handle\("([^"]+)",`)
	reBackupEntry   = regexp.MustCompile(`\{"(/api/v1/backup/[^"]+)",`)
	reGHMirrorConst = regexp.MustCompile(`ghMirrorPath\s*=\s*"([^"]+)"`)
)

// enumerateRegisteredRoutes 机械枚举全量注册路由，并清扫注册面
// 只许出现在 handler.go 与 router.go（新文件注册路由即红）。
func enumerateRegisteredRoutes(t *testing.T) map[string]bool {
	t.Helper()
	backendRoot := filepath.Join("..", "..", "..")
	handlerPath := filepath.Join(backendRoot, "internal", "http", "handler", "handler.go")
	ghMirrorPath := filepath.Join(backendRoot, "internal", "http", "handler", "gh_mirror.go")
	routerPath := filepath.Join(backendRoot, "internal", "http", "router.go")

	handlerSrc := readFileForCensus(t, handlerPath)
	ghSrc := readFileForCensus(t, ghMirrorPath)
	routerSrc := readFileForCensus(t, routerPath)

	routes := map[string]bool{}
	// 1) handler.go 字面量注册（拼接行 "/api/v1"+route.path 因引号
	//    后不是逗号不会命中，备份循环单独展开）。
	for _, m := range reMuxLiteral.FindAllStringSubmatch(handlerSrc, -1) {
		routes[m[1]] = true
	}
	// 2) 备份端点循环：清单内每条以原前缀与 /api/v1 双前缀各注册一次。
	backupEntries := reBackupEntry.FindAllStringSubmatch(handlerSrc, -1)
	if len(backupEntries) == 0 {
		t.Fatal("census: no backup route entries found in handler.go (extraction caliber broken)")
	}
	for _, m := range backupEntries {
		routes[m[1]] = true
		routes["/api/v1"+m[1]] = true
	}
	// 3) ghMirrorPath 常量注册。
	gh := reGHMirrorConst.FindStringSubmatch(ghSrc)
	if gh == nil {
		t.Fatal("census: ghMirrorPath constant not found in gh_mirror.go")
	}
	if !strings.Contains(handlerSrc, "mux.HandleFunc(ghMirrorPath,") {
		t.Fatal("census: handler.go no longer registers ghMirrorPath")
	}
	routes[gh[1]] = true
	// 4) router.go 直注册。
	for _, m := range reMuxHandle.FindAllStringSubmatch(routerSrc, -1) {
		routes[m[1]] = true
	}

	// 5) 注册面清扫：全后端整树调用点普查（scanRouteRegistration
	//    CallSites，非测试文件）里带字面量路径的 .Handle/.HandleFunc
	//    只许出现在 handler.go 与 router.go，且清扫所得字面量集
	//    必须被上面的枚举覆盖（防换个接收者名字在别处注册绕过
	//    普查）。非字面量调用点不在此判，由白名单测试逐一相等
	//    兜底——同为整树扫描，第三文件两类注册都无处遁形。
	allowed := map[string]bool{
		"internal/http/handler/handler.go": true,
		"internal/http/router.go":          true,
	}
	swept := map[string]bool{}
	for _, site := range scanRouteRegistrationCallSites(t, backendRoot) {
		if !site.literal {
			continue
		}
		if !allowed[site.relFile] {
			t.Errorf("census: route registration surface outside handler.go/router.go: %s:%d", site.relFile, site.line)
			continue
		}
		swept[site.pattern] = true
	}
	for p := range swept {
		if p == "/api/v1" {
			// 备份循环的拼接前缀，非独立路由（L1 口径）。
			continue
		}
		if !routes[p] {
			t.Errorf("census: swept registration %q not covered by enumeration", p)
		}
	}
	return routes
}

func readFileForCensus(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("census: read %s: %v", path, err)
	}
	return string(data)
}

var reHandleCallStart = regexp.MustCompile(`\.Handle(?:Func)?\(`)

// regCallSite 是整树普查到的一处 .Handle/.HandleFunc 调用点。
type regCallSite struct {
	relFile string // 相对 backend 根的路径（slash 分隔）
	line    int
	arg     string // 首参原文（空白归一化）；无顶层逗号（单参调用）为空串
	literal bool   // 首参是否为纯字符串字面量
	pattern string // literal 时的字面量值
}

// scanRouteRegistrationCallSites 整树普查全后端（非测试文件）的
// 全部 .Handle/.HandleFunc 调用点（R7-L8-01）：不预设注册只发
// 生在哪两个文件——任何文件里的调用点都进入结果，文件面规则
// （清扫）与白名单逐一相等都在这份全集上判定。测试文件除外：
// 测试自建的 mux 从不接生产 router（handler 包测试以自有 mux
// 调 Register 的情形，其注册点仍是 handler.go 的生产调用点，
// 已在普查内）。
func scanRouteRegistrationCallSites(t *testing.T, backendRoot string) []regCallSite {
	t.Helper()
	var sites []regCallSite
	err := filepath.Walk(backendRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(backendRoot, path)
		if rerr != nil {
			return rerr
		}
		src := string(data)
		rel = filepath.ToSlash(rel)
		for _, loc := range reHandleCallStart.FindAllStringIndex(src, -1) {
			arg := strings.Join(strings.Fields(firstCallArg(src, loc[1])), " ")
			site := regCallSite{
				relFile: rel,
				line:    strings.Count(src[:loc[0]], "\n") + 1,
				arg:     arg,
			}
			if isPureStringLiteral(arg) {
				site.literal = true
				if v, uerr := strconv.Unquote(arg); uerr == nil {
					site.pattern = v
				} else {
					site.pattern = strings.Trim(arg, `"`)
				}
			}
			sites = append(sites, site)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("census: walk backend tree: %v", err)
	}
	return sites
}

// nonLiteralRegSite 是一处「首参非纯字符串字面量」的注册调用点
// 登记：file 为相对 backend 根的路径，arg 为首参表达式（空白
// 归一化后），note 说明该调用点注册的路径如何被
// enumerateRegisteredRoutes 的特例展开覆盖。
type nonLiteralRegSite struct {
	file string
	arg  string
	note string
}

// nonLiteralRegistrationWhitelist 是全后端全部非常量注册调用点
// 的显式白名单（RB3-03，R7-L8-01 起整树口径）。新增此类注册时：
// 先在此登记（含覆盖口径说明），再让 enumerateRegisteredRoutes
// 的特例展开覆盖其路径、并在 routeClassification 里归类。
var nonLiteralRegistrationWhitelist = []nonLiteralRegSite{
	{
		"internal/http/handler/handler.go", "route.path",
		"备份端点循环注册：路径取自同一清单条目，枚举第 2 步以 reBackupEntry 展开清单（原前缀＋/api/v1 双前缀）覆盖其全部路径",
	},
	{
		"internal/http/handler/handler.go", `"/api/v1"+route.path`,
		"备份端点循环的双前缀别名注册：与上一处同一清单、同一展开口径",
	},
	{
		"internal/http/handler/handler.go", "ghMirrorPath",
		"GitHub 镜像代理注册：路径为 gh_mirror.go 的 ghMirrorPath 常量，枚举第 3 步以 reGHMirrorConst 提取常量值覆盖",
	},
}

// firstCallArg 返回 src 自 pos 起（紧跟调用左括号）的第一个实参
// 原文：扫描到括号深度回到 0 之前的第一个顶层逗号为止，字符串
// 内的逗号与括号不计。无顶层逗号（单参调用）时返回空串。
func firstCallArg(src string, pos int) string {
	depth := 1
	inStr := false
	for i := pos; i < len(src); i++ {
		c := src[i]
		if inStr {
			if c == '\\' {
				i++
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return ""
			}
		case ',':
			if depth == 1 {
				return src[pos:i]
			}
		}
	}
	return ""
}

// isPureStringLiteral 判 arg 是否恰为一个字符串字面量（闭引号即
// 结尾），拼接表达式 `"/api/v1"+route.path` 不算纯字面量。
func isPureStringLiteral(arg string) bool {
	if len(arg) < 2 || arg[0] != '"' {
		return false
	}
	for i := 1; i < len(arg); i++ {
		switch arg[i] {
		case '\\':
			i++
		case '"':
			return i == len(arg)-1
		}
	}
	return false
}

// TestRouteRegistrationNonLiteralWhitelist 机械扫描全后端整树
// 的全部 .Handle/.HandleFunc 调用点（R7-L8-01 起整树口径，原
// 先只开 handler.go/router.go 两文件，第三文件的非字面量注册
// 全盲）：首参非纯字符串字面量的集合必须与
// nonLiteralRegistrationWhitelist 逐一相等——多一处（新非常量
// 注册未登记）或少一处（登记已失效）都点名报红（RB3-03)。
func TestRouteRegistrationNonLiteralWhitelist(t *testing.T) {
	backendRoot := filepath.Join("..", "..", "..")

	found := map[string]string{} // key: file + "\x00" + arg → 定位 file:line
	for _, site := range scanRouteRegistrationCallSites(t, backendRoot) {
		if site.arg == "" || site.literal {
			continue
		}
		found[site.relFile+"\x00"+site.arg] = site.relFile + ":" + strconv.Itoa(site.line)
	}

	allowed := map[string]nonLiteralRegSite{}
	for _, s := range nonLiteralRegistrationWhitelist {
		key := s.file + "\x00" + s.arg
		if _, dup := allowed[key]; dup {
			t.Errorf("non-literal registration whitelist has duplicate entry %q in %s", s.arg, s.file)
		}
		allowed[key] = s
	}
	for key, at := range found {
		if _, ok := allowed[key]; !ok {
			arg := strings.SplitN(key, "\x00", 2)[1]
			t.Errorf("non-literal route registration at %s (first arg %s) is NOT in nonLiteralRegistrationWhitelist — "+
				"register it there with a note on how its paths are enumerated, or use a plain string literal (RB3-03 gate)", at, arg)
		}
	}
	for key, s := range allowed {
		if _, ok := found[key]; !ok {
			t.Errorf("non-literal registration whitelist entry %q in %s no longer matches any call site — remove it (RB3-03 gate)", s.arg, s.file)
		}
	}
}

func TestRouteClassificationCensus(t *testing.T) {
	registered := enumerateRegisteredRoutes(t)

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
	var unclassified, stale []string
	for p := range registered {
		if _, ok := classified[p]; !ok {
			unclassified = append(unclassified, p)
		}
	}
	for p := range classified {
		if !registered[p] {
			stale = append(stale, p)
		}
	}
	sort.Strings(unclassified)
	sort.Strings(stale)
	for _, p := range unclassified {
		t.Errorf("registered route %q has NO classification — classify it in routeClassification (L8-F-06 gate)", p)
	}
	for _, p := range stale {
		t.Errorf("classified route %q is no longer registered — remove or fix its classification", p)
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
