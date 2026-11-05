package socket

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// D1-04: the panel↔agent command set (30 types) had no mechanical
// contract test — consistency rested on the runtime "unknown command"
// fallback, so a rename or typo on either side passed every unit test
// and only surfaced on a live node. This census test extracts both
// sets mechanically and pins them against the golden set:
//
//   - agent set: the case labels of routeCommand's switch in
//     websocket_reporter.go (AST, this package);
//   - panel set: every string literal under go-backend/internal
//     (non-test files) matching the command shapes, plus the dynamic
//     "Add"/"Update"+suffix expansions used by federation_resources.go.
//
// Adding or renaming a command now requires touching the golden list
// and both ends in the same change, or this test goes red. The panel
// half skips when the agent module is built outside the monorepo
// (go-backend absent); the agent-vs-golden half always runs.

var goldenCommandSet = []string{
	"AddCLimiters", "AddChains", "AddLimiters", "AddService",
	"DeleteCLimiters", "DeleteChains", "DeleteLimiters", "DeleteService",
	"KernelApply", "KernelControl", "KernelStatus",
	"PauseService", "ResumeService", "RollbackAgent",
	"ServiceMonitorCheck", "SetProtocol",
	"TcpPing", "Traceroute",
	"TuneApply", "TuneCancel", "TuneProbe", "TuneStart", "TuneStatus",
	"UdpPing", "UninstallAgent",
	"UpdateCLimiters", "UpdateChains", "UpdateLimiters", "UpdateService",
	"UpgradeAgent",
}

func sortedSet(in map[string]bool) []string {
	out := make([]string, 0, len(in))
	for k := range in {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func diffSets(name string, got map[string]bool, want []string) string {
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	var missing, extra []string
	for _, w := range want {
		if !got[w] {
			missing = append(missing, w)
		}
	}
	for g := range got {
		if !wantSet[g] {
			extra = append(extra, g)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return ""
	}
	return name + " diverges from the golden set: missing=" + strings.Join(missing, ",") + " extra=" + strings.Join(extra, ",")
}

func agentCommandSet(t *testing.T) map[string]bool {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), "websocket_reporter.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse websocket_reporter.go: %v", err)
	}
	set := make(map[string]bool)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "routeCommand" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			clause, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range clause.List {
				if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if v, err := strconv.Unquote(lit.Value); err == nil {
						set[v] = true
					}
				}
			}
			return true
		})
	}
	if len(set) == 0 {
		t.Fatal("no routeCommand case labels extracted — the census itself is broken")
	}
	return set
}

var panelCommandShape = regexp.MustCompile(`^(Add|Update|Delete|Pause|Resume)[A-Z][A-Za-z0-9]*$|^(Tcp|Udp)[A-Z][A-Za-z0-9]*$|^Tune[A-Z][A-Za-z0-9]*$|^Kernel[A-Z][A-Za-z0-9]*$|^(Traceroute|ServiceMonitorCheck|SetProtocol|UpgradeAgent|RollbackAgent|UninstallAgent)$`)

func panelCommandSet(t *testing.T) (map[string]bool, bool) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// Package dir is <repo>/go-gost/x/socket; the panel module sits at
	// <repo>/go-backend.
	internalDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "go-backend", "internal")
	if st, err := os.Stat(internalDir); err != nil || !st.IsDir() {
		return nil, false
	}
	set := make(map[string]bool)
	err := filepath.WalkDir(internalDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		var literals []string
		var dynamicPrefixes []string
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.BasicLit:
				if node.Kind == token.STRING {
					if v, uerr := strconv.Unquote(node.Value); uerr == nil {
						literals = append(literals, v)
					}
				}
			case *ast.BinaryExpr:
				// federation_resources.go builds "Add"+suffix /
				// "Update"+suffix with suffix in {Limiters, Chains,
				// CLimiters}; expand those constructions explicitly.
				if node.Op == token.ADD {
					if lit, ok := node.X.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if v, uerr := strconv.Unquote(lit.Value); uerr == nil && (v == "Add" || v == "Update") {
							if _, isIdent := node.Y.(*ast.Ident); isIdent {
								dynamicPrefixes = append(dynamicPrefixes, v)
							}
						}
					}
				}
			}
			return true
		})
		for _, v := range literals {
			if panelCommandShape.MatchString(v) && !strings.HasSuffix(v, "Response") {
				set[v] = true
			}
		}
		for _, prefix := range dynamicPrefixes {
			for _, v := range literals {
				if v == "Limiters" || v == "Chains" || v == "CLimiters" {
					set[prefix+v] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk panel sources: %v", err)
	}
	return set, true
}

func TestD1CommandSetContract(t *testing.T) {
	agent := agentCommandSet(t)
	if msg := diffSets("agent routeCommand set", agent, goldenCommandSet); msg != "" {
		t.Fatal(msg)
	}
	panel, present := panelCommandSet(t)
	if !present {
		t.Log("go-backend not found next to the agent module; panel half of the contract skipped (agent half still enforced)")
		return
	}
	if msg := diffSets("panel send set", panel, goldenCommandSet); msg != "" {
		t.Fatal(msg)
	}
	if msg := diffSets("panel send set vs agent set", panel, sortedSet(agent)); msg != "" {
		t.Fatal(msg)
	}
}
