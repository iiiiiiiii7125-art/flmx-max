package middleware

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Route test-coverage gate (C1-02, LEDGER 族八).
//
// 判定：runCensus 枚举的全量注册路由，每条必须有行为测试引用——
// 三口径并集（与 C1-02 审计口径一致，AST 口径，注释/字符串幽灵
// 永不计入）：
//   C1 路径字符串：测试文件含该路径字面量；尾斜杠前缀路由
//       （/api/v1/monitor/nodes/）以任一更长字面量为前缀命中为准；
//   C2 handler 直调：测试文件含该 handler 方法名的标识符
//       （h.<method> 方法值、<method>( 直调均为 AST 标识符）；
//   C3 契约测试：tests/contract/ 内含该路径字面量。
// 无口径命中者只许走显式漏斗登记（routeCoverageFunnel）：登记
// 共享漏斗的测试名＋差异点说明；登记本身受卫生检查（重复、
// 失效、已直覆者须摘除）。本文件自身与
// route_classification_test.go 排除在引用扫描之外——后者含全量
// 路由分类表字面量，计入即自证、门恒绿（防自证是本门的第一变异项）。

// routeCoverageFunnelEntry 是一条无直接测试引用的路由的显式
// 漏斗登记：test 为 internal/http/handler/ 内真实存在的测试
// 函数（须跑共享漏斗并钉住本路由的差异点），note 说明为何无
// 直接引用及漏斗断言钉住的差异点。
type routeCoverageFunnelEntry struct {
	path string
	test string
	note string
}

// routeCoverageFunnel 是当前全集的漏斗登记表。C1-02 补测落地后
// 全集均有直接引用，本表为空——新增无直接引用的路由时，先在
// 此登记（含差异点说明），再让门通过；无登记即红。
var routeCoverageFunnel = []routeCoverageFunnelEntry{}

// routeHandlerMapping 是路由 → handler 方法名的映射；handler
// 为空表示注册形态无法解析方法名（如 /system-info 的
// h.WebSocketHandler() 调用值），该路由只走 C1/C3 口径。
type routeHandlerMapping struct {
	handlers map[string]string
	fatal    string
}

// routeHandlerMap 从 handler.go / router.go 的 AST 提取路由 →
// handler 映射：字面量首参＋h.<method> 次参；ghMirrorPath 常量
// 首参取常量值；备份循环清单展开（原前缀＋/api/v1 双前缀）。
func routeHandlerMap(root string) routeHandlerMapping {
	out := routeHandlerMapping{handlers: map[string]string{}}
	_, handlerAST, _, err := parseGoFileAST(filepath.Join(root, filepath.FromSlash(handlerRelFile)))
	if err != nil {
		out.fatal = fmt.Sprintf("coverage: parse handler.go: %v", err)
		return out
	}
	_, ghAST, _, err := parseGoFileAST(filepath.Join(root, filepath.FromSlash(ghMirrorRelFile)))
	if err != nil {
		out.fatal = fmt.Sprintf("coverage: parse gh_mirror.go: %v", err)
		return out
	}
	ghPath, ok := constStringValue(ghAST, "ghMirrorPath")
	if !ok {
		out.fatal = "coverage: ghMirrorPath constant not found in gh_mirror.go"
		return out
	}
	// 备份循环清单：{path, h.fn} → 双前缀映射。
	ast.Inspect(handlerAST, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, elt := range lit.Elts {
			inner, ok := elt.(*ast.CompositeLit)
			if !ok || len(inner.Elts) != 2 {
				continue
			}
			pathLit, ok := inner.Elts[0].(*ast.BasicLit)
			if !ok || pathLit.Kind != token.STRING {
				continue
			}
			path, err := strconv.Unquote(pathLit.Value)
			if err != nil || !strings.HasPrefix(path, "/api/v1/backup/") {
				continue
			}
			sel, ok := inner.Elts[1].(*ast.SelectorExpr)
			if !ok {
				continue
			}
			out.handlers[path] = sel.Sel.Name
			out.handlers["/api/v1"+path] = sel.Sel.Name
		}
		return true
	})
	// 字面量 / 常量注册调用点。
	for _, rel := range []string{handlerRelFile, routerRelFile} {
		_, file, _, err := parseGoFileAST(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			out.fatal = fmt.Sprintf("coverage: parse %s: %v", rel, err)
			return out
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
				return true
			}
			if len(call.Args) < 2 {
				return true
			}
			var path string
			switch arg := call.Args[0].(type) {
			case *ast.BasicLit:
				if arg.Kind != token.STRING {
					return true
				}
				v, err := strconv.Unquote(arg.Value)
				if err != nil {
					return true
				}
				path = v
			case *ast.Ident:
				if arg.Name == "ghMirrorPath" {
					path = ghPath
				} else {
					return true
				}
			default:
				return true
			}
			if hsel, ok := call.Args[1].(*ast.SelectorExpr); ok {
				if id, ok := hsel.X.(*ast.Ident); ok && id.Name == "h" {
					out.handlers[path] = hsel.Sel.Name
				}
			}
			return true
		})
	}
	return out
}

