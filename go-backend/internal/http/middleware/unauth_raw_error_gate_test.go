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
// 谓词扩展（修复轮 9，I-C1-06）：同一泄露面还有 http.Error 直
// 出形态——gh_mirror.go 四处与 openSubscription 渲染失败支以
// http.Error(w, err.Error(), …) 把 err 原文写给未鉴权调用方，
// 不经 response 包、旧谓词不可见。现行五点的 err 构成经审计判
// 定安全（自著运维文案＋上游网络/JSON 错误、调用方自身入参回
// 显、模板执行错误），但点位若改接仓储错误即无门会红。扩展谓
// 词对同一扫描集（公开入口＋一层被调）内的 http.Error 原文点
// 做来源追溯：err 的最近前置赋值（主导赋值）链中凡出现仓储层
// 调用（h.repo.* 或 repo/store/db 根调用，与 handler 包
// 「DB 访问一律走 h.repo 方法」的约定同口径）即违规；来源无
// 法静态追溯（形参/字段直入）同按违规 fail-closed。现行五点
// 以锚点断言钉住计数（githubMirror ≥4、openSubscription ≥1），
// 点位被删/提取口径损坏即红、不得以空集假绿。
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

// ── http.Error 直出面扩展谓词（I-C1-06）──────────────────────

// selectorChain 返回选择器链的段名序列（如 h.repo.Lookup →
// [h repo Lookup]）；表达式不是选择器链时返回 nil。
func selectorChain(e ast.Expr) []string {
	var rev []string
	for {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok {
			break
		}
		rev = append(rev, sel.Sel.Name)
		e = sel.X
	}
	id, ok := e.(*ast.Ident)
	if !ok || len(rev) == 0 {
		return nil
	}
	out := []string{id.Name}
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, rev[i])
	}
	return out
}

// repoRootedCall 报告调用是否仓储层形态：h.repo.* 链（handler
// 约定 DB 访问一律经 h.repo 方法），或以 repo/store/db 变量为
// 根的方法调用。
func repoRootedCall(call *ast.CallExpr) bool {
	chain := selectorChain(call.Fun)
	if len(chain) < 2 {
		return false
	}
	if chain[0] == "h" && chain[1] == "repo" {
		return true
	}
	switch chain[0] {
	case "repo", "store", "db":
		return true
	}
	return false
}

// governingProducer 返回函数体内 ident 在 before 位置之前最近
// 的一次赋值（主导赋值）的 RHS 表达式；无赋值（形参/字段/外
// 部来源）返回 nil。多值赋值 LHS/RHS 个数相等时按位取、单
// RHS 多 LHS（函数调用多返回）时取该 RHS。
func governingProducer(body *ast.BlockStmt, name string, before token.Pos) ast.Expr {
	var best *ast.AssignStmt
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || as.Pos() >= before {
			return true
		}
		for i, lhs := range as.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && id.Name == name {
				_ = i
				if best == nil || as.Pos() > best.Pos() {
					best = as
				}
			}
		}
		return true
	})
	if best == nil {
		return nil
	}
	for i, lhs := range best.Lhs {
		if id, ok := lhs.(*ast.Ident); ok && id.Name == name {
			if len(best.Rhs) == len(best.Lhs) {
				return best.Rhs[i]
			}
			if len(best.Rhs) == 1 {
				return best.Rhs[0]
			}
			return nil
		}
	}
	return nil
}

// isErrorConstructor 报告调用是否为错误构造器：其结果文本由
// 实参拼成（%v/%w 展开），实参的来源因此计入错误文本来源。
func isErrorConstructor(call *ast.CallExpr) bool {
	chain := selectorChain(call.Fun)
	if len(chain) != 2 {
		return false
	}
	return (chain[0] == "fmt" && chain[1] == "Errorf") ||
		(chain[0] == "errors" && (chain[1] == "New" || chain[1] == "Join"))
}

