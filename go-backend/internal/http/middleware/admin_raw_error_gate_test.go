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

// R2-F4-01 回归门：管理员面禁止把仓储层错误原文直出给调用方。
//
// 机制谓词（F4 §B.1）：管理员可达端点以 response.Err /
// response.ErrDefault 把错误原文写入客户端回执。首轮 F4-01 只登记
// 3 点、W2 称同族由 3 扩至 26 处全收口，但其作用域只覆盖 5 文件
// 函数集，user_inbound.go／user_subscription.go 两文件与套餐隧道
// 面 23 处残留（掉表法行为证实 3 端点回 `SQL logic error: no such
// table…`）；本人面（getMyInbounds／userSubscriptionMine）已走
// respondInternalError 泛化，与管理员面同族分叉。本门把管理员面
// 的同口径钉死：仓储源错误原文一律不得进回执，须记日志＋通用文
// 案（respondInternalError）。
//
// 判定是污染分析，不是字面量 grep（W8 窄谓词只认字面量 `err`，漏
// 计異名变量 41＋Sprintf 嵌套 7——R2-F4-02）：回执消息实参含仓储
// 错误文本当且仅当——
//   - 实参子树含 `.Error()` 调用且其接收者是仓储污染的错误值；
//   - 实参是 fmt.Sprintf／Sprint／`+` 拼接且任一操作数含仓储错
//     误文本（错误值以 %v 插值同样计入）；
//   - 实参是先落入变量的消息（如 `msg := err.Error()`、
//     `rollbackErrs = append(rollbackErrs, rbErr.Error())`），沿
//     主导赋值链追溯仍含仓储错误文本。
// 错误值的仓储污染（与 unauth 门的 errExprTainted 同族、扩两
// 处）：主导赋值是 h.repo.* 调用；错误构造器（fmt.Errorf／
// errors.New／Join）任一实参污染；同包被调函数任一 return 的末
// 位错误污染（一层展开、深度上限 2，参数按调用点实参绑定——
// 如 markRepoError(err) 的透明包装、errString(err) 的文本透传
// 均靠绑定判定）；gorm 句柄的 `.Error` 字段（`tx.Commit().Error`，
// 句柄源自 h.repo.BeginTx()）。无法追溯来源的标识符（形参／字
// 段直入）不判污染（已知局限，见 adminErrTainted 注释）；主导
// 赋值按源码位置取最近一次，不建模控制流，分支内重赋值可能漏
// 报——该形态须人工复核。
//
// 推导（全部机械）：
//   1. 管理员面路由集取自 routeClassification 中 classAdmin 的
//      全部条目（与分类普查门同一真值源）；
//   2. 每条管理员路由的入口函数从 handler.go 的 Register 调用点
//      AST 提取（`h.<fn>` 直挂；备份清单循环注册按复合字面量展
//      开、双前缀齐全）；
//   3. 入口函数体（及一层同包被调函数体，防经手 helper 中转直
//      出）内，凡 response.Err／ErrDefault 的消息实参含仓储错误
//      文本即违规。
// 固定校验文案天然沉默：`Err(403, "无权限")`、`ErrDefault("请求
// 参数错误")` 等字面量消息无污染；校验错（parseTrafficLimit 的
// flowErr、normalizeAndValidateConfigValue、HashPassword 安全
// 错）与数据面清理错的来源不是仓储层，同样沉默——F4 §B.1 的
// 8 处非仓储对照点是本门的真阴性锚。
//
// 推导完整性以锚点断言兜底：三面 9 个管理员入口必须都在推导结果
// 内（user-inbound／user-subscription／tunnel/user 三面），推导
// 集过小即红，门不得以空集假绿；合成变异自检另证谓词本身成立
// （直出／Sprintf／異名变量／被调转手／tx 字段五形态必抓、合规
// 与校验错形态必沉默）。

