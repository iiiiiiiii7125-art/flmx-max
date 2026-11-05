package repo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// B4-05 残面: 哨兵谓词自身的变异测试。此前 port 条件表只收带
// 空格形态, 紧凑写法 (port=?/port>=?) 与 .Take( 组合可整体绕过
// 负向臂而无人察觉——本组把谓词当被测对象: 合成源码逐形态断言
// 判形结果, 谓词再退化时先在这里变红。

func k9ParseFunc(t *testing.T, src string) *ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", "package synthetic\n"+src, 0)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok {
			return fd
		}
	}
	t.Fatal("no func decl in synthetic source")
	return nil
}

func TestK9DecisionShapedPredicateVariants(t *testing.T) {
	cases := []struct {
		name string
		cond string
		verb string
		want bool
	}{
		{"带空格等值 + Count（原覆盖）", "port = ?", "Count", true},
		{"紧凑等值 port=? + Take", "port=?", "Take", true},
		{"紧凑大于等于 port>=? + Count", "port>=?", "Count", true},
		{"紧凑不等 port<>? + Find", "port<>?", "Find", true},
		{"紧凑 IN port IN(?) + Pluck", "port IN(?)", "Pluck", true},
		{"紧凑 BETWEEN + Scan", "port BETWEEN ? AND ?", "Scan", true},
		{"裸列名 Pluck(port) 非条件", "port", "Pluck", false},
		{"长列名 port_range_start 非条件", "port_range_start = ?", "Count", false},
		{"无 port 条件不判形", "status = ?", "Count", false},
		{"非决策动词 (Exec) 不判形", "port=?", "Exec", false},
	}
	for _, tc := range cases {
		src := "func probe(db DB) {\n\t_ = db.Model(&model.ForwardPort{}).Where(\"node_id = ? AND " + tc.cond + "\", 1, 2)." + tc.verb + "(&out)\n}\n"
		if got := k9DecisionShaped(k9ParseFunc(t, src)); got != tc.want {
			t.Fatalf("%s: k9DecisionShaped = %v, want %v (src: %s)", tc.name, got, tc.want, src)
		}
	}

	// 无占用表 model 引用时不判形（谓词三要素缺一不可）。
	src := "func probe(db DB) {\n\t_ = db.Model(&model.User{}).Where(\"node_id = ? AND port=?\", 1, 2).Count(&out)\n}\n"
	if k9DecisionShaped(k9ParseFunc(t, src)) {
		t.Fatal("query without an occupancy-table model must not be decision-shaped")
	}
}
