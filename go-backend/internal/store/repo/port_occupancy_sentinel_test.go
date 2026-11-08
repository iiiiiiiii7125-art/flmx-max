package repo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Round 2 K9 / P01-001 枚举哨兵：节点端口的全部选口/判重点位必须走
// 统一四源谓词（repository_port_occupancy.go）。X-P2-13 的教训是同族
// 只修了两个面、其余三面手写集合继续漏源——本测试把点位全集钉成
// 清单：正向断言每个已知点位函数体内确实调用统一入口；负向断言
// 任何未登记函数不得出现「按 node+port 查四张占用表」的决策形查询
// （新增点位必须先在此登记并改走统一谓词，或写明豁免理由）。
//
// R1-03-M3 重写负向臂：旧实现用裸子串（`.Count(` 等）在函数体文本
// 上判形，而本仓 GORM 链式调用的主导格式是点号留行尾、动词在下
// 一行行首，源码文本里根本不存在 `.Count(` 子串——按旧逻辑全仓
// 复算仅 1 个函数可命中，负向臂名存实亡。现改 AST 谓词：函数体
// 内同时出现 (a) 四张占用表之一的 model 引用、(b) 含 node_id 与
// port 的条件字符串、(c) 决策动词调用（Count/Pluck/Find/Scan/
// First/Take）即判决策形，与源码排版无关。正向臂同步改 AST：
// 登记入口名必须作为真实调用出现，注释文本不再能满足断言。
// 扫描范围扩到 handler 包（R1C-03：选口/判重决策点大量在
// handler 层，此前不在锁定面；handler 对四表无直查是结构事实，
// 本哨兵把该事实钉成门）。

// k9PortDecisionPoints 是选口/判重点位闭合清单（正向）：
// 文件 → 函数 → 函数体必须真实调用的统一入口名。
var k9PortDecisionPoints = []struct {
	file string
	fn   string
	want string
}{
	{"repository_mutations.go", "pickNodePortTx", "UsedPortsOnNodeTx"},
	{"repository_mutations.go", "GetUsedPortsOnNodeAsMap", "UsedPortsOnNodeTx"},
	{"repository_port_occupancy.go", "ListUsedPortsOnNode", "UsedPortsOnNodeTx"},
	{"repository_control.go", "HasOtherForwardOnNodePortTx", "NodePortOccupiedTx"},
	{"repository_control.go", "NodePortOccupiedForTunnelTx", "NodePortOccupiedTx"},
	{"repository_kernel.go", "NodePortRangeOccupant", "NodePortRangeOccupancyTx"},
	{"repository_backup_precheck.go", "precheckBackupPortConflicts", "NodePortOccupantTx"},
	{"repository_nft_pending_delete.go", "FindOtherForwardOnNodePortTx", "NodePortOccupantTx"},
	// R1C-02 补登：此前未锁的真实决策/消费点。
	{"repository_mutations.go", "replaceForwardPortsTx", "HasOtherForwardOnNodePortTx"},
	{"repository_mutations.go", "CreateForwardTx", "HasOtherForwardOnNodePortTx"},
	{"repository_kernel.go", "NodePortOccupant", "NodePortRangeOccupant"},
	{"repository_port_occupancy.go", "TunnelChainPortReusableTx", "NodePortOccupiedTx"},
}

// k9PortQueryAllowlist 是负向扫描的豁免清单（函数名 → 理由）。这些
// 函数含 node+port 决策形查询，但语义上不做「端口是否可分配」的
// 判定：只按已判定来源补取详情、或只读展示既有占用。
var k9PortQueryAllowlist = map[string]string{
	"FindOtherForwardOnNodePortTx":  "详情补取：来源判定走 NodePortOccupantTx（正向清单已锁），本函数只按来源取名称/状态",
	"ListActiveForwardPortsForNode": "节点页展示清单：只读列出本节点转发口，不做分配决策",
	"getTunnelEntryPortRanges":      "展示聚合：按隧道读既有链路/入站端口聚合 min/max 供列表渲染，不做分配决策",
}

// k9HandlerQueryAllowlist 是 handler 包负向扫描的豁免清单。handler
// 层的选口/判重一律经 repo 统一入口，原则上不应出现对三表的直查
// 决策形；确有合法直查时在此点名登记理由。
var k9HandlerQueryAllowlist = map[string]string{}

type k9Func struct {
	file string
	name string
	decl *ast.FuncDecl
}

func k9LoadDirFuncs(t *testing.T, dir string) []k9Func {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	var out []k9Func
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			out = append(out, k9Func{file: name, name: fd.Name.Name, decl: fd})
		}
	}
	return out
}

// k9CallName 取调用表达式的被调名（标识符或选择器末段），非简单
// 形态返回 ""。
func k9CallName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.ParenExpr:
		return k9CallName(f.X)
	}
	return ""
}

var k9OccupancyModels = map[string]bool{
	"ForwardPort": true,
	"ChainTunnel": true,
	"NodeInbound": true,
}

var k9DecisionVerbs = map[string]bool{
	"Count": true,
	"Pluck": true,
	"Find":  true,
	"Scan":  true,
	"First": true,
	"Take":  true,
}

