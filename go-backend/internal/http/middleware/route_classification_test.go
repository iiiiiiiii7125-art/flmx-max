package middleware

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// routeClassification 是全量注册路由的显式分类表（211 条）。
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
	adm("/api/v1/subscription/delete"), adm("/api/v1/subscription/list"),
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
	reHandleFuncLiteral = regexp.MustCompile(`\.HandleFunc\("([^"]+)"`)
	reHandleLiteral     = regexp.MustCompile(`\.Handle\("([^"]+)"`)
	reMuxLiteral        = regexp.MustCompile(`mux\.HandleFunc\("([^"]+)",`)
	reMuxHandle         = regexp.MustCompile(`mux\.Handle\("([^"]+)",`)
	reBackupEntry       = regexp.MustCompile(`\{"(/api/v1/backup/[^"]+)",`)
	reGHMirrorConst     = regexp.MustCompile(`ghMirrorPath\s*=\s*"([^"]+)"`)
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

	// 5) 注册面清扫：全后端（非测试文件）里带字面量路径的
	//    .Handle/.HandleFunc 只许出现在 handler.go 与 router.go，
	//    且清扫所得字面量集必须被上面的枚举覆盖（防换个接收者
	//    名字在别处注册绕过普查）。
	allowed := map[string]bool{
		filepath.Clean(handlerPath): true,
		filepath.Clean(routerPath):  true,
	}
	swept := map[string]bool{}
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
		src := string(data)
		hits := append(reHandleFuncLiteral.FindAllStringSubmatch(src, -1), reHandleLiteral.FindAllStringSubmatch(src, -1)...)
		if len(hits) == 0 {
			return nil
		}
		if !allowed[filepath.Clean(path)] {
			t.Errorf("census: route registration surface outside handler.go/router.go: %s", path)
			return nil
		}
		for _, m := range hits {
			swept[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("census: walk backend tree: %v", err)
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
