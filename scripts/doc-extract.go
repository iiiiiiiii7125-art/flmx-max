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
//   doc-extract events <dir>           递归枚举 dir 下非测试 .go
//                                      文件中包级 notifyEvent* 字
//                                      符串常量/变量的值集（排序
//                                      去重、逐行输出）
//   doc-extract testfns <dir> <prefix> 统计 dir 下 *_test.go 顶层
//                                      函数名以 prefix 开头的数量
//   doc-extract strlit <file> <value>  file 中存在字符串字面量其
//                                      值恰为 value 时退出 0
// 退出码：0 成功（strlit 为「找到」）；1 strlit 未找到；
// 2 用法/IO/解析错误；3 events 集中存在 notifyEvent* 声明其值
// 不是 [a-z_]+ 形态（前提异常，交由门点名）。
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

func cmdEvents(dir string) int {
	files, err := buildFiles(dir, true, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	values := map[string]bool{}
	for _, path := range files {
		f, err := parseFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
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
	files, err := buildFiles(dir, false, true)
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

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: doc-extract <events|testfns|strlit> ...")
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
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		os.Exit(2)
	}
}