// adminRouteEntryFns 从 handler.go 的 Register 调用点推导管理员
// 路由 → 入口函数名。分类表（routeClassification）是管理员集的
// 唯一真值源；注册形态与分类对不上（管理员路由无注册点）即
// fatal。备份清单循环注册（`for _, route := range []struct{...}`
// ＋双前缀）按复合字面量展开。
func adminRouteEntryFns(t *testing.T, backendRoot string) map[string]string {
	t.Helper()
	admin := map[string]bool{}
	for _, e := range routeClassification {
		if e.class == classAdmin {
			admin[e.path] = true
		}
	}

	_, handlerAST, _, err := parseGoFileAST(filepath.Join(backendRoot, filepath.FromSlash(handlerRelFile)))
	if err != nil {
		t.Fatalf("gate: parse handler.go: %v", err)
	}

	entries := map[string]string{}
	addEntry := func(pattern, fn string) {
		if admin[pattern] {
			entries[pattern] = fn
		}
	}
	// 备份清单循环展开：`for _, route := range []struct{path, fn}
	// {{path, fn}, ...}`，双前缀（原路径与 /api/v1+原路径）齐全。
	ast.Inspect(handlerAST, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		cl, ok := rs.X.(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, elt := range cl.Elts {
			pair, ok := elt.(*ast.CompositeLit)
			if !ok || len(pair.Elts) != 2 {
				continue
			}
			pathLit, ok := pair.Elts[0].(*ast.BasicLit)
			if !ok || pathLit.Kind != token.STRING {
				continue
			}
			path, uerr := strconv.Unquote(pathLit.Value)
			if uerr != nil {
				continue
			}
			fnSel, ok := pair.Elts[1].(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if id, ok := fnSel.X.(*ast.Ident); !ok || id.Name != "h" {
				continue
			}
			addEntry(path, fnSel.Sel.Name)
			addEntry("/api/v1"+path, fnSel.Sel.Name)
		}
		return true
	})
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
		pathLit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || pathLit.Kind != token.STRING {
			return true
		}
		pattern, uerr := strconv.Unquote(pathLit.Value)
		if uerr != nil {
			return true
		}
		if !admin[pattern] {
			return true
		}
		switch a1 := call.Args[1].(type) {
		case *ast.SelectorExpr:
			// mux.HandleFunc(path, h.<fn>)
			if id, ok := a1.X.(*ast.Ident); ok && id.Name == "h" {
				entries[pattern] = a1.Sel.Name
			}
		}
		return true
	})

	// 锚点：R2-F4-01 三面 9 个管理员入口必须都在推导集内，且每
	// 条管理员路由都要有入口（/system-info 在 router.go 注册、
	// 处理体在 ws 包，非管理员面，不在本门面）。
	anchors := map[string]string{
		"/api/v1/user-inbound/list":        "getUserInbounds",
		"/api/v1/user-inbound/set":         "setUserInbounds",
		"/api/v1/user-subscription/list":   "userSubscriptionList",
		"/api/v1/user-subscription/set":    "userSubscriptionSet",
		"/api/v1/tunnel/user/list":         "userTunnelList",
		"/api/v1/tunnel/user/assign":       "userTunnelAssign",
		"/api/v1/tunnel/user/batch-assign": "userTunnelBatchAssign",
		"/api/v1/tunnel/user/remove":       "userTunnelRemove",
		"/api/v1/tunnel/user/update":       "userTunnelUpdate",
	}
	for pattern, wantFn := range anchors {
		if got, ok := entries[pattern]; !ok || got != wantFn {
			t.Fatalf("gate: admin route %q entry derivation = (%q, %v), want %q — extraction caliber broken", pattern, got, ok, wantFn)
		}
	}
	for p := range admin {
		if _, ok := entries[p]; !ok {
			t.Fatalf("gate: admin route %q has no derived entry function in handler.go Register", p)
		}
	}
	return entries
}

// ── 污染分析 ──────────────────────────────────────────────

