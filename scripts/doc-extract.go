//go:build ignore

// doc-extract.go — check-doc-consistency.sh 的编译集提取助手
// （修复轮 7，R7-27 / G-C4-10 机制处置）。
//
// 背景：doc 门的 §1 前提、§7 事件集、§10(a) 计数旧实现以 awk 词法
// 剥注释（go_strip，字符串/原始字符串内容原样保留）＋ grep 提取。
// 该面有两个机制缺口：①原始字符串的内容仍是 grep 的命中面——把
// 真实常量/函数的文本搬进原始字符串（调用点改字面量）后，计数
// 与集合不变而真实定义已漂移；②文件枚举不认构建约束——
// //go:build ignore 的文件与当前平台不编译的文件，其文本同样
// 入集。本助手改以 go/ast 解析真实语法树、go/build 按构建约束
// （含 //go:build 与 GOOS/GOARCH 文件名后缀）定文件集，即编译
// 集本身：字符串/原始字符串只是字面量节点、永不冒充声明，构
// 建排除文件不入枚举。
//
// 用法（由 check-doc-consistency.sh 构建后调用，不单独运行）：
//
//	doc-extract events <dir>           递归枚举 dir 下非测试 .go
//	                                   文件中包级 notifyEvent* 字
//	                                   符串常量/变量的值集（排序
//	                                   去重、逐行输出）；同时守
//	                                   发射面（修复轮 9，R9-27）：
//	                                   发射入口（dispatchNotify/
//	                                   notifySync/notifyTracked/
//	                                   notify）与 notifyJob/
//	                                   notifyMessage 字面量的事件
//	                                   值若为字符串字面量直发、
//	                                   或来源不是 notifyEvent* 常
//	                                   量标识符且不在机器内转发
//	                                   函数内 → 退出 3 点名。
//	doc-extract testfns <dir> <prefix> 递归统计 dir 下 *_test.go
//	                                   顶层函数名以 prefix 开头
//	                                   的数量（修复轮 9，R9-26：
//	                                   与 go test ./... 的包面
//	                                   对齐，子目录包同入集）
//	doc-extract strlit <file> <value>  file 中存在字符串字面量其
//	                                   值恰为 value 时退出 0
//	doc-extract keyemit <file> <type> <value>
//	                                   file 中存在「以 value 为键
//	                                   的 <type> 复合字面量、且该字
//	                                   面量位于 append(...) 实参子
//	                                   树内」（即渲染发射点形态）时
//	                                   退出 0
//
// 退出码：0 成功（strlit/keyemit 为「找到」）；1 未找到；
// 2 用法/IO/解析错误；3 events 前提异常——集中存在 notifyEvent*
// 声明其值不是 [a-z_]+ 形态，或发射面守卫命中字面量直发/不可
// 证明来源（均交由门点名）。
package main

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// buildFiles 返回 dir 下（递归或单层）经构建约束过滤的 .go 文件。
// go/build 的 MatchFile 同时处理 //go:build 行与文件名后缀，且对
// _test.go 的处理与编译器一致。
func buildFiles(dir string, recursive bool, wantTests bool) ([]string, error) {
	var out []string
	add := func(path string) error {
		name := filepath.Base(path)
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		isTest := strings.HasSuffix(name, "_test.go")
		if isTest != wantTests {
			return nil
		}
		match, err := build.Default.MatchFile(filepath.Dir(path), name)
		if err != nil {
			return fmt.Errorf("match %s: %w", path, err)
		}
		if match {
			out = append(out, path)
		}
		return nil
	}
	if recursive {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				// 与 go 工具的目录约定对齐（修复轮 8，H-C4 信息
				// 项处置）：testdata 与 _/. 前缀目录永不属于任何
				// 包的编译集——其内 .go 文件既不是产品声明、也不
				// 被 MatchFile 的单文件视角排除，必须在 Walk 层
				// 整目录跳过，否则夹具声明会污染事件普查集。
				if path != dir {
					name := info.Name()
					if name == "testdata" || strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
						return filepath.SkipDir
					}
				}
				return nil
			}
			return add(path)
		})
		if err != nil {
			return nil, err
		}
	} else {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if err := add(filepath.Join(dir, e.Name())); err != nil {
				return nil, err
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func parseFile(path string) (*ast.File, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return f, nil
}

var (
	eventNameRe = regexp.MustCompile(`^notifyEvent[A-Za-z]+$`)
	eventValRe  = regexp.MustCompile(`^[a-z_]+$`)
)

// emitEntries 是通知发射入口方法名 → 事件参数下标。dispatchNotify
// 的事件参数在 ctx 之后（下标 1），其余入口事件居首（下标 0）。
var emitEntries = map[string]int{
	"dispatchNotify": 1,
	"notifySync":     0,
	"notifyTracked":  0,
	"notify":         0,
}

// emitCallForwarders 是机器内转发函数：其体内向 dispatchNotify
// 传的事件来自自身形参/job 字段，来源已在它们各自的入口调用点
// 受守（notifySync 的调用方）或经 notifyJob 字面量受守
// （runNotifyWorker 的 job 来源），故非标识符形态在此豁免。
var emitCallForwarders = map[string]bool{
	"notifySync":      true,
	"runNotifyWorker": true,
}

// emitLitExempt 是复合字面量事件字段的机器内豁免（按字面量类
// 型分）：notifyJob 的 event 字段在 notify/notifyTracked 内由其
// 形参转发；notifyMessage 的 Event 字段在 dispatchNotify 内由
// 其形参组装。两者的入口形参值均在各自调用点受守。
var emitLitExempt = map[string]map[string]bool{
	"notifyJob":     {"notify": true, "notifyTracked": true},
	"notifyMessage": {"dispatchNotify": true},
}

// emitVisitor 在一棵 AST 内守事件发射面（R9-27）：「事件值只来
// 自 notifyEvent* 常量」此前只是隐含前提——发射入口接受任意字
// 符串、配置面对未登记事件同样放行，顺手以字面量直发即产生文
// 档外事件而门恒绿。本守卫把前提落成机制：发射入口调用与
// notifyJob/notifyMessage 字面量的事件值，凡字符串字面量直发
// 一律点名；凡来源不是 notifyEvent* 常量标识符（局部变量、字
// 段、拼接、调用结果）且不在机器内转发/组装函数内，同样点名
// ——新发射面必须把事件落成具名常量再发。
type emitVisitor struct {
	fset     *token.FileSet
	fnStack  []string
	problems *[]string
}

func (v emitVisitor) currentFn() string {
	if len(v.fnStack) == 0 {
		return ""
	}
	return v.fnStack[len(v.fnStack)-1]
}

func (v emitVisitor) classify(pos token.Pos, what string, e ast.Expr, exempt map[string]bool) {
	switch val := e.(type) {
	case *ast.BasicLit:
		if val.Kind == token.STRING {
			*v.problems = append(*v.problems, fmt.Sprintf("%s: %s 以字符串字面量 %s 直发事件，事件值只能来自 notifyEvent* 常量",
				v.fset.Position(pos), what, val.Value))
		}
	case *ast.Ident:
		if eventNameRe.MatchString(val.Name) {
			return
		}
		if exempt[v.currentFn()] {
			return
		}
		*v.problems = append(*v.problems, fmt.Sprintf("%s: %s 的事件来源 %q 不是 notifyEvent* 常量标识符，无法静态证明其值来自常量集",
			v.fset.Position(pos), what, val.Name))
	default:
		if exempt[v.currentFn()] {
			return
		}
		*v.problems = append(*v.problems, fmt.Sprintf("%s: %s 的事件来源不是 notifyEvent* 常量标识符，无法静态证明其值来自常量集",
			v.fset.Position(pos), what))
	}
}

func (v emitVisitor) Visit(n ast.Node) ast.Visitor {
	if n == nil {
		return nil
	}
	if fd, ok := n.(*ast.FuncDecl); ok {
		v.fnStack = append(v.fnStack, fd.Name.Name)
		return v
	}
	switch node := n.(type) {
	case *ast.CallExpr:
		sel, ok := node.Fun.(*ast.SelectorExpr)
		if !ok {
			return v
		}
		idx, isEntry := emitEntries[sel.Sel.Name]
		if !isEntry || len(node.Args) <= idx {
			return v
		}
		v.classify(node.Args[idx].Pos(), "发射入口 "+sel.Sel.Name+" 的事件参数", node.Args[idx], emitCallForwarders)
	case *ast.CompositeLit:
		id, ok := node.Type.(*ast.Ident)
		if !ok {
			return v
		}
		exempt, watched := emitLitExempt[id.Name]
		if !watched {
			return v
		}
		field := "event"
		if id.Name == "notifyMessage" {
			field = "Event"
		}
		for _, elt := range node.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != field {
				continue
			}
			v.classify(kv.Value.Pos(), id.Name+" 字面量的 "+field+" 字段", kv.Value, exempt)
		}
	}
	return v
}

func cmdEvents(dir string) int {
	files, err := buildFiles(dir, true, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	values := map[string]bool{}
	var emitProblems []string
	for _, path := range files {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, fmt.Errorf("parse %s: %w", path, err))
			return 2
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				// 无显式值的规格沿用上一规格的值表达式，不产生新
				// 值，不入集（与集合语义一致）。
				for i, name := range vs.Names {
					if !eventNameRe.MatchString(name.Name) {
						continue
					}
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						fmt.Fprintf(os.Stderr, "notifyEvent 常量 %s 的值不是字符串字面量\n", name.Name)
						return 3
					}
					val, err := strconv.Unquote(lit.Value)
					if err != nil {
						fmt.Fprintf(os.Stderr, "notifyEvent 常量 %s 的值无法解引：%v\n", name.Name, err)
						return 3
					}
					if !eventValRe.MatchString(val) {
						fmt.Fprintf(os.Stderr, "notifyEvent 常量 %s 的值 %q 不是 [a-z_]+ 形态\n", name.Name, val)
						return 3
					}
					values[val] = true
				}
			}
		}
		// 发射面守卫与常量普查同文件集同遍进行（R9-27）。
		ast.Walk(emitVisitor{fset: fset, problems: &emitProblems}, f)
	}
	if len(emitProblems) > 0 {
		for _, p := range emitProblems {
			fmt.Fprintln(os.Stderr, p)
		}
		return 3
	}
	sorted := make([]string, 0, len(values))
	for v := range values {
		sorted = append(sorted, v)
	}
	sort.Strings(sorted)
	for _, v := range sorted {
		fmt.Println(v)
	}
	return 0
}

