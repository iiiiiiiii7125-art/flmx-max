package handler

// R3-MR-01 登记门：repo/纯读聚合 helper 的「读错误被吞」常设门禁。
//
// 机制谓词：handler 包内，一次 repo 读调用（或错误透明的纯读聚合
// helper 调用）的 error 结果位被 `_` 丢弃，使调用方拿空集/零值
// 继续成功路径。此形态曾造成：凭据轮换读失败装已下发（旧凭据永
// 续）、隧道更新读失败误清全部旧链运行时、转发创建空入口集落库、
// 订阅拉取面读失败渲染空订阅、remoteSourceSave 读失败 nil 解引用。
//
// 门的推导（全部机械、不依赖登记点清单）：
//  1. 从 repo 包 AST 枚举 *Repository 全部导出方法，按「结果数 ≥2
//     且末位为 error」＋「函数体无写证据」分类出 READ 集；写证据＝
//     gorm 写动词选择器调用 ∪ 调用已分类为写的方法/包级函数（不动点
//     迭代）。名称与体证据冲突的方法（读名写体 / 写名读体）门红、
//     必须在 readSwallowRepoOverrides 里逐个写明裁定与理由。
//  2. 从 handler 包 AST 推导纯读聚合 helper 集：结果数 ≥2 末位
//     error、未失格、且体内有 READ 调用（或调用已合格 helper）。
//     失格＝直接调用 READ 集外的 repo 方法、或调用已失格函数，
//     沿调用边不动点传播——经写面 helper 干活的函数（如
//     syncRealmNodes 经 ensureRealmKernel/applyKernelWith 下发）
//     不是纯读聚合，其调用点不进本门派生集（边界见 B 报告）。
//  3. 扫描 handler 包全部非测试 .go 的赋值语句：READ 方法或合格
//     helper 的调用，其 error 位（多值调用的最后一个左值）为 `_`
//     即一个派生点，键＝(文件, 所在函数, 被调名)，同键计数。
//  4. 派生集必须与 read_error_swallow_gate_registry.tsv 登记册
//     「精确相等」（含计数与非空理由）：派生出未登记点＝新吞错点
//     或旧点回退，红；登记册有派生不到的行＝登记册腐烂，红。
//
// 修复点修好后自动退出派生集（不再入册）；有人把修复改回吞错
// 形态即以「未登记点」翻红。自检：TestReadErrorSwallowGateSynthetic
// 用内存合成源码常驻验活推导管线（吞错必抓、查错不抓、写调用不抓、
// helper 吞错必抓）。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ---------- 基础设施 ----------

func parseDirGoFiles(t *testing.T, dir string, includeTests bool) map[string]*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if !includeTests && strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		out[name] = f
	}
	if len(out) == 0 {
		t.Fatalf("no go files parsed in %s", dir)
	}
	return out
}

func parseSources(t *testing.T, srcs map[string]string) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	for name, src := range srcs {
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse synthetic %s: %v", name, err)
		}
		out[name] = f
	}
	return out
}

// resultShape 返回函数类型的 (结果数, 末位是否为 error)。
func resultShape(ft *ast.FuncType) (int, bool) {
	if ft == nil || ft.Results == nil {
		return 0, false
	}
	n := 0
	lastErr := false
	for _, field := range ft.Results.List {
		cnt := len(field.Names)
		if cnt == 0 {
			cnt = 1
		}
		n += cnt
		ident, ok := field.Type.(*ast.Ident)
		lastErr = ok && ident.Name == "error"
	}
	return n, lastErr
}

func receiverTypeName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	expr := fd.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// selectorChain 返回选择器链的逐段名（如 h.repo.GetX →
// [h repo GetX]；非选择器表达式返回 nil）。
func selectorChain(expr ast.Expr) []string {
	var parts []string
	for {
		switch e := expr.(type) {
		case *ast.SelectorExpr:
			parts = append([]string{e.Sel.Name}, parts...)
			expr = e.X
		case *ast.Ident:
			parts = append([]string{e.Name}, parts...)
			return parts
		default:
			return nil
		}
	}
}

// ---------- repo 方法分类 ----------

