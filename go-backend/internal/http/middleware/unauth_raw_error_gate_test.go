package middleware

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// R2-021 回归门：未鉴权可达面禁止把内部错误原文（err.Error()）
// 直出给调用方。B-sec-01 的机制谓词——handler 在调用方尚未通过
// 任何鉴权（或仅在口令校验流程中）时，以 response.Err /
// response.ErrDefault 把 GORM/DB 错误串原文放进响应信封，泄露
// DB 形态（SQLite 锁/表结构、PG 的 SQL 片段）；联邦面已按
// O12-001 收口（记日志＋通用文案），本门把同口径钉死到全部公开
// 面，防同族在任一公开端点复活。
//
// 推导（全部机械、无手写名单）：
//  1. 公开面路由集取自 routeClassification 中 classPublic 的
//     全部条目（与分类普查门同一真值源）；
//  2. 每条公开路由的入口函数从 handler.go 的 Register 调用点
//     AST 提取（h.<fn> 直挂取 fn 名；h.authPeer(h.<fn>) 包裹的
//     联邦面入口取 authPeer 本体——内层 handler 在对端令牌验过
//     之后才运行，属已鉴权面，不在谓词内）；
//  3. 入口函数体（及一层同包被调函数体，防经手 helper 中转直
//     出）内，凡 response.Err / response.ErrDefault 的任一参数
//     子树包含 .Error() 调用即违规。
//
// 推导完整性以锚点断言兜底：历史上漏过的五个面与关键公开面
// 必须都在推导结果内，推导集过小（提取口径损坏）即红，门不得
// 以空集假绿。/system-info 注册在 router.go、处理体在 ws 包，
// 不在本门扫描面（其凭据面由 B-sec §三复核覆盖）。

// argContainsErrorCall 报告表达式子树中是否含 .Error() 方法调用。
func argContainsErrorCall(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if found || n == nil {
			return !found
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Error" {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// rawErrorResponseLines 返回函数体内全部「response.Err /
// response.ErrDefault 且参数含 .Error() 调用」的调用行号。
func rawErrorResponseLines(fset *token.FileSet, body *ast.BlockStmt) []int {
	var lines []int
	if body == nil {
		return lines
	}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != "response" || (sel.Sel.Name != "Err" && sel.Sel.Name != "ErrDefault") {
			return true
		}
		for _, arg := range call.Args {
			if argContainsErrorCall(arg) {
				lines = append(lines, fset.Position(call.Pos()).Line)
				break
			}
		}
		return true
	})
	return lines
}

// directCallees 返回函数体内直接调用的同包函数键集合（键口径
// 与 declKey 一致）：h.<fn>() 方法调用记 "Handler.<fn>"，
// <fn>() 包级调用记原名（h.repo.<fn> 的外层 X 非 Ident，不命
// 中；跨包调用同理不命中）。
func directCallees(body *ast.BlockStmt) map[string]bool {
	out := map[string]bool{}
	if body == nil {
		return out
	}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if id, ok := fun.X.(*ast.Ident); ok && id.Name == "h" {
				out["Handler."+fun.Sel.Name] = true
			}
		case *ast.Ident:
			out[fun.Name] = true
		}
		return true
	})
	return out
}

// declKey 是声明的索引键：方法记 "<接收者基类型>.<名>"（同名
// 方法可分属不同接收者类型，如 loginRateLimiter 与
// loginUsernameLimiter 各有 evictIfNeededLocked），包级函数记
// 原名。
func declKey(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

type handlerDecl struct {
	file string
	fset *token.FileSet
	decl *ast.FuncDecl
}

// collectHandlerDecls 解析 handler 包全部非测试源文件，按函数名
// 索引 FuncDecl（同名重定义即 fatal，门不许在重名歧义下判定）。
func collectHandlerDecls(t *testing.T, backendRoot string) map[string]*handlerDecl {
	t.Helper()
	dir := filepath.Join(backendRoot, "internal", "http", "handler")
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	decls := map[string]*handlerDecl{}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		fset, file, _, perr := parseGoFileAST(p)
		if perr != nil {
			t.Fatalf("gate: parse %s: %v", p, perr)
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			key := declKey(fd)
			if prev, dup := decls[key]; dup {
				t.Fatalf("gate: handler function %q defined twice (%s and %s)", key, prev.file, p)
			}
			decls[key] = &handlerDecl{file: filepath.ToSlash(p), fset: fset, decl: fd}
		}
	}
	return decls
}