// k9CallsFunc 报告函数体内是否真实调用了名为 want 的函数/方法
// （AST 判定，注释与字符串文本不算数）。
func k9CallsFunc(fd *ast.FuncDecl, want string) bool {
	found := false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok && k9CallName(call.Fun) == want {
			found = true
			return false
		}
		return true
	})
	return found
}

// k9DecisionShaped 以 AST 判函数体是否含「按 node+port 查占用表」
// 的决策形查询：占用表 model 引用、node_id 与 port 条件字面量、
// 决策动词调用三者齐备才判形（与源码排版无关）。
func k9DecisionShaped(fd *ast.FuncDecl) bool {
	modelHit, nodeCond, portCond, verbHit := false, false, false, false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			if ident, ok := node.X.(*ast.Ident); ok && ident.Name == "model" && k9OccupancyModels[node.Sel.Name] {
				modelHit = true
			}
		case *ast.BasicLit:
			if node.Kind == token.STRING {
				if strings.Contains(node.Value, "node_id") {
					nodeCond = true
				}
				// 端口条件须是条件形态（port 后跟比较/集合运算符）；
				// 裸列名（Pluck("port")、列清单里的
				// "port_range_start"）不算条件，避免误伤。
				// B4-05 残面：紧凑写法（port=?、port>=?、port<>?）
				// 此前因条件表只收带空格形态而整体逃逸本哨兵，现
				// 由 portConditionShaped 按边界＋前瞻运算符判形。
				if portConditionShaped(node.Value) {
					portCond = true
				}
			}
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && k9DecisionVerbs[sel.Sel.Name] {
				verbHit = true
			}
		}
		return true
	})
	return modelHit && nodeCond && portCond && verbHit
}

// portConditionShaped 判条件字面量是否含 port 条件形态：在标
// 识符边界处（前一字符非 [a-z0-9_]，排除 airport/transport 等
// 长标识符内部与 _port 后缀列）出现 "port"，其后跳过空白紧跟
// 比较运算符（=、>、<、!）或集合关键字（IN、BETWEEN）。带空格
// 与紧凑写法同判；裸列名与 port_range_* 长列名不判形（运算符
// 不紧贴 port 本体）。
// 已知不可覆盖形态（列遗留，勿当已锁）：条件串由变量/拼接构
// 成（非字面量）、结构体/映射形态的 Where（无条件字面量）、
// Raw/Exec 拼 SQL——这些需靠评审与正向清单兜底。
func portConditionShaped(lit string) bool {
	for i := 0; i+4 <= len(lit); i++ {
		if lit[i:i+4] != "port" {
			continue
		}
		if i > 0 {
			if c := lit[i-1]; c == '_' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
				continue
			}
		}
		rest := strings.TrimLeft(lit[i+4:], " \t\r\n")
		if rest == "" {
			continue
		}
		switch rest[0] {
		case '=', '>', '<', '!':
			return true
		}
		if strings.HasPrefix(rest, "IN") || strings.HasPrefix(rest, "BETWEEN") {
			return true
		}
	}
	return false
}

func TestK9PortOccupancySentinel(t *testing.T) {
	funcs := k9LoadDirFuncs(t, ".")
	byKey := make(map[string]k9Func, len(funcs))
	for _, fn := range funcs {
		byKey[fn.file+"::"+fn.name] = fn
	}

	// 正向：每个登记点位必须真实调用统一谓词。
	for _, pt := range k9PortDecisionPoints {
		fn, ok := byKey[pt.file+"::"+pt.fn]
		if !ok {
			t.Fatalf("P01-001 sentinel: decision point %s::%s not found (renamed? update the closed list)", pt.file, pt.fn)
		}
		if !k9CallsFunc(fn.decl, pt.want) {
			t.Fatalf("P01-001 sentinel: %s::%s must route through %s (unified four-source predicate), body does not call it", pt.file, pt.fn, pt.want)
		}
	}

	// 负向（repo 包）：未登记函数不得出现决策形占用查询。
	for _, fn := range funcs {
		if fn.file == "repository_port_occupancy.go" {
			continue
		}
		if _, exempt := k9PortQueryAllowlist[fn.name]; exempt {
			continue
		}
		if k9DecisionShaped(fn.decl) {
			t.Fatalf("P01-001 sentinel: %s::%s contains a hand-rolled node+port occupancy query over the shared port tables; route it through the unified predicate in repository_port_occupancy.go or register an exemption with rationale", fn.file, fn.name)
		}
	}

	// 负向（handler 包）：选口/判重决策点不得绕开 repo 统一入口
	// 对四表直查（R1C-03 扩扫）。
	handlerFuncs := k9LoadDirFuncs(t, filepath.Join("..", "..", "http", "handler"))
	for _, fn := range handlerFuncs {
		if _, exempt := k9HandlerQueryAllowlist[fn.name]; exempt {
			continue
		}
		if k9DecisionShaped(fn.decl) {
			t.Fatalf("P01-001 sentinel: handler %s::%s contains a hand-rolled node+port occupancy query over the shared port tables; route it through the repo unified predicate in repository_port_occupancy.go or register an exemption with rationale", fn.file, fn.name)
		}
	}
}