// gorm 写动词：一元证据（在 repo 包内出现这些选择器调用即视为有
// 写副作用；与名称冲突时由 overrides 裁定，见 classifyRepoMethods）。
var repoWriteSelectors = map[string]bool{
	"Create": true, "CreateInBatches": true, "Save": true,
	"Update": true, "Updates": true, "UpdateColumn": true,
	"UpdateColumns": true, "Delete": true, "Exec": true,
	"FirstOrCreate": true, "Assign": true, "Begin": true,
	"Commit": true, "Rollback": true, "RollbackTo": true,
	"SavePoint": true,
}

var repoWriteNamePrefixes = []string{
	"Create", "Insert", "Update", "Upsert", "Save", "Delete", "Remove",
	"Set", "Mark", "Reset", "Record", "Increment", "Decrement", "Clear",
	"Purge", "Add", "Ensure", "Rotate", "Consume", "Claim", "Replace",
	"Sync", "Apply", "Store", "Write", "Toggle", "Assign", "Bind",
	"Revoke", "Grant", "Drop", "Touch", "Bump",
}

var repoReadNamePrefixes = []string{
	"Get", "List", "Find", "Count", "Exists", "Load", "Fetch", "Has",
	"Sum", "Lookup", "Search",
}

func hasAnyPrefix(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// readSwallowRepoOverrides：名称/体证据冲突方法的逐个裁定。
// 键＝方法名；值＝裁定理由。分类结果：
//   - 条目写 "READ" 前缀：体证据未见写、名称像写，经人工核实为真
//     读，纳入 READ 集。
//   - 条目写 "WRITE" 前缀：名称像读但体证据有写（如懒创建），留在
//     READ 集外（安全侧）。
var readSwallowRepoOverrides = map[string]string{}

// bodyCalls 汇总一个函数体内的调用：选择器尾名集合 + repo 方法
// 调用名集合（链含 repo 段）+ 包级函数调用名集合。
func bodyCalls(body *ast.BlockStmt) (selNames map[string]bool, repoCalls map[string]bool, plainCalls map[string]bool) {
	selNames = map[string]bool{}
	repoCalls = map[string]bool{}
	plainCalls = map[string]bool{}
	if body == nil {
		return
	}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			selNames[fun.Sel.Name] = true
			chain := selectorChain(fun)
			for i := 0; i+1 < len(chain); i++ {
				if chain[i] == "repo" {
					repoCalls[chain[i+1]] = true
				}
			}
		case *ast.Ident:
			plainCalls[fun.Name] = true
		}
		return true
	})
	return
}

// classifyRepoMethods 从 repo 包文件集分类出 READ 方法集。
// 返回 (readSet, totalExported, conflicts)。conflicts 为名称/体证据
// 冲突且无 override 的方法名——非空即门红。
func classifyRepoMethods(files map[string]*ast.File) (map[string]bool, int, []string) {
	type methodInfo struct {
		nres     int
		lastErr  bool
		exported bool
		body     *ast.BlockStmt
	}
	methods := map[string]*methodInfo{}
	pkgFuncs := map[string]*ast.BlockStmt{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fd.Recv == nil {
				pkgFuncs[fd.Name.Name] = fd.Body
				continue
			}
			if receiverTypeName(fd) != "Repository" {
				continue
			}
			nres, lastErr := resultShape(fd.Type)
			methods[fd.Name.Name] = &methodInfo{
				nres: nres, lastErr: lastErr,
				exported: ast.IsExported(fd.Name.Name), body: fd.Body,
			}
		}
	}
	// 写证据不动点：包级函数与方法统一迭代。证据三源：gorm 写动词
	// 选择器；调用已判定的写函数（plain 标识）；以任意接收者调用已
	// 判定的写方法（repo 内部互调走 r. 链、无 repo 段，只能按选择器
	// 尾名传播——尾名与 gorm 读动词同名时只会把写判定传给「调用了
	// 某写方法名」的函数，而写方法名集合本身由写动词证据锚定，
	// 且最终还有名称/体证据冲突检查兜底）。
	writeFuncs := map[string]bool{}
	writeMethods := map[string]bool{}
	for changed := true; changed; {
		changed = false
		mark := func(set map[string]bool, name string) {
			if !set[name] {
				set[name] = true
				changed = true
			}
		}
		probe := func(body *ast.BlockStmt) bool {
			sel, _, plain := bodyCalls(body)
			for s := range sel {
				if repoWriteSelectors[s] || writeMethods[s] {
					return true
				}
			}
			for p := range plain {
				if writeFuncs[p] {
					return true
				}
			}
			return false
		}
		for name, body := range pkgFuncs {
			if !writeFuncs[name] && probe(body) {
				mark(writeFuncs, name)
			}
		}
		for name, mi := range methods {
			if !writeMethods[name] && probe(mi.body) {
				mark(writeMethods, name)
			}
		}
	}
	readSet := map[string]bool{}
	var conflicts []string
	total := 0
	for name, mi := range methods {
		if !mi.exported {
			continue
		}
		total++
		if mi.nres < 2 || !mi.lastErr {
			continue
		}
		isWrite := writeMethods[name]
		ov, hasOv := readSwallowRepoOverrides[name]
		switch {
		case !isWrite && hasAnyPrefix(name, repoWriteNamePrefixes):
			// 读体写名：须 override 裁定为 READ。
			if hasOv && strings.HasPrefix(ov, "READ") {
				readSet[name] = true
			} else {
				conflicts = append(conflicts, name+"（写名读体，未裁定）")
			}
		case isWrite && hasAnyPrefix(name, repoReadNamePrefixes):
			// 写体读名：须 override 裁定（WRITE=留在集外）。
			if !hasOv {
				conflicts = append(conflicts, name+"（读名写体，未裁定）")
			}
		case !isWrite:
			readSet[name] = true
		}
	}
	sort.Strings(conflicts)
	return readSet, total, conflicts
}