// publicRouteEntryFns 从 handler.go 的 Register 调用点推导公开
// 路由 → 入口函数名。分类表（routeClassification）是公开集的唯
// 一真值源；注册形态与分类对不上（公开路由无注册点）即 fatal。
func publicRouteEntryFns(t *testing.T, backendRoot string) map[string]string {
	t.Helper()
	public := map[string]bool{}
	for _, e := range routeClassification {
		if e.class == classPublic {
			public[e.path] = true
		}
	}

	_, handlerAST, _, err := parseGoFileAST(filepath.Join(backendRoot, filepath.FromSlash(handlerRelFile)))
	if err != nil {
		t.Fatalf("gate: parse handler.go: %v", err)
	}
	_, ghAST, _, err := parseGoFileAST(filepath.Join(backendRoot, filepath.FromSlash(ghMirrorRelFile)))
	if err != nil {
		t.Fatalf("gate: parse gh_mirror.go: %v", err)
	}
	ghPath, ok := constStringValue(ghAST, "ghMirrorPath")
	if !ok {
		t.Fatal("gate: ghMirrorPath constant not found")
	}

	entries := map[string]string{}
	ast.Inspect(handlerAST, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "mux" {
			return true
		}
		if len(call.Args) < 2 {
			return true
		}
		var pattern string
		switch a0 := call.Args[0].(type) {
		case *ast.BasicLit:
			if a0.Kind != token.STRING {
				return true
			}
			v, uerr := strconv.Unquote(a0.Value)
			if uerr != nil {
				return true
			}
			pattern = v
		case *ast.Ident:
			if a0.Name != "ghMirrorPath" {
				return true // 备份循环等非字面量注册：均为非公开面
			}
			pattern = ghPath
		default:
			return true
		}
		if !public[pattern] {
			return true
		}
		switch a1 := call.Args[1].(type) {
		case *ast.SelectorExpr:
			// mux.HandleFunc(path, h.<fn>)
			if id, ok := a1.X.(*ast.Ident); ok && id.Name == "h" {
				entries[pattern] = a1.Sel.Name
			}
		case *ast.CallExpr:
			// mux.HandleFunc(path, h.authPeer(h.<fn>))：入口是
			// authPeer 本体，内层在对端鉴权之后运行。
			if s, ok := a1.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "authPeer" {
				entries[pattern] = "authPeer"
			}
		}
		return true
	})

	// 锚点：历史漏面与关键公开面必须都在推导集内，且每条公开
	// 路由都要有入口（/system-info 在 router.go 注册、处理体在
	// ws 包，不在本门面，单独豁免并在此点名）。
	anchors := map[string]string{
		"/api/v1/user/login":         "login",
		"/api/v1/open_api/sub_store": "openAPISubStore",
		"/api/v1/captcha/check":      "checkCaptcha",
		"/api/v1/captcha/verify":     "captchaVerify",
		"/api/v1/config/get":         "getConfigByName",
		"/api/v1/public/config/get":  "getPublicConfigByName",
		"/api/v1/federation/connect": "authPeer",
		"/api/v1/open_api/sub/":      "openSubscription",
		"/flow/config":               "flowConfig",
	}
	for pattern, wantFn := range anchors {
		if got, ok := entries[pattern]; !ok || got != wantFn {
			t.Fatalf("gate: public route %q entry derivation = (%q, %v), want %q — extraction caliber broken", pattern, got, ok, wantFn)
		}
	}
	for p := range public {
		if p == "/system-info" {
			continue
		}
		if _, ok := entries[p]; !ok {
			t.Fatalf("gate: public route %q has no derived entry function in handler.go Register", p)
		}
	}
	return entries
}