func cmdTestFns(dir, prefix string) int {
	// 递归（修复轮 9，R9-26）：声称口径是全域（CI 的 go test
	// ./... 会执行子目录包用例），非递归只读顶层目录会让子包
	// 新增用例脱检、门报「实数一致」而实数已漂移。递归的目录
	// 跳过纪律（testdata 与 _/. 前缀）与 events 的 Walk 同口径
	// （R8-11），不入集的目录两面一致。
	files, err := buildFiles(dir, true, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	count := 0
	for _, path := range files {
		f, err := parseFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		for _, decl := range f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil &&
				strings.HasPrefix(fd.Name.Name, prefix) {
				count++
			}
		}
	}
	fmt.Println(count)
	return 0
}

func cmdStrLit(file, want string) int {
	f, err := parseFile(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found || n == nil {
			return !found
		}
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if val, err := strconv.Unquote(lit.Value); err == nil && val == want {
				found = true
				return false
			}
		}
		return true
	})
	if found {
		return 0
	}
	return 1
}

// cmdKeyEmit 是 strlit 的发射点形态（修复轮 9，I-C6-03 处置）：
// strlit 只证「值恰等的字符串字面量在文件中存在」——未用变量持
// 同值字面量一行即满足、不证发射（原 H-C4-06）。doc §1 的前提
// 是「share.go 渲染输出 xhttp-opts」，其真实发射形态是渲染函数
// 把 yamlField{"xhttp-opts", …} 追加进待输出的字段表。本子命令
// 把前提钉到该形态：值为键（复合字面量首元）的 <typeName> 复合
// 字面量、且整段位于 append(...) 调用的实参子树内（嵌套的
// yamlNode{{"host",…}} 同在实参内、同样入集）。未用变量、单独
// 的 yamlField 赋值（未追加）均不满足；删除发射行同样失效。要
// 再造满足点须伪造一条 append 发射调用——构造形态与真实发射
// 同形，其残面按伪造渲染调用计、与收益不成比例，不另设防。
func cmdKeyEmit(file, typeName, want string) int {
	f, err := parseFile(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	found := false
	appendDepth := 0
	var appendStack []bool
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		if n == nil {
			// Inspect 以 nil 回调标记离开节点：与进入时压栈的
			// append 标记配对弹出（见下方压栈约定）。
			if len(appendStack) > 0 {
				if appendStack[len(appendStack)-1] {
					appendDepth--
				}
				appendStack = appendStack[:len(appendStack)-1]
			}
			return false
		}
		isAppendCall := false
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "append" {
				isAppendCall = true
			}
		}
		appendStack = append(appendStack, isAppendCall)
		if isAppendCall {
			appendDepth++
		}
		if appendDepth > 0 {
			if lit, ok := n.(*ast.CompositeLit); ok && len(lit.Elts) > 0 {
				if id, ok := lit.Type.(*ast.Ident); ok && id.Name == typeName {
					if key, ok := lit.Elts[0].(*ast.BasicLit); ok &&
						key.Kind == token.STRING {
						if val, err := strconv.Unquote(key.Value); err == nil &&
							val == want {
							found = true
							return false
						}
					}
				}
			}
		}
		return true
	})
	if found {
		return 0
	}
	return 1
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: doc-extract <events|testfns|strlit|keyemit> ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "events":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: doc-extract events <dir>")
			os.Exit(2)
		}
		os.Exit(cmdEvents(os.Args[2]))
	case "testfns":
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: doc-extract testfns <dir> <prefix>")
			os.Exit(2)
		}
		os.Exit(cmdTestFns(os.Args[2], os.Args[3]))
	case "strlit":
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: doc-extract strlit <file> <value>")
			os.Exit(2)
		}
		os.Exit(cmdStrLit(os.Args[2], os.Args[3]))
	case "keyemit":
		if len(os.Args) != 5 {
			fmt.Fprintln(os.Stderr, "usage: doc-extract keyemit <file> <type> <value>")
			os.Exit(2)
		}
		os.Exit(cmdKeyEmit(os.Args[2], os.Args[3], os.Args[4]))
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		os.Exit(2)
	}
}