// errExprTainted 定向判定错误表达式 expr 的文本是否可能来自
// 仓储层。错误文本的流动只有三条道（漫游子树会把调用的入参
// 误作来源——入参的数据取自仓储与错误文本无关）：
//   - 仓储根调用本身的返回值（h.repo.* 等）；
//   - 错误构造器（fmt.Errorf 等）的实参（被 %v/%w 展开进文本）；
//   - 普通调用的错误返回值：展开被调函数的 return 面、只看其
//     末位结果（Go 惯例 error 居末），递归一层（depth＋访问集
//     防环）；标识符按其在当前函数体的主导赋值递归（pseen 防
//     自引用环，如 err = fmt.Errorf("%w", err)）。
//
// 跨包调用（如 kernel.Subscription）不在同包声明集内、不展
// 开，按其面外来源论（已由立案审计逐点判定构成）。
func errExprTainted(decls map[string]*handlerDecl, fnBody *ast.BlockStmt, expr ast.Expr, depth int, visited map[string]bool, pseen map[token.Pos]bool) bool {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	switch node := expr.(type) {
	case *ast.CallExpr:
		if repoRootedCall(node) {
			return true
		}
		if isErrorConstructor(node) {
			for _, arg := range node.Args {
				if errExprTainted(decls, fnBody, arg, depth, visited, pseen) {
					return true
				}
			}
			return false
		}
		if depth >= 2 {
			return false
		}
		var key string
		switch fun := node.Fun.(type) {
		case *ast.Ident:
			key = fun.Name
		case *ast.SelectorExpr:
			if id, ok := fun.X.(*ast.Ident); ok && id.Name == "h" {
				key = "Handler." + fun.Sel.Name
			}
		}
		if key == "" || visited[key] {
			return false
		}
		cd, ok := decls[key]
		if !ok || cd.decl.Body == nil {
			return false
		}
		visited[key] = true
		defer delete(visited, key)
		tainted := false
		ast.Inspect(cd.decl.Body, func(m ast.Node) bool {
			if tainted || m == nil {
				return !tainted
			}
			if ret, ok := m.(*ast.ReturnStmt); ok && len(ret.Results) > 0 {
				if errExprTainted(decls, cd.decl.Body, ret.Results[len(ret.Results)-1], depth+1, visited, pseen) {
					tainted = true
					return false
				}
			}
			return true
		})
		return tainted
	case *ast.Ident:
		prod := governingProducer(fnBody, node.Name, node.Pos())
		if prod == nil || pseen[prod.Pos()] {
			return false
		}
		pseen[prod.Pos()] = true
		return errExprTainted(decls, fnBody, prod, depth, visited, pseen)
	}
	return false
}

// httpErrorRawSites 返回函数体内全部 http.Error 原文点：消息参
// 数（下标 1）子树含 .Error() 调用，或消息参数是标识符且其主
// 导赋值的 RHS 含 .Error() 调用。每点给出调用行号与错误接收者
// 标识符名集（供来源追溯）。
type httpErrorRawSite struct {
	line      int
	receivers []string
	call      *ast.CallExpr
}

func httpErrorRawSites(fset *token.FileSet, body *ast.BlockStmt) []httpErrorRawSite {
	var sites []httpErrorRawSite
	if body == nil {
		return sites
	}
	collectReceivers := func(e ast.Expr) []string {
		var names []string
		seen := map[string]bool{}
		ast.Inspect(e, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Error" {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && !seen[id.Name] {
				seen[id.Name] = true
				names = append(names, id.Name)
			}
			return true
		})
		return names
	}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Error" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "http" {
			return true
		}
		if len(call.Args) < 2 {
			return true
		}
		msg := call.Args[1]
		receivers := collectReceivers(msg)
		if len(receivers) == 0 {
			// 消息先落入字符串变量再直出（msg := err.Error()）。
			if id, ok := msg.(*ast.Ident); ok {
				if prod := governingProducer(body, id.Name, call.Pos()); prod != nil {
					receivers = collectReceivers(prod)
				}
			}
		}
		if len(receivers) > 0 {
			sites = append(sites, httpErrorRawSite{
				line:      fset.Position(call.Pos()).Line,
				receivers: receivers,
				call:      call,
			})
		}
		return true
	})
	return sites
}

// unauthHTTPErrorViolations 在与 response 谓词同一扫描集上跑
// http.Error 扩展谓词，返回违规与逐函数的原文点计数（锚点用）。
func unauthHTTPErrorViolations(decls map[string]*handlerDecl, entries map[string]string) ([]string, map[string]int) {
	entryFns := map[string][]string{}
	for pattern, fn := range entries {
		entryFns[fn] = append(entryFns[fn], pattern)
	}
	siteCounts := map[string]int{}
	var violations []string
	checkBody := func(owner string, patterns []string, hd *handlerDecl) {
		for _, site := range httpErrorRawSites(hd.fset, hd.decl.Body) {
			siteCounts[owner]++
			for _, recv := range site.receivers {
				prod := governingProducer(hd.decl.Body, recv, site.call.Pos())
				if prod == nil {
					violations = append(violations, fmt.Sprintf(
						"public route %v handler %s (%s:%d) writes raw error text via http.Error with untraceable provenance (%s has no governing assignment in the function) — repository errors could reach unauthenticated callers undetected (I-C1-06 gate)",
						patterns, owner, hd.file, site.line, recv))
					continue
				}
				if errExprTainted(decls, hd.decl.Body, prod, 0, map[string]bool{}, map[token.Pos]bool{}) {
					violations = append(violations, fmt.Sprintf(
						"public route %v handler %s (%s:%d) writes repository-sourced error text via http.Error to unauthenticated callers — log it server-side and return a generic message (I-C1-06 gate)",
						patterns, owner, hd.file, site.line))
				}
			}
		}
	}
	for fn, patterns := range entryFns {
		sort.Strings(patterns)
		hd, ok := decls["Handler."+fn]
		if !ok {
			continue // 入口缺失已由 response 谓词路径点名
		}
		checkBody(fn, patterns, hd)
		for callee := range directCallees(hd.decl.Body) {
			if callee == "Handler."+fn {
				continue
			}
			if cd, ok := decls[callee]; ok {
				checkBody(fn+"→"+callee, patterns, cd)
			}
		}
	}
	sort.Strings(violations)
	return violations, siteCounts
}