// adminTaintEnv 是污染判定的上下文：当前函数体（主导赋值查
// 找）、声明集（被调展开）、展开深度与防环集、参数绑定（被调展
// 开时形参 → 调用点实参及其所在函数体——绑定表达式的来源查
// 找必须在调用方体内进行，形参本身在被调体内无赋值）。
type adminTaintEnv struct {
	decls   map[string]*handlerDecl
	body    *ast.BlockStmt
	depth   int
	visited map[string]bool
	pseen   map[token.Pos]bool
	bind    map[string]adminBoundExpr
}

// adminBoundExpr 是一次参数绑定：调用点实参表达式＋其实参所在
// 的函数体（主导赋值查找的域）。
type adminBoundExpr struct {
	expr ast.Expr
	body *ast.BlockStmt
}

func (e adminTaintEnv) child(body *ast.BlockStmt, bind map[string]adminBoundExpr) adminTaintEnv {
	return adminTaintEnv{
		decls:   e.decls,
		body:    body,
		depth:   e.depth + 1,
		visited: e.visited,
		pseen:   e.pseen,
		bind:    bind,
	}
}

// adminRepoRootedCall 报告调用是否仓储层方法：只认 h.repo.* 链
// （handler 约定 DB 访问一律经 h.repo 方法）。注意与 unauth 门
// 的 repoRootedCall 的区别：后者另认 repo／store／db 包名前缀，
// 会把 repo 包的纯函数（如 repo.NormalizeKCPNodeMemMB 的校验
// 错）误判为仓储错——本门只认方法链，包级纯函数不计入。
func adminRepoRootedCall(call *ast.CallExpr) bool {
	chain := selectorChain(call.Fun)
	if len(chain) < 2 {
		return false
	}
	return chain[0] == "h" && chain[1] == "repo"
}

// adminCalleeKey 取调用的同包被调键（h.<fn> 方法记
// "Handler.<fn>"，<fn>() 包级调用记原名），跨包／方法值调用返
// 回空。
func adminCalleeKey(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		if id, ok := fun.X.(*ast.Ident); ok && id.Name == "h" {
			return "Handler." + fun.Sel.Name
		}
	}
	return ""
}

// adminBindParams 把被调声明的形参与调用点实参按位绑定（接收者
// 除外）；实参数少于形参时只绑已有部分。绑定带调用方体，供绑
// 定表达式的来源查找。
func adminBindParams(cd *handlerDecl, call *ast.CallExpr, callerBody *ast.BlockStmt) map[string]adminBoundExpr {
	bind := map[string]adminBoundExpr{}
	if cd.decl.Type == nil || cd.decl.Type.Params == nil {
		return bind
	}
	i := 0
	for _, field := range cd.decl.Type.Params.List {
		for _, name := range field.Names {
			if i < len(call.Args) {
				bind[name.Name] = adminBoundExpr{expr: call.Args[i], body: callerBody}
			}
			i++
		}
	}
	return bind
}

// adminExpandCallee 对同包被调函数逐条 return 的末位结果跑判定
// fn（错误值取末位、文本值取末位——Go 惯例 error 居末、单值返
// 回即本身）；深度上限 2，防环用 visited。
func adminExpandCallee(call *ast.CallExpr, env adminTaintEnv, fn func(ast.Expr, adminTaintEnv) bool) bool {
	if env.depth >= 2 {
		return false
	}
	key := adminCalleeKey(call)
	if key == "" || env.visited[key] {
		return false
	}
	cd, ok := env.decls[key]
	if !ok || cd.decl.Body == nil {
		return false
	}
	env.visited[key] = true
	defer delete(env.visited, key)
	child := env.child(cd.decl.Body, adminBindParams(cd, call, env.body))
	tainted := false
	ast.Inspect(cd.decl.Body, func(m ast.Node) bool {
		if tainted || m == nil {
			return !tainted
		}
		if ret, ok := m.(*ast.ReturnStmt); ok && len(ret.Results) > 0 {
			if fn(ret.Results[len(ret.Results)-1], child) {
				tainted = true
				return false
			}
		}
		return true
	})
	return tainted
}