// unauthRawErrorViolations 在给定声明集与公开入口集上跑齐谓词，
// 返回全部违规（点名路由、函数、文件行）。
func unauthRawErrorViolations(decls map[string]*handlerDecl, entries map[string]string) []string {
	entryFns := map[string][]string{} // fn -> patterns
	for pattern, fn := range entries {
		entryFns[fn] = append(entryFns[fn], pattern)
	}
	var violations []string
	for fn, patterns := range entryFns {
		sort.Strings(patterns)
		hd, ok := decls["Handler."+fn]
		if !ok {
			violations = append(violations, fmt.Sprintf("public entry function %q (routes %v) not found in handler package", fn, patterns))
			continue
		}
		for _, line := range rawErrorResponseLines(hd.fset, hd.decl.Body) {
			violations = append(violations, fmt.Sprintf(
				"public route %v handler %s (%s:%d) returns raw error text (response.Err/ErrDefault with .Error() argument) to unauthenticated callers — log it server-side and return a generic message (R2-021 gate)",
				patterns, fn, hd.file, line))
		}
		// 一层同包被调：防经手 helper 中转直出。
		for callee := range directCallees(hd.decl.Body) {
			if callee == "Handler."+fn {
				continue
			}
			cd, ok := decls[callee]
			if !ok {
				continue
			}
			for _, line := range rawErrorResponseLines(cd.fset, cd.decl.Body) {
				violations = append(violations, fmt.Sprintf(
					"public route %v handler %s calls %s (%s:%d) which returns raw error text (response.Err/ErrDefault with .Error() argument) to unauthenticated callers (R2-021 gate)",
					patterns, fn, callee, cd.file, line))
			}
		}
	}
	sort.Strings(violations)
	return violations
}

func TestUnauthenticatedRawErrorGate(t *testing.T) {
	backendRoot := filepath.Join("..", "..", "..")
	entries := publicRouteEntryFns(t, backendRoot)
	decls := collectHandlerDecls(t, backendRoot)
	for _, v := range unauthRawErrorViolations(decls, entries) {
		t.Error(v)
	}
	t.Logf("unauthenticated raw-error gate: %d public routes derived, %d handler declarations indexed", len(entries), len(decls))
}

// TestUnauthenticatedRawErrorGateSynthetic 是门的合成变异自检：
// 谓词必须逮住直挂形态与拼接形态、对合规形态（记日志＋通用文
// 案）与已鉴权面同形态保持沉默——任一不符即门退化、当场报红。
func TestUnauthenticatedRawErrorGateSynthetic(t *testing.T) {
	src := `package handler

import (
	"log"
	"net/http"

	"go-backend/internal/http/response"
)

type Handler struct{ repo int }

func (h *Handler) pubRaw(w http.ResponseWriter, r *http.Request) {
	err := errBoom()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
}

func (h *Handler) pubConcat(w http.ResponseWriter, r *http.Request) {
	err := errBoom()
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("lookup failed: "+err.Error()))
		return
	}
}

func (h *Handler) pubViaHelper(w http.ResponseWriter, r *http.Request) {
	h.helperRaw(w)
}

func (h *Handler) helperRaw(w http.ResponseWriter) {
	err := errBoom()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
}

func (h *Handler) pubClean(w http.ResponseWriter, r *http.Request) {
	err := errBoom()
	if err != nil {
		log.Printf("pubClean: lookup failed: %v", err)
		response.WriteJSON(w, response.Err(-2, "Internal error"))
		return
	}
}

func (h *Handler) adminRaw(w http.ResponseWriter, r *http.Request) {
	err := errBoom()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
}

func errBoom() error { return nil }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	decls := map[string]*handlerDecl{}
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			decls[declKey(fd)] = &handlerDecl{file: "synthetic.go", fset: fset, decl: fd}
		}
	}
	entries := map[string]string{
		"/pub/raw":    "pubRaw",
		"/pub/concat": "pubConcat",
		"/pub/helper": "pubViaHelper",
		"/pub/clean":  "pubClean",
	}
	violations := unauthRawErrorViolations(decls, entries)
	joined := strings.Join(violations, "\n")
	for _, want := range []string{"pubRaw", "pubConcat", "helperRaw"} {
		if !strings.Contains(joined, want) {
			t.Errorf("synthetic gate must name %q; violations: %v", want, violations)
		}
	}
	if strings.Contains(joined, "pubClean") || strings.Contains(joined, "adminRaw") {
		t.Errorf("synthetic gate must stay silent on pubClean and adminRaw; violations: %v", violations)
	}
	if len(violations) != 3 {
		t.Errorf("synthetic gate violations = %d, want exactly 3 (pubRaw, pubConcat, helperRaw): %v", len(violations), violations)
	}
}