func TestUnauthenticatedHTTPErrorGate(t *testing.T) {
	backendRoot := filepath.Join("..", "..", "..")
	entries := publicRouteEntryFns(t, backendRoot)
	decls := collectHandlerDecls(t, backendRoot)
	violations, siteCounts := unauthHTTPErrorViolations(decls, entries)
	for _, v := range violations {
		t.Error(v)
	}
	// 锚点：现行五点必须都被推导面找到且判为可放行（计数在、
	// 无违规）；点位被删或提取口径损坏即红。
	if siteCounts["githubMirror"] < 4 {
		t.Fatalf("gate: http.Error raw sites in githubMirror = %d, want >= 4 (I-C1-06 census anchor) — extraction caliber broken", siteCounts["githubMirror"])
	}
	if siteCounts["openSubscription"] < 1 {
		t.Fatalf("gate: http.Error raw sites in openSubscription = %d, want >= 1 (I-C1-06 census anchor) — extraction caliber broken", siteCounts["openSubscription"])
	}
	t.Logf("unauthenticated http.Error gate: %d raw sites across public surface, anchors githubMirror=%d openSubscription=%d", func() int {
		n := 0
		for _, c := range siteCounts {
			n += c
		}
		return n
	}(), siteCounts["githubMirror"], siteCounts["openSubscription"])
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

// TestUnauthenticatedHTTPErrorGateSynthetic 是 http.Error 扩展谓
// 词的合成变异自检：仓储直源、经 Handler 方法转手的仓储源、形
// 参直入的不可追溯源必须点名；fmt.Errorf 自著文案助手源与已
// 鉴权面同形态必须沉默。
func TestUnauthenticatedHTTPErrorGateSynthetic(t *testing.T) {
	src := `package handler

import (
	"fmt"
	"net/http"
)

type repoSvc struct{}

func (repoSvc) Lookup(id int) (string, error) { return "", nil }

type Handler struct{ repo repoSvc }

func lookupUpstream() (string, error) { return "", fmt.Errorf("GitHub returned %d", 502) }

func (h *Handler) loadThing(id int) (string, error) { return h.repo.Lookup(id) }

func (h *Handler) pubHTTPRepo(w http.ResponseWriter, r *http.Request) {
	_, err := h.repo.Lookup(1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
}

func (h *Handler) pubHTTPHelperRepo(w http.ResponseWriter, r *http.Request) {
	_, err := h.loadThing(1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
}

func (h *Handler) pubHTTPFmt(w http.ResponseWriter, r *http.Request) {
	_, err := lookupUpstream()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
}

func (h *Handler) pubHTTPCallee(w http.ResponseWriter, r *http.Request) {
	err := errBoom()
	h.emitRaw(w, err)
}

func (h *Handler) emitRaw(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusBadGateway)
}

func (h *Handler) adminHTTPRepo(w http.ResponseWriter, r *http.Request) {
	_, err := h.repo.Lookup(1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
}

func errBoom() error { return nil }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic_http.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	decls := map[string]*handlerDecl{}
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			decls[declKey(fd)] = &handlerDecl{file: "synthetic_http.go", fset: fset, decl: fd}
		}
	}
	entries := map[string]string{
		"/pub/httprepo":   "pubHTTPRepo",
		"/pub/httphelper": "pubHTTPHelperRepo",
		"/pub/httpfmt":    "pubHTTPFmt",
		"/pub/httpcallee": "pubHTTPCallee",
	}
	violations, _ := unauthHTTPErrorViolations(decls, entries)
	joined := strings.Join(violations, "\n")
	for _, want := range []string{"pubHTTPRepo", "pubHTTPHelperRepo", "emitRaw"} {
		if !strings.Contains(joined, want) {
			t.Errorf("synthetic http.Error gate must name %q; violations: %v", want, violations)
		}
	}
	if strings.Contains(joined, "pubHTTPFmt") || strings.Contains(joined, "adminHTTPRepo") {
		t.Errorf("synthetic http.Error gate must stay silent on pubHTTPFmt and adminHTTPRepo; violations: %v", violations)
	}
	if len(violations) != 3 {
		t.Errorf("synthetic http.Error gate violations = %d, want exactly 3 (pubHTTPRepo, pubHTTPHelperRepo, emitRaw): %v", len(violations), violations)
	}
}