// adminTracesToRepo 报告表达式的值是否源自仓储层句柄：h.repo.*
// 调用本身，或其根标识符的主导赋值是仓储根调用（tx ←
// h.repo.BeginTx() 之类）。供 gorm 句柄 `.Error` 字段判定。
func adminTracesToRepo(e ast.Expr, env adminTaintEnv) bool {
	switch node := e.(type) {
	case *ast.CallExpr:
		if adminRepoRootedCall(node) {
			return true
		}
		return adminTracesToRepo(node.Fun, env)
	case *ast.SelectorExpr:
		return adminTracesToRepo(node.X, env)
	case *ast.Ident:
		prod := governingProducer(env.body, node.Name, node.Pos())
		if prod == nil || env.pseen[prod.Pos()] {
			return false
		}
		env.pseen[prod.Pos()] = true
		return adminTracesToRepo(prod, env)
	case *ast.ParenExpr:
		return adminTracesToRepo(node.X, env)
	}
	return false
}

// adminErrTainted 判定错误值表达式是否可能来自仓储层。流动只有
// 五条道：仓储根调用本身；错误构造器的实参；同包被调 return 的
// 末位错误（一层展开、参数按调用点实参绑定）；gorm 句柄 `.Error`
// 字段；标识符按主导赋值递归。无法追溯的标识符（入口函数形参／
// 字段直入）不判（已知局限：入口形参不携带错误值、字段直入的仓
// 储错须人工复核；本门的三面 9 入口内无字段直入形态）。
func adminErrTainted(e ast.Expr, env adminTaintEnv) bool {
	for {
		paren, ok := e.(*ast.ParenExpr)
		if !ok {
			break
		}
		e = paren.X
	}
	switch node := e.(type) {
	case *ast.StarExpr:
		return adminErrTainted(node.X, env)
	case *ast.Ident:
		if bound, ok := env.bind[node.Name]; ok {
			child := env
			child.body = bound.body
			child.bind = map[string]adminBoundExpr{}
			for k, v := range env.bind {
				if k != node.Name {
					child.bind[k] = v
				}
			}
			return adminErrTainted(bound.expr, child)
		}
		if node.Name == "nil" {
			return false
		}
		prod := governingProducer(env.body, node.Name, node.Pos())
		if prod == nil || env.pseen[prod.Pos()] {
			return false
		}
		env.pseen[prod.Pos()] = true
		return adminErrTainted(prod, env)
	case *ast.SelectorExpr:
		// gorm 句柄的 `.Error` 字段（如 tx.Commit().Error）：
		// 句柄源自仓储层即污染。
		if node.Sel.Name == "Error" {
			return adminTracesToRepo(node.X, env)
		}
		return false
	case *ast.CallExpr:
		if adminRepoRootedCall(node) {
			return true
		}
		if isErrorConstructor(node) {
			for _, arg := range node.Args {
				if adminErrTainted(arg, env) || adminTextTainted(arg, env) {
					return true
				}
			}
			return false
		}
		return adminExpandCallee(node, env, adminErrTainted)
	case *ast.CompositeLit:
		for _, elt := range node.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if adminErrTainted(kv.Value, env) || adminTextTainted(kv.Value, env) {
					return true
				}
				continue
			}
			if adminErrTainted(elt, env) || adminTextTainted(elt, env) {
				return true
			}
		}
		return false
	}
	return false
}