// testReferences 是测试整树的 AST 引用集：字符串字面量、标识
// 符、测试函数名（及其所在文件）。注释与字符串内的 handler
// 名不进入标识符集——AST 口径的要点。
type testReferences struct {
	literals         map[string]bool
	contractLiterals map[string]bool
	idents           map[string]bool
	testFuncs        map[string]string // TestXxx → 相对路径
	fatal            string
}

// coverageScanExcluded 是引用扫描排除的测试文件（basename）：
// route_classification_test.go 含全量路由分类表字面量、
// route_test_coverage_test.go 含本门自身的合成树与登记表——
// 计入任一者即自证，门恒绿。
var coverageScanExcluded = map[string]bool{
	"route_classification_test.go": true,
	"route_test_coverage_test.go":  true,
}

func collectTestReferences(root string) testReferences {
	refs := testReferences{
		literals:         map[string]bool{},
		contractLiterals: map[string]bool{},
		idents:           map[string]bool{},
		testFuncs:        map[string]string{},
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if coverageScanExcluded[filepath.Base(path)] {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		_, file, _, perr := parseGoFileAST(path)
		if perr != nil {
			return fmt.Errorf("coverage: parse %s: %w", path, perr)
		}
		contract := strings.HasPrefix(rel, "tests/contract/")
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BasicLit:
				if x.Kind == token.STRING {
					if v, err := strconv.Unquote(x.Value); err == nil {
						refs.literals[v] = true
						if contract {
							refs.contractLiterals[v] = true
						}
					}
				}
			case *ast.Ident:
				refs.idents[x.Name] = true
			case *ast.FuncDecl:
				if strings.HasPrefix(x.Name.Name, "Test") {
					refs.testFuncs[x.Name.Name] = rel
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		refs.fatal = err.Error()
	}
	return refs
}

// coverageViolations 在给定 root 与漏斗登记表上跑覆盖门：
// 每条注册路由须有 C1/C2/C3 任一口径命中，或有效的漏斗登记；
// 漏斗登记本身须卫生（无重复、无失效、无已直覆）。
func coverageViolations(root string, funnel []routeCoverageFunnelEntry) []string {
	out := runCensus(root)
	if out.fatal != "" {
		return []string{out.fatal}
	}
	hm := routeHandlerMap(root)
	if hm.fatal != "" {
		return []string{hm.fatal}
	}
	refs := collectTestReferences(root)
	if refs.fatal != "" {
		return []string{refs.fatal}
	}
	funnelByPath := map[string]routeCoverageFunnelEntry{}
	var violations []string
	for _, e := range funnel {
		if _, dup := funnelByPath[e.path]; dup {
			violations = append(violations, fmt.Sprintf(
				"coverage funnel has duplicate entry for %q — remove one", e.path))
			continue
		}
		funnelByPath[e.path] = e
		if !out.routes[e.path] {
			violations = append(violations, fmt.Sprintf(
				"coverage funnel entry %q is no longer registered — remove it", e.path))
			continue
		}
		file, ok := refs.testFuncs[e.test]
		if !ok {
			violations = append(violations, fmt.Sprintf(
				"coverage funnel for %q names test %q, which does not exist — register a real funnel test", e.path, e.test))
			continue
		}
		if !strings.HasPrefix(file, "internal/http/handler/") {
			violations = append(violations, fmt.Sprintf(
				"coverage funnel for %q names test %q in %s, not in internal/http/handler/ — funnel tests must live with the behaviour tests", e.path, e.test, file))
		}
	}
	routes := make([]string, 0, len(out.routes))
	for p := range out.routes {
		routes = append(routes, p)
	}
	sort.Strings(routes)
	for _, p := range routes {
		handler := hm.handlers[p]
		c1 := refs.literals[p]
		if !c1 && strings.HasSuffix(p, "/") {
			for lit := range refs.literals {
				if strings.HasPrefix(lit, p) {
					c1 = true
					break
				}
			}
		}
		c2 := handler != "" && refs.idents[handler]
		c3 := refs.contractLiterals[p]
		if !c3 && strings.HasSuffix(p, "/") {
			for lit := range refs.contractLiterals {
				if strings.HasPrefix(lit, p) {
					c3 = true
					break
				}
			}
		}
		covered := c1 || c2 || c3
		if e, ok := funnelByPath[p]; ok {
			if covered {
				// 已直覆的路由不许再占漏斗登记（双向卫生，与
				// nonLiteralRegistrationWhitelist 同口径）。
				violations = append(violations, fmt.Sprintf(
					"coverage funnel entry for %q (test %q) is stale — the route is already covered by a direct reference; remove it", p, e.test))
			}
			continue
		}
		if covered {
			continue
		}
		detail := "no test reference (C1 path literal / C2 handler identifier / C3 contract literal)"
		if handler == "" {
			detail += " — handler unresolvable, C1/C3 only"
		} else {
			detail += fmt.Sprintf(" — handler %s", handler)
		}
		violations = append(violations, fmt.Sprintf(
			"registered route %q has %s — add a behaviour test, or register a funnel in routeCoverageFunnel with its differentiating assertion", p, detail))
	}
	return violations
}

// TestRouteTestCoverage 是 C1-02 的常设门：全量注册路由逐条
// 须有三口径任一命中或有效漏斗登记。
func TestRouteTestCoverage(t *testing.T) {
	backendRoot := filepath.Join("..", "..", "..")
	out := runCensus(backendRoot)
	if out.fatal != "" {
		t.Fatal(out.fatal)
	}
	for _, msg := range coverageViolations(backendRoot, routeCoverageFunnel) {
		t.Error(msg)
	}
	t.Logf("route test coverage: %d registered routes checked", len(out.routes))
}

// ---- 常设变异红证：合成后端树 ----

const coverageSyntheticHandlerGo = `package handler

import "net/http"

type probeHandler struct{}

func (probeHandler) keep(http.ResponseWriter, *http.Request)   {}
func (probeHandler) verify(http.ResponseWriter, *http.Request) {}
func (probeHandler) exp(http.ResponseWriter, *http.Request)    {}
func (probeHandler) imp(http.ResponseWriter, *http.Request)    {}
func (probeHandler) bexp(http.ResponseWriter, *http.Request)   {}
func (probeHandler) bimp(http.ResponseWriter, *http.Request)   {}
func (probeHandler) gh(http.ResponseWriter, *http.Request)     {}
func (probeHandler) mon(http.ResponseWriter, *http.Request)    {}

func (h probeHandler) Register(mux *http.ServeMux) {
    mux.HandleFunc("/api/v1/probe/keep", h.keep)
    mux.HandleFunc("/api/v1/probe/verify", h.verify)
    mux.HandleFunc("/api/v1/probe/exp", h.exp)
    mux.HandleFunc("/api/v1/probe/imp", h.imp)
    mux.HandleFunc("/api/v1/monitor/tunnels/", h.mon)
    for _, route := range []struct {
        path string
        fn   http.HandlerFunc
    }{
        {"/api/v1/backup/export", h.bexp},
        {"/api/v1/backup/import", h.bimp},
    } {
        mux.HandleFunc(route.path, route.fn)
        mux.HandleFunc("/api/v1"+route.path, route.fn)
    }
    mux.HandleFunc(ghMirrorPath, h.gh)
}
`

const coverageSyntheticGHMirrorGo = "package handler\n\nconst ghMirrorPath = \"/api/v1/open_api/gh/\"\n"

const coverageSyntheticRouterGo = `package httpserver

import "net/http"

func registerProbe(mux *http.ServeMux, h http.Handler) {
    mux.Handle("/system-info", h)
}
`

// coverageProbeTestGo 是合成树的基线测试引用：keep 走 C1 路径
// 字面量，verify 走 C2 handler 标识符，bexp 走 C2（同时覆盖
// 备份 export 原前缀与双前缀别名）。exp、imp、mon（仅更长字面量
// 未给）、gh、/system-info、备份 import 双路均无引用。
const coverageProbeTestGo = `package handler

import "testing"

func TestCoverageProbe(t *testing.T) {
    _ = "/api/v1/probe/keep"
    var h probeHandler
    _ = h.verify
    _ = h.bexp
}
`

func writeCoverageSyntheticTree(t *testing.T, root string) {
	t.Helper()
	writeTreeFile(t, root, "internal/http/handler/handler.go", coverageSyntheticHandlerGo)
	writeTreeFile(t, root, "internal/http/handler/gh_mirror.go", coverageSyntheticGHMirrorGo)
	writeTreeFile(t, root, "internal/http/router.go", coverageSyntheticRouterGo)
	writeTreeFile(t, root, "internal/http/handler/coverage_probe_test.go", coverageProbeTestGo)
}

func coverageViolationText(t *testing.T, root string, funnel []routeCoverageFunnelEntry) string {
	t.Helper()
	return strings.Join(coverageViolations(root, funnel), "\n")
}

// TestRouteTestCoverageGateMutations 是覆盖门的常设变异红证：
// 同一判定逻辑（coverageViolations）在合成树上必须对每一类
// 逃逸报红并点名探针——无引用、注释幽灵、字符串幽灵、自证
// 文件、失效漏斗；反之合法引用（C1/C2/C3 形态、前缀命中、
// 双前缀共享 handler、有效漏斗）必须放行。
func TestRouteTestCoverageGateMutations(t *testing.T) {
	wantUncovered := []string{
		"/api/v1/probe/exp",
		"/api/v1/probe/imp",
		"/api/v1/monitor/tunnels/",
		"/api/v1/backup/import",
		"/api/v1/api/v1/backup/import",
		"/api/v1/open_api/gh/",
		"/system-info",
	}
	mustName := func(t *testing.T, text string, probes ...string) {
		t.Helper()
		for _, p := range probes {
			if !strings.Contains(text, p) {
				t.Errorf("violations must name %q; got:\n%s", p, text)
			}
		}
	}
	mustNotName := func(t *testing.T, text string, probes ...string) {
		t.Helper()
		for _, p := range probes {
			if strings.Contains(text, p) {
				t.Errorf("violations must NOT name %q (it is covered); got:\n%s", p, text)
			}
		}
	}

	t.Run("MC1 baseline names every uncovered route", func(t *testing.T) {
		root := t.TempDir()
		writeCoverageSyntheticTree(t, root)
		text := coverageViolationText(t, root, nil)
		mustName(t, text, wantUncovered...)
		mustNotName(t, text,
			"/api/v1/probe/keep", "/api/v1/probe/verify",
			"/api/v1/backup/export", "/api/v1/api/v1/backup/export")
	})

	t.Run("MC2 adding references clears exactly those routes", func(t *testing.T) {
		root := t.TempDir()
		writeCoverageSyntheticTree(t, root)
		appendTreeFile(t, root, "internal/http/handler/coverage_probe_test.go",
			"\nfunc TestCoverageProbeMore(t *testing.T) {\n\t_ = \"/api/v1/probe/exp\"\n\tvar h probeHandler\n\t_ = h.bimp\n}\n")
		text := coverageViolationText(t, root, nil)
		mustNotName(t, text,
			"/api/v1/probe/exp", "/api/v1/backup/import", "/api/v1/api/v1/backup/import")
		mustName(t, text, "/api/v1/probe/imp", "/system-info")
	})

	t.Run("MC3 comment ghost does not count", func(t *testing.T) {
		root := t.TempDir()
		writeCoverageSyntheticTree(t, root)
		appendTreeFile(t, root, "internal/http/handler/coverage_probe_test.go",
			"\n// h.exp is covered by TestCoverageProbeMore (ghost comment)\n// \"/api/v1/probe/imp\" is referenced here (ghost comment)\n")
		text := coverageViolationText(t, root, nil)
		mustName(t, text, "/api/v1/probe/exp", "/api/v1/probe/imp")
	})

	t.Run("MC4 handler name inside a string literal does not count", func(t *testing.T) {
		root := t.TempDir()
		writeCoverageSyntheticTree(t, root)
		appendTreeFile(t, root, "internal/http/handler/coverage_probe_test.go",
			"\nfunc TestCoverageProbeGhost(t *testing.T) {\n\t_ = \"h.exp\"\n}\n")
		text := coverageViolationText(t, root, nil)
		mustName(t, text, "/api/v1/probe/exp")
	})

	t.Run("MC5 prefix route covered by a longer path literal", func(t *testing.T) {
		root := t.TempDir()
		writeCoverageSyntheticTree(t, root)
		appendTreeFile(t, root, "internal/http/handler/coverage_probe_test.go",
			"\nfunc TestCoverageProbePrefix(t *testing.T) {\n\t_ = \"/api/v1/monitor/tunnels/quality\"\n}\n")
		text := coverageViolationText(t, root, nil)
		mustNotName(t, text, "/api/v1/monitor/tunnels/")
		mustName(t, text, "/api/v1/probe/exp")
	})

	t.Run("MC6 funnel naming a missing test goes red", func(t *testing.T) {
		root := t.TempDir()
		writeCoverageSyntheticTree(t, root)
		funnel := []routeCoverageFunnelEntry{{
			path: "/api/v1/probe/exp", test: "TestNoSuchFunnel",
			note: "probe: no such test",
		}}
		text := coverageViolationText(t, root, funnel)
		mustName(t, text, "/api/v1/probe/exp", "TestNoSuchFunnel")
	})

	t.Run("MC7 funnel naming a real handler-package test clears", func(t *testing.T) {
		root := t.TempDir()
		writeCoverageSyntheticTree(t, root)
		funnel := []routeCoverageFunnelEntry{{
			path: "/api/v1/probe/exp", test: "TestCoverageProbe",
			note: "probe: shared funnel exercised by TestCoverageProbe",
		}}
		text := coverageViolationText(t, root, funnel)
		mustNotName(t, text, "/api/v1/probe/exp")
		mustName(t, text, "/api/v1/probe/imp")
	})

	t.Run("MC8 self-proof files do not count", func(t *testing.T) {
		root := t.TempDir()
		writeCoverageSyntheticTree(t, root)
		// 分类表形态：全量路径字面量只出现在被排除的文件里时，
		// 门必须继续报红——否则分类表自身即构成自证。
		writeTreeFile(t, root, "internal/http/handler/route_classification_test.go",
			"package handler\n\nimport \"testing\"\n\nfunc TestClassificationTable(t *testing.T) {\n\t_ = \"/api/v1/probe/exp\"\n\t_ = \"/api/v1/probe/imp\"\n\tvar h probeHandler\n\t_ = h.mon\n\t_ = h.gh\n}\n")
		text := coverageViolationText(t, root, nil)
		mustName(t, text,
			"/api/v1/probe/exp", "/api/v1/probe/imp",
			"/api/v1/monitor/tunnels/", "/api/v1/open_api/gh/")
	})

	t.Run("MC9 stale funnel for an already-covered route goes red", func(t *testing.T) {
		root := t.TempDir()
		writeCoverageSyntheticTree(t, root)
		funnel := []routeCoverageFunnelEntry{{
			path: "/api/v1/probe/keep", test: "TestCoverageProbe",
			note: "probe: keep is already C1-covered, funnel must be removed",
		}}
		text := coverageViolationText(t, root, funnel)
		// 已直覆的路由不许再占漏斗登记：卫生检查须点名。
		if !strings.Contains(text, "/api/v1/probe/keep") {
			t.Errorf("stale funnel for covered route must be flagged; got:\n%s", text)
		}
	})
}