// ---------- handler 纯读 helper 资格 ----------

// qualifyReadHelpers 推导 handler 包内错误透明的纯读聚合 helper 集。
//
// 资格规则（在全包函数图上不动点推导）：
//   - 失格：函数体内直接调用任何 READ 集之外的 repo 方法，或调用
//     任何已失格的 handler 函数。失格沿调用边传播——经写面 helper
//     （如 ensureRealmKernel/applyKernelWith 下发）干活的函数不是
//     纯读聚合，哪怕它自己一行 repo 调用都没有（syncRealmNodes
//     即此类：realm 同步是写面，不进本门谓词）。
//   - 合格（且须满足候选形状：结果数 ≥2、末位 error）：未失格，且
//     体内有直接 READ 调用或调用已合格 helper。
//   - 中立：完全不触达 repo 的纯函数不影响资格（格式化/集合运算等）。
//
// 函数按名建表（同名方法跨接收者合并为并集——本包内方法名基本
// 唯一，合并方向偏保守：任一同名体失格则全失格）。
func qualifyReadHelpers(files map[string]*ast.File, readSet map[string]bool) map[string]bool {
	bodies := map[string]*ast.BlockStmt{}
	cands := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			name := fd.Name.Name
			if bodies[name] == nil {
				bodies[name] = fd.Body
			} else {
				// 同名合并：把第二个函数体也纳入分析（拼进同一节点
				// 的调用集近似——用一个合成块承接两个体的语句）。
				bodies[name] = &ast.BlockStmt{List: append(bodies[name].List, fd.Body.List...)}
			}
			nres, lastErr := resultShape(fd.Type)
			if nres >= 2 && lastErr {
				cands[name] = true
			}
		}
	}
	type fnCalls struct {
		repo  map[string]bool
		funcs map[string]bool
	}
	calls := map[string]*fnCalls{}
	for name, body := range bodies {
		sel, repoC, plain := bodyCalls(body)
		fc := &fnCalls{repo: repoC, funcs: map[string]bool{}}
		for s := range sel {
			if bodies[s] != nil {
				fc.funcs[s] = true
			}
		}
		for p := range plain {
			if bodies[p] != nil {
				fc.funcs[p] = true
			}
		}
		calls[name] = fc
	}
	// 失格传播。
	disq := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for name, fc := range calls {
			if disq[name] {
				continue
			}
			bad := false
			for c := range fc.repo {
				if !readSet[c] {
					bad = true
				}
			}
			for g := range fc.funcs {
				if disq[g] {
					bad = true
				}
			}
			if bad {
				disq[name] = true
				changed = true
			}
		}
	}
	// 合格传播（仅候选形状）。
	qualified := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for name := range cands {
			if qualified[name] || disq[name] {
				continue
			}
			fc := calls[name]
			ok := false
			for c := range fc.repo {
				if readSet[c] {
					ok = true
				}
			}
			for g := range fc.funcs {
				if qualified[g] {
					ok = true
				}
			}
			if ok {
				qualified[name] = true
				changed = true
			}
		}
	}
	return qualified
}