// adminTextTainted 判定字符串表达式是否可能含仓储错误文本：
// `.Error()` 的接收者是污染错误值；Sprintf／Sprint／Join／
// append／`+` 任一操作数含仓储错误文本；同包被调 return 的文本
// （参数绑定）；标识符按主导赋值递归。
func adminTextTainted(e ast.Expr, env adminTaintEnv) bool {
	for {
		paren, ok := e.(*ast.ParenExpr)
		if !ok {
			break
		}
		e = paren.X
	}
	switch node := e.(type) {
	case *ast.BasicLit:
		return false
	case *ast.Ident:
		if bound, ok := env.bind[node.Name]; ok {
			child := env
			child.body = bound.body
			child.bind = map[string]adminBoundExpr{}
			for k, v := range env.bind {
				if k != node.Name {
					child.bind[k] = v
				}
			}
			return adminTextTainted(bound.expr, child)
		}
		prod := governingProducer(env.body, node.Name, node.Pos())
		if prod == nil || env.pseen[prod.Pos()] {
			return false
		}
		env.pseen[prod.Pos()] = true
		return adminTextTainted(prod, env)
	case *ast.CallExpr:
		if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Error" {
			// err.Error()：文本污染 ＝ 接收者错误值污染。
			return adminErrTainted(sel.X, env)
		}
		if chain := selectorChain(node.Fun); len(chain) == 2 {
			switch {
			case chain[0] == "fmt" && (chain[1] == "Sprintf" || chain[1] == "Sprint" || chain[1] == "Sprintln" || chain[1] == "Errorf"):
				for _, arg := range node.Args {
					if adminErrTainted(arg, env) || adminTextTainted(arg, env) {
						return true
					}
				}
				return false
			case chain[0] == "strings" && chain[1] == "Join":
				for _, arg := range node.Args {
					if adminErrTainted(arg, env) || adminTextTainted(arg, env) {
						return true
					}
				}
				return false
			}
		}
		if id, ok := node.Fun.(*ast.Ident); ok && id.Name == "append" {
			for _, arg := range node.Args {
				if adminErrTainted(arg, env) || adminTextTainted(arg, env) {
					return true
				}
			}
			return false
		}
		return adminExpandCallee(node, env, adminTextTainted)
	case *ast.BinaryExpr:
		return adminErrTainted(node.X, env) || adminTextTainted(node.X, env) ||
			adminErrTainted(node.Y, env) || adminTextTainted(node.Y, env)
	case *ast.CompositeLit:
		for _, elt := range node.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if adminErrTainted(kv.Value, env) || adminTextTainted(kv.Value, env) {
					return true
				}
				continue
			}
			if adminErrTainted(elt, env) || adminTextTainted(elt, env) {
				return true
			}
		}
		return false
	}
	return false
}

// ── 扫描 ──────────────────────────────────────────────

// adminRawSite 是一处违规点：文件、行、所属入口。
type adminRawSite struct {
	file string
	line int
	fn   string
}

// adminRawErrorSites 返回函数体内全部「response.Err／ErrDefault
// 且消息实参含仓储错误文本」的调用点。Err 的消息在下标 1、
// ErrDefault 的消息在下标 0。
func adminRawErrorSites(fset *token.FileSet, decls map[string]*handlerDecl, hd *handlerDecl) []adminRawSite {
	var sites []adminRawSite
	body := hd.decl.Body
	if body == nil {
		return sites
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
		if !ok || id.Name != "response" {
			return true
		}
		var msg ast.Expr
		switch sel.Sel.Name {
		case "Err":
			if len(call.Args) < 2 {
				return true
			}
			msg = call.Args[1]
		case "ErrDefault":
			if len(call.Args) < 1 {
				return true
			}
			msg = call.Args[0]
		default:
			return true
		}
		env := adminTaintEnv{decls: decls, body: body, visited: map[string]bool{}, pseen: map[token.Pos]bool{}, bind: map[string]adminBoundExpr{}}
		if adminTextTainted(msg, env) {
			sites = append(sites, adminRawSite{
				file: filepath.Base(hd.file),
				line: fset.Position(call.Pos()).Line,
				fn:   declKey(hd.decl),
			})
		}
		return true
	})
	return sites
}

