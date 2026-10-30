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

// k9PortDecisionPoints 是选口/判重点位闭合清单（正向）：
// 文件 → 函数 → 函数体必须出现的统一入口名。
var k9PortDecisionPoints = []struct {
	file string
	fn   string
	want string
}{
	{"repository_mutations.go", "pickNodePortTx", "UsedPortsOnNodeTx"},
	{"repository_mutations.go", "GetUsedPortsOnNodeAsMap", "UsedPortsOnNodeTx"},
	{"repository_federation.go", "ListUsedPortsOnNode", "UsedPortsOnNodeTx"},
	{"repository_control.go", "HasOtherForwardOnNodePortTx", "NodePortOccupiedTx"},
	{"repository_control.go", "NodePortOccupiedForTunnelTx", "NodePortOccupiedTx"},
	{"repository_kernel.go", "NodePortRangeOccupant", "NodePortRangeOccupancyTx"},
	{"repository_backup_precheck.go", "precheckBackupPortConflicts", "NodePortOccupantTx"},
	{"repository_nft_pending_delete.go", "FindOtherForwardOnNodePortTx", "NodePortOccupantTx"},
}

// k9PortQueryAllowlist 是负向扫描的豁免清单（函数名 → 理由）。这些
// 函数含 node+port 决策形查询，但语义上不做「端口是否可分配」的
// 判定：要么只按已判定来源补取详情，要么是联邦源内部的子集守卫/
// 清单（只能在统一判重之外多拒、不能放行统一谓词禁止的口）。
var k9PortQueryAllowlist = map[string]string{
	"FindOtherForwardOnNodePortTx":           "详情补取：来源判定走 NodePortOccupantTx（正向清单已锁），本函数只按来源取名称/状态",
	"ExistsActivePeerShareRuntimeOnNodePort": "联邦子集守卫：federation tunnel/create 在统一已用集判重之外叠加，只会多拒不会放行",
	"ListActivePeerShareRuntimePorts":        "联邦子集清单：pickPeerSharePort 在统一已用集之外叠加排除本分享自有预留",
	"ListActiveForwardPortsForNode":          "节点页展示清单：只读列出本节点转发口，不做分配决策",
}

type k9FuncBody struct {
	file string
	name string
	body string
}

func k9LoadPackageFuncs(t *testing.T) []k9FuncBody {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var out []k9FuncBody
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(".", name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			start := fset.Position(fd.Body.Pos()).Offset
			end := fset.Position(fd.Body.End()).Offset
			out = append(out, k9FuncBody{file: name, name: fd.Name.Name, body: string(src[start:end])})
		}
	}
	return out
}

// k9DecisionShaped 判断函数体是否含「按 node+port 查占用表」的决策形
// 查询：引用四张占用表之一、带 node_id 条件与端口条件、且是读查询。
func k9DecisionShaped(body string) bool {
	modelHit := false
	for _, m := range []string{"model.ForwardPort{}", "model.ChainTunnel{}", "model.NodeInbound{}", "model.PeerShareRuntime{}"} {
		if strings.Contains(body, m) {
			modelHit = true
			break
		}
	}
	if !modelHit || !strings.Contains(body, "node_id = ?") {
		return false
	}
	portHit := false
	for _, p := range []string{"port = ?", "port > 0", "port BETWEEN"} {
		if strings.Contains(body, p) {
			portHit = true
			break
		}
	}
	if !portHit {
		return false
	}
	for _, v := range []string{".Count(", ".Pluck(", ".Find(", ".Scan(", ".First("} {
		if strings.Contains(body, v) {
			return true
		}
	}
	return false
}

func TestK9PortOccupancySentinel(t *testing.T) {
	funcs := k9LoadPackageFuncs(t)
	byKey := make(map[string]k9FuncBody, len(funcs))
	for _, fn := range funcs {
		byKey[fn.file+"::"+fn.name] = fn
	}

	// 正向：每个登记点位必须走统一谓词。
	for _, pt := range k9PortDecisionPoints {
		fn, ok := byKey[pt.file+"::"+pt.fn]
		if !ok {
			t.Fatalf("P01-001 sentinel: decision point %s::%s not found (renamed? update the closed list)", pt.file, pt.fn)
		}
		if !strings.Contains(fn.body, pt.want) {
			t.Fatalf("P01-001 sentinel: %s::%s must route through %s (unified four-source predicate), body does not reference it", pt.file, pt.fn, pt.want)
		}
	}

	// 负向：未登记函数不得出现决策形占用查询。
	for _, fn := range funcs {
		if fn.file == "repository_port_occupancy.go" {
			continue
		}
		if _, exempt := k9PortQueryAllowlist[fn.name]; exempt {
			continue
		}
		if k9DecisionShaped(fn.body) {
			t.Fatalf("P01-001 sentinel: %s::%s contains a hand-rolled node+port occupancy query over the shared port tables; route it through the unified predicate in repository_port_occupancy.go or register an exemption with rationale", fn.file, fn.name)
		}
	}
}