// ---------- 吞错点扫描 ----------

type readSwallowSite struct {
	File   string
	Func   string
	Callee string
}

func (s readSwallowSite) String() string {
	return s.File + "\t" + s.Func + "\t" + s.Callee
}

// scanReadSwallowSites 扫描 handler 文件集中的派生点（键→次数）。
func scanReadSwallowSites(files map[string]*ast.File, readSet, helpers map[string]bool) map[readSwallowSite]int {
	sites := map[readSwallowSite]int{}
	calleeOf := func(expr ast.Expr) (string, bool) {
		call, ok := expr.(*ast.CallExpr)
		if !ok {
			return "", false
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			chain := selectorChain(fun)
			for i := 0; i+1 < len(chain); i++ {
				if chain[i] == "repo" && readSet[chain[i+1]] {
					return chain[i+1], true
				}
			}
			if helpers[fun.Sel.Name] {
				return fun.Sel.Name, true
			}
		case *ast.Ident:
			if helpers[fun.Name] {
				return fun.Name, true
			}
		}
		return "", false
	}
	for fname, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok || len(assign.Rhs) != 1 || len(assign.Lhs) < 2 {
					return true
				}
				callee, isCand := calleeOf(assign.Rhs[0])
				if !isCand {
					return true
				}
				last := assign.Lhs[len(assign.Lhs)-1]
				if ident, ok := last.(*ast.Ident); ok && ident.Name == "_" {
					sites[readSwallowSite{File: fname, Func: fd.Name.Name, Callee: callee}]++
				}
				return true
			})
		}
	}
	return sites
}

func deriveReadSwallowSites(handlerFiles, repoFiles map[string]*ast.File) (map[readSwallowSite]int, map[string]bool, int, []string) {
	readSet, total, conflicts := classifyRepoMethods(repoFiles)
	helpers := qualifyReadHelpers(handlerFiles, readSet)
	return scanReadSwallowSites(handlerFiles, readSet, helpers), helpers, total, conflicts
}

// ---------- 主门 ----------

const readSwallowRegistryName = "read_error_swallow_gate_registry.tsv"

func TestReadErrorSwallowGate(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	handlerFiles := parseDirGoFiles(t, wd, false)
	repoFiles := parseDirGoFiles(t, filepath.Join(wd, "..", "..", "store", "repo"), false)

	readSet, total, conflicts := classifyRepoMethods(repoFiles)
	if len(conflicts) > 0 {
		t.Fatalf("repo 方法名称/体证据冲突未裁定（在 readSwallowRepoOverrides 逐个登记裁定）：\n  %s", strings.Join(conflicts, "\n  "))
	}
	// 防假绿锚点：分类面塌陷（解析失败/方法面骤减）必须红而不是
	// 以空集「通过」。基数按立门时实测（方法 417 / READ 集 190+）
	// 取保守下限，后续方法面增长不触发、骤减必触发。
	if total < 380 {
		t.Fatalf("repo 导出方法总数异常偏低：%d（立门时 417），分类输入可能不完整", total)
	}
	if len(readSet) < 150 {
		t.Fatalf("repo READ 集异常偏小：%d，分类推导可能失效", len(readSet))
	}
	helpers := qualifyReadHelpers(handlerFiles, readSet)
	if len(helpers) < 5 {
		t.Fatalf("纯读 helper 合格集异常偏小：%d，资格推导可能失效", len(helpers))
	}
	sites := scanReadSwallowSites(handlerFiles, readSet, helpers)
	if len(sites) == 0 {
		t.Fatal("派生点为空：扫描推导失效（豁免点必然存在，空集即假绿）")
	}

	// 读登记册。
	regRaw, err := os.ReadFile(filepath.Join(wd, readSwallowRegistryName))
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	registered := map[readSwallowSite]int{}
	for ln, line := range strings.Split(string(regRaw), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			t.Fatalf("registry line %d: want 5 tab-separated fields (file, func, callee, count, reason), got %d", ln+1, len(fields))
		}
		count, cerr := strconv.Atoi(fields[3])
		if cerr != nil || count < 1 {
			t.Fatalf("registry line %d: bad count %q", ln+1, fields[3])
		}
		if strings.TrimSpace(fields[4]) == "" {
			t.Fatalf("registry line %d: reason must not be empty", ln+1)
		}
		key := readSwallowSite{File: fields[0], Func: fields[1], Callee: fields[2]}
		if _, dup := registered[key]; dup {
			t.Fatalf("registry line %d: duplicate key %s", ln+1, key)
		}
		registered[key] = count
	}

	var problems []string
	for site, count := range sites {
		rc, ok := registered[site]
		if !ok {
			problems = append(problems, fmt.Sprintf("未登记的读错误吞噬点 ×%d：%s（修复它，或逐点写明机制理由登记入册）", count, site))
		} else if rc != count {
			problems = append(problems, fmt.Sprintf("登记计数不符：%s 登记 %d、派生 %d（同键新增/减少吞错点须同步登记册）", site, rc, count))
		}
	}
	for site := range registered {
		if _, ok := sites[site]; !ok {
			problems = append(problems, fmt.Sprintf("登记册腐烂行（已无此派生点）：%s（点已修复则删行；点被改形则同步键）", site))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("R3-MR-01 登记门红：\n  %s", strings.Join(problems, "\n  "))
	}
}