// adminRawErrorViolations 在给定声明集与管理员入口集上跑齐谓词，
// 返回全部违规（点名路由、函数、文件行）。入口体＋一层同包被调
// 体同扫。
func adminRawErrorViolations(decls map[string]*handlerDecl, entries map[string]string) []string {
	entryFns := map[string][]string{} // fn -> patterns
	for pattern, fn := range entries {
		entryFns[fn] = append(entryFns[fn], pattern)
	}
	seen := map[string]bool{}
	var violations []string
	checkBody := func(patterns []string, owner string, hd *handlerDecl) {
		for _, site := range adminRawErrorSites(hd.fset, decls, hd) {
			key := fmt.Sprintf("%s:%d", site.file, site.line)
			if seen[key] {
				continue
			}
			seen[key] = true
			violations = append(violations, fmt.Sprintf(
				"admin route %v handler %s (%s:%d) returns warehouse-sourced error text (response.Err/ErrDefault with repository-tainted message) — log it server-side and return a generic message (R2-F4-01 gate)",
				patterns, owner, site.file, site.line))
		}
	}
	for fn, patterns := range entryFns {
		sort.Strings(patterns)
		hd, ok := decls["Handler."+fn]
		if !ok {
			violations = append(violations, fmt.Sprintf("admin entry function %q (routes %v) not found in handler package", fn, patterns))
			continue
		}
		checkBody(patterns, fn, hd)
		// 一层同包被调：防经手 helper 中转直出。
		for callee := range directCallees(hd.decl.Body) {
			if callee == "Handler."+fn {
				continue
			}
			if cd, ok := decls[callee]; ok {
				checkBody(patterns, fn+"→"+callee, cd)
			}
		}
	}
	sort.Strings(violations)
	return violations
}

func TestAdminRawErrorGate(t *testing.T) {
	backendRoot := filepath.Join("..", "..", "..")
	entries := adminRouteEntryFns(t, backendRoot)
	decls := collectHandlerDecls(t, backendRoot)
	violations := adminRawErrorViolations(decls, entries)
	for _, v := range violations {
		t.Error(v)
	}
	// 推导基数下限：管理员路由现行 136 条；提取口径损坏（漏注册
	// 形态）致推导集缩水即红，门不得以小集合假绿。
	if len(entries) < 100 {
		t.Fatalf("gate: admin routes derived = %d, want >= 100 — extraction caliber broken", len(entries))
	}
	t.Logf("admin raw-error gate: %d admin routes derived, %d handler declarations indexed, %d violations", len(entries), len(decls), len(violations))
}

