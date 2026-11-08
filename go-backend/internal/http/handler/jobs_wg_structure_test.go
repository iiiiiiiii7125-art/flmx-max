package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

// TestJobsWGAdjacentAddOneSentinel 是 PD-23 的结构哨兵：StartBackgroundJobs
// 的 jobsWG 计数必须保持「启动点相邻 Add(1)」形态——每个 go h.run* 语句
// 的紧邻前一条语句必须是 h.jobsWG.Add(1)，且函数内不得出现任何其他
// jobsWG.Add 调用（尤其写死总数的 Add(N)）。本测试只断言结构与配对
// 相等，不钉循环总数：增删循环时同步增删其相邻 Add(1) 即可，计数
// 漂移在结构上不可能，测试也不随成员数过期。
func TestJobsWGAdjacentAddOneSentinel(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), "jobs.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse jobs.go: %v", err)
	}

	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "StartBackgroundJobs" {
			body = fn.Body
			break
		}
	}
	if body == nil {
		t.Fatal("StartBackgroundJobs not found in jobs.go")
	}

	isJobsWGAdd := func(stmt ast.Stmt) (arg string, isAdd bool) {
		expr, ok := stmt.(*ast.ExprStmt)
		if !ok {
			return "", false
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok {
			return "", false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Add" {
			return "", false
		}
		inner, ok := sel.X.(*ast.SelectorExpr)
		if !ok || inner.Sel.Name != "jobsWG" {
			return "", false
		}
		if len(call.Args) != 1 {
			return "", true
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.INT {
			return lit.Value, true
		}
		return "", true
	}

	stmts := body.List
	goCount, addCount := 0, 0
	for i, stmt := range stmts {
		if arg, isAdd := isJobsWGAdd(stmt); isAdd {
			addCount++
			if arg != "1" {
				t.Errorf("statement %d: jobsWG.Add(%s) breaks the adjacent Add(1) form", i, arg)
			}
			continue
		}
		if _, isGo := stmt.(*ast.GoStmt); isGo {
			goCount++
			if i == 0 {
				t.Errorf("statement %d: go statement has no preceding jobsWG.Add(1)", i)
				continue
			}
			if arg, isAdd := isJobsWGAdd(stmts[i-1]); !isAdd || arg != "1" {
				t.Errorf("statement %d: go statement is not immediately preceded by jobsWG.Add(1)", i)
			}
		}
	}
	if goCount == 0 {
		t.Fatal("no go statements found in StartBackgroundJobs — the sentinel itself is broken")
	}
	if addCount != goCount {
		t.Errorf("jobsWG.Add(1) count %d != go statement count %d in StartBackgroundJobs", addCount, goCount)
	}
}