// ---------- 合成变异自检 ----------

func TestReadErrorSwallowGateSynthetic(t *testing.T) {
	repoSrcs := map[string]string{
		"repo.go": `package repo

type Repository struct{}

func (r *Repository) ListThings() ([]int64, error) { return nil, nil }

func (r *Repository) CountThings() (int64, error) { return 0, nil }

func (r *Repository) UpdateThing() (int, error) {
	db := &fakeDB{}
	_ = db.Exec("update thing set x=1")
	return 0, nil
}

type fakeDB struct{}

func (d *fakeDB) Exec(q string) error { return nil }
`,
	}
	handlerSrcs := map[string]string{
		"h.go": `package handler

type repoStub interface{}

type Handler struct{ repo *repo }

type repo struct{}

func (h *Handler) swallowDirect() int {
	rows, _ := h.repo.ListThings()
	return len(rows)
}

func (h *Handler) checkedDirect() (int, error) {
	rows, err := h.repo.ListThings()
	if err != nil {
		return 0, err
	}
	return len(rows), nil
}

func (h *Handler) swallowWrite() int {
	n, _ := h.repo.UpdateThing()
	return n
}

func (h *Handler) allThings() ([]int64, error) {
	rows, err := h.repo.ListThings()
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (h *Handler) swallowViaHelper() int {
	rows, _ := h.allThings()
	return len(rows)
}

func (h *Handler) applyThings() ([]int64, error) {
	rows, err := h.repo.ListThings()
	if err != nil {
		return nil, err
	}
	if _, err := h.repo.UpdateThing(); err != nil {
		return nil, err
	}
	return rows, nil
}

func (h *Handler) swallowViaWriteHelper() int {
	rows, _ := h.applyThings()
	return len(rows)
}
`,
	}
	// 注意：合成 handler 的 repo 是本地 stub 类型，分类只取 repo 包
	// 方法表（名字对齐即可），扫描按调用链中的 repo 段识别。
	sites, helpers, _, conflicts := deriveReadSwallowSites(
		parseSources(t, handlerSrcs), parseSources(t, repoSrcs))
	if len(conflicts) > 0 {
		t.Fatalf("synthetic conflicts: %v", conflicts)
	}
	if !helpers["allThings"] {
		t.Fatal("synthetic: allThings must qualify as pure-read helper")
	}
	if helpers["applyThings"] {
		t.Fatal("synthetic: applyThings calls a repo write and must be disqualified")
	}
	want := map[readSwallowSite]int{
		{File: "h.go", Func: "swallowDirect", Callee: "ListThings"}:   1,
		{File: "h.go", Func: "swallowViaHelper", Callee: "allThings"}: 1,
	}
	if len(sites) != len(want) {
		t.Fatalf("synthetic sites = %v, want %v", sites, want)
	}
	for k, v := range want {
		if sites[k] != v {
			t.Fatalf("synthetic sites = %v, want %v", sites, want)
		}
	}
}