// TestAdminRawErrorGateSynthetic 是门的合成变异自检：谓词必须逮
// 住直出／Sprintf 插值／異名变量／被调转手／tx 字段／消息变量／
// markRepoError 透明包装／errString 文本透传八形态，对合规形态
// （记日志＋通用文案）、校验错形态与固定 403 文案保持沉默——任
// 一不符即门退化、当场报红。本测试在基线树上必须全绿（谓词本
// 身成立的证明，与真实门在基线树上红 176 处对照）。
func TestAdminRawErrorGateSynthetic(t *testing.T) {
	src := `package handler

import (
	"fmt"
	"log"
	"net/http"

	"go-backend/internal/http/response"
)

type repoSvc struct{}

func (repoSvc) Lookup(id int) (string, error) { return "", nil }
func (repoSvc) BeginTx() txSvc                { return txSvc{} }

type txSvc struct{ Error error }

func (txSvc) Commit() txSvc { return txSvc{} }

type Handler struct{ repo repoSvc }

func markRepoError(err error) error {
	if err == nil {
		return nil
	}
	return err
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func parseLimit() error { return fmt.Errorf("流量限制不能小于0") }

func (h *Handler) loadThing(id int) (string, error) { return h.repo.Lookup(id) }

func (h *Handler) loadMarked(id int) (string, error) {
	_, err := h.repo.Lookup(id)
	if err != nil {
		return "", markRepoError(err)
	}
	return "", nil
}

func (h *Handler) pubRepoDirect(w http.ResponseWriter, r *http.Request) {
	_, err := h.repo.Lookup(1)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
}

func (h *Handler) pubSprintf(w http.ResponseWriter, r *http.Request) {
	_, err := h.repo.Lookup(1)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("lookup failed: %v", err)))
		return
	}
}

func (h *Handler) pubAlias(w http.ResponseWriter, r *http.Request) {
	_, lookupErr := h.repo.Lookup(1)
	if lookupErr != nil {
		response.WriteJSON(w, response.Err(-2, lookupErr.Error()))
		return
	}
}

func (h *Handler) pubCallee(w http.ResponseWriter, r *http.Request) {
	_, err := h.loadThing(1)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
}

func (h *Handler) pubTx(w http.ResponseWriter, r *http.Request) {
	tx := h.repo.BeginTx()
	if err := tx.Commit().Error; err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
}

func (h *Handler) pubMsgVar(w http.ResponseWriter, r *http.Request) {
	_, err := h.repo.Lookup(1)
	if err != nil {
		msg := err.Error()
		response.WriteJSON(w, response.Err(-2, msg))
		return
	}
}

func (h *Handler) pubMarkRepo(w http.ResponseWriter, r *http.Request) {
	_, err := h.loadMarked(1)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
}

func (h *Handler) pubErrString(w http.ResponseWriter, r *http.Request) {
	_, err := h.repo.Lookup(1)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, errString(err)))
		return
	}
}

func (h *Handler) pubClean(w http.ResponseWriter, r *http.Request) {
	_, err := h.repo.Lookup(1)
	if err != nil {
		log.Printf("pubClean: lookup failed: %v", err)
		response.WriteJSON(w, response.Err(-2, "Internal error"))
		return
	}
}

func (h *Handler) pubValidation(w http.ResponseWriter, r *http.Request) {
	if err := parseLimit(); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
}

func (h *Handler) pub403(w http.ResponseWriter, r *http.Request) {
	response.WriteJSON(w, response.Err(403, "无权限"))
}

func (h *Handler) adminRaw(w http.ResponseWriter, r *http.Request) {
	_, err := h.repo.Lookup(1)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic_admin.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	decls := map[string]*handlerDecl{}
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			decls[declKey(fd)] = &handlerDecl{file: "synthetic_admin.go", fset: fset, decl: fd}
		}
	}
	entries := map[string]string{
		"/pub/repo":      "pubRepoDirect",
		"/pub/sprintf":   "pubSprintf",
		"/pub/alias":     "pubAlias",
		"/pub/callee":    "pubCallee",
		"/pub/tx":        "pubTx",
		"/pub/msgvar":    "pubMsgVar",
		"/pub/markrepo":  "pubMarkRepo",
		"/pub/errstring": "pubErrString",
		"/pub/clean":     "pubClean",
		"/pub/valid":     "pubValidation",
		"/pub/403":       "pub403",
	}
	violations := adminRawErrorViolations(decls, entries)
	joined := strings.Join(violations, "\n")
	for _, want := range []string{"pubRepoDirect", "pubSprintf", "pubAlias", "pubCallee", "pubTx", "pubMsgVar", "pubMarkRepo", "pubErrString"} {
		if !strings.Contains(joined, want) {
			t.Errorf("synthetic admin gate must name %q; violations: %v", want, violations)
		}
	}
	for _, quiet := range []string{"pubClean", "pubValidation", "pub403", "adminRaw"} {
		if strings.Contains(joined, quiet) {
			t.Errorf("synthetic admin gate must stay silent on %q; violations: %v", quiet, violations)
		}
	}
	if len(violations) != 8 {
		t.Errorf("synthetic admin gate violations = %d, want exactly 8: %v", len(violations), violations)
	}
}
