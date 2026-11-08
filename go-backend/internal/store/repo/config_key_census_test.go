package repo

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// AG-05：设置键普查门。
//
// 背景：vite_config 的访问策略只有 config_policy.go 五张表这一处登记，
// 此前没有任何普查——后端/前端代码按名引用一个未登记的新键时，
// PolicyForConfig 静默落默认 sensitive（非管理员不可读），管理员无感、
// 普通用户功能面静默收窄，无任何信号。本门把「代码引用键全集」与
// 「登记全集」机械对账：引用了未登记的键即红，逼新键先登记再上线。
//
// 登记全集（universe）＝五表并集 ∪ backupRuntimeExcludedConfigKeys
// （运行时台账键，已在 config_policy.go 内显式登记并注明理由）∪
// backupFilterExemptConfigKeyPrefixes 前缀族（同为显式登记）。
// 五表并集基线钉死为 configKeyRegistryBaseline：往五表加键必须同步
// 改这个数字，增减登记要有意识地过本门。
//
// 引用全集的机械枚举通道（测试文件一律排除：用例里的假键是夹具，
// 与生产口径同 route 普查）：
//
//	BE-A 后端访问器字面量实参：GetConfigByName / GetViteConfigValue /
//	     UpsertConfig / DeleteConfig 的键位字符串字面量；
//	     GetConfigsByNames 的 []string 字面量元素；UpsertConfigs 的
//	     map 字面量键；ApplyConfigBatch 的 deleteKeys 字面量元素。
//	BE-B 后端访问器标识符实参：同上位置的标识符/包外选择器，解析到
//	     全树 const 字符串声明取值（monitoring.Config* 等经此入集）。
//	BE-C 后端配置键命名模式常量：const 名匹配 *ConfigKey 后缀或
//	     Config* / notifyConfig* / gistConfig* 前缀、且值为键形
//	     字符串的声明（gist 台账键、notify 设置键经此入集——它们多
//	     经 ListConfigs 结果 map 以常量下标读取，不经具名访问器）。
//	BE-D model.ViteConfig{Name: "字面量"} 的种子点。
//	FE-A 前端 getConfigByName / getPublicConfigByName /
//	     getCachedConfig 实参：字面量直取；标识符只解析到同文件
//	     const 字符串声明，或全大写命名（SCREAMING_CASE，本仓跨
//	     文件导入常量的既定形态）的跨文件声明——通用局部名（如
//	     key）跨文件同名碰撞会把无关存储键误连成配置键（首跑实测
//	     sessionStorage 的 flmx_chunk_reload 经他文件 key 变量被
//	     误报），故不做无限定全局解析。
//	FE-B 前端 *CONFIG_KEYS 集合字面量（PUBLIC_BRAND_CONFIG_KEYS /
//	     SENSITIVE_CONFIG_KEYS 等键单数组/Set 的元素）。
//
// 已知边界（不在枚举内，靠其他门或约定兜底）：
//   - 经变量拼装的键名（fmt.Sprintf 前缀族等）静态不可见，其登记
//     义务落在 backupFilterExemptConfigKeyPrefixes 这类显式前缀
//     登记上；枚举到的带前缀键按前缀登记放行。
//   - ListConfigs 结果 map 的字面量下标读（cfg["app_name"] 形态）
//     与 kernel 渲染的同名局部变量 map 静态不可区分，不设通道；
//     该形态涉及的键均另有 BE-A/BE-C/FE 通道覆盖。
//   - 前端 config 页表单的 key: 字面量是管理员写面，模式与通用
//     表单 schema 混杂不设通道；其键均经后端读面或 FE 通道入集。
//
// DEV-RM-GIST (2.1.0): 53 → 45 — the eight gist_* keys left the
// sensitive/system-managed tables with the Gist backup feature.
const configKeyRegistryBaseline = 45

var configKeyShape = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// singleKeyAccessors：键在第 0 实参的具名配置访问器（任意接收者，
// 以方法/函数名为准——与 route 普查的文本面口径一致，本仓不存在
// 同名异义的非配置函数）。
var configSingleKeyAccessors = map[string]bool{
	"GetConfigByName":    true,
	"GetViteConfigValue": true,
	"UpsertConfig":       true,
	"DeleteConfig":       true,
}

func isConfigKeyConstName(name, pkg string) bool {
	if strings.HasSuffix(name, "ConfigKey") {
		return true
	}
	if strings.HasPrefix(name, "notifyConfig") || strings.HasPrefix(name, "gistConfig") {
		return true
	}
	// monitoring 包风格：ConfigMonitorRetentionDays 等（Config 后接
	// 大写）。必须限定 monitoring 包：repo 包自己的 ConfigAccess*
	// 是策略级别枚举值（public/sensitive/authenticated），不是键名，
	// 无包限定会把它们误报成未登记键（首跑实测）。
	if pkg == "monitoring" && strings.HasPrefix(name, "Config") && len(name) > len("Config") {
		c := name[len("Config")]
		return c >= 'A' && c <= 'Z'
	}
	return false
}

type configKeySite struct {
	key  string
	site string
}

func TestConfigKeyCensus(t *testing.T) {
	backendRoot := filepath.Join("..", "..", "..")
	repoRoot := filepath.Join(backendRoot, "..")

	// ── 登记全集 ──────────────────────────────────────────────
	registry := map[string]bool{}
	for _, table := range []map[string]struct{}{
		publicConfigKeys, sensitiveConfigKeys, requiredConfigKeys,
		systemManagedConfigKeys, authenticatedConfigKeys,
	} {
		for k := range table {
			registry[k] = true
		}
	}
	if len(registry) != configKeyRegistryBaseline {
		t.Errorf("config policy registry union = %d keys, baseline pinned at %d — adding/removing a policy key must update configKeyRegistryBaseline in config_key_census_test.go in the same change", len(registry), configKeyRegistryBaseline)
	}
	universe := map[string]bool{}
	for k := range registry {
		universe[k] = true
	}
	for k := range backupRuntimeExcludedConfigKeys {
		universe[k] = true
	}

	var sites []configKeySite
	addSite := func(key, site string) {
		key = normalizeConfigKey(key)
		if key == "" {
			return
		}
		sites = append(sites, configKeySite{key, site})
	}

	// ── 后端枚举（AST，排除 _test.go）────────────────────────
	var goFiles []string
	err := filepath.Walk(backendRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		goFiles = append(goFiles, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk backend: %v", err)
	}
	if len(goFiles) < 100 {
		t.Fatalf("backend census walked only %d go files — enumeration root is wrong, gate would pass vacuously", len(goFiles))
	}

	type parsedFile struct {
		path string
		fset *token.FileSet
		file *ast.File
	}
	type constDecl struct {
		name, value, pkg string
	}
	var parsed []parsedFile
	var constDecls []constDecl
	constValues := map[string]string{}
	for _, path := range goFiles {
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		parsed = append(parsed, parsedFile{path, fset, file})
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if v, uerr := strconv.Unquote(lit.Value); uerr == nil {
							constValues[name.Name] = v
							constDecls = append(constDecls, constDecl{name.Name, v, file.Name.Name})
						}
					}
				}
			}
		}
	}

	// constRef 把实参表达式解析为常量字符串（字面量/标识符/包选择器）。
	constRef := func(expr ast.Expr) (string, bool) {
		switch e := expr.(type) {
		case *ast.BasicLit:
			if e.Kind == token.STRING {
				if v, uerr := strconv.Unquote(e.Value); uerr == nil {
					return v, true
				}
			}
		case *ast.Ident:
			if v, ok := constValues[e.Name]; ok {
				return v, true
			}
		case *ast.SelectorExpr:
			if v, ok := constValues[e.Sel.Name]; ok {
				return v, true
			}
		}
		return "", false
	}

	for _, pf := range parsed {
		rel, _ := filepath.Rel(backendRoot, pf.path)
		site := func(n ast.Node) string {
			return fmt.Sprintf("%s:%d", filepath.ToSlash(rel), pf.fset.Position(n.Pos()).Line)
		}
		ast.Inspect(pf.file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				sel, ok := node.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch {
				case configSingleKeyAccessors[sel.Sel.Name]:
					if len(node.Args) >= 1 {
						if v, ok := constRef(node.Args[0]); ok {
							addSite(v, site(node))
						}
					}
				case sel.Sel.Name == "GetConfigsByNames":
					if len(node.Args) >= 1 {
						if lit, ok := node.Args[0].(*ast.CompositeLit); ok {
							for _, elt := range lit.Elts {
								if v, ok := constRef(elt); ok {
									addSite(v, site(node))
								}
							}
						}
					}
				case sel.Sel.Name == "UpsertConfigs":
					if len(node.Args) >= 1 {
						if lit, ok := node.Args[0].(*ast.CompositeLit); ok {
							for _, elt := range lit.Elts {
								if kv, ok := elt.(*ast.KeyValueExpr); ok {
									if v, ok := constRef(kv.Key); ok {
										addSite(v, site(node))
									}
								}
							}
						}
					}
				case sel.Sel.Name == "ApplyConfigBatch":
					if len(node.Args) >= 2 {
						if lit, ok := node.Args[1].(*ast.CompositeLit); ok {
							for _, elt := range lit.Elts {
								if v, ok := constRef(elt); ok {
									addSite(v, site(node))
								}
							}
						}
					}
				}
			case *ast.CompositeLit:
				// BE-D：model.ViteConfig{Name: "…"} 种子点。
				typeName := ""
				switch tt := node.Type.(type) {
				case *ast.SelectorExpr:
					typeName = tt.Sel.Name
				case *ast.Ident:
					typeName = tt.Name
				}
				if typeName == "ViteConfig" {
					for _, elt := range node.Elts {
						kv, ok := elt.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Name" {
							if v, ok := constRef(kv.Value); ok {
								addSite(v, site(node))
							}
						}
					}
				}
			}
			return true
		})
	}

	// BE-C：配置键命名模式常量。
	for _, cd := range constDecls {
		if isConfigKeyConstName(cd.name, cd.pkg) && configKeyShape.MatchString(cd.value) {
			addSite(cd.value, "const "+cd.name)
		}
	}

	// ── 前端枚举（文本面，vite-frontend/src）──────────────────
	frontendSrc := filepath.Join(repoRoot, "vite-frontend", "src")
	var tsFiles []string
	err = filepath.Walk(frontendSrc, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !(strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx")) {
			return nil
		}
		tsFiles = append(tsFiles, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk frontend: %v", err)
	}
	if len(tsFiles) < 50 {
		t.Fatalf("frontend census walked only %d ts files — enumeration root is wrong, gate would pass vacuously", len(tsFiles))
	}
	feConstRe := regexp.MustCompile(`(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	feScreamingRe := regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	feConstsByFile := map[string]map[string]string{}
	feSharedConsts := map[string]string{}
	feContents := map[string]string{}
	for _, path := range tsFiles {
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("read %s: %v", path, rerr)
		}
		content := string(raw)
		feContents[path] = content
		byFile := map[string]string{}
		for _, m := range feConstRe.FindAllStringSubmatch(content, -1) {
			v := m[2]
			if v == "" {
				v = m[3]
			}
			byFile[m[1]] = v
			if feScreamingRe.MatchString(m[1]) {
				feSharedConsts[m[1]] = v
			}
		}
		feConstsByFile[path] = byFile
	}
	feAccessorRe := regexp.MustCompile(`(?:getConfigByName|getPublicConfigByName|getCachedConfig)\(\s*(?:"([^"]+)"|'([^']+)'|([A-Za-z_$][A-Za-z0-9_$]*))`)
	feCollectionRe := regexp.MustCompile(`(?s)(?:const|let|var)\s+\w*CONFIG_KEYS\w*\s*=\s*(?:new\s+Set\s*\(\s*)?\[(.*?)\]`)
	feQuotedRe := regexp.MustCompile(`"([^"]+)"|'([^']+)'`)
	for _, path := range tsFiles {
		rel, _ := filepath.Rel(repoRoot, path)
		content := feContents[path]
		for _, m := range feAccessorRe.FindAllStringSubmatch(content, -1) {
			v := m[1]
			if v == "" {
				v = m[2]
			}
			if v == "" {
				// 标识符实参：同文件声明优先，全大写命名才跨文件
				// 解析；都未命中（变量实参）则跳过。
				if local, ok := feConstsByFile[path][m[3]]; ok {
					v = local
				} else if shared, ok := feSharedConsts[m[3]]; ok {
					v = shared
				}
			}
			if v != "" {
				addSite(v, filepath.ToSlash(rel))
			}
		}
		for _, m := range feCollectionRe.FindAllStringSubmatch(content, -1) {
			for _, q := range feQuotedRe.FindAllStringSubmatch(m[1], -1) {
				v := q[1]
				if v == "" {
					v = q[2]
				}
				addSite(v, filepath.ToSlash(rel)+" (CONFIG_KEYS)")
			}
		}
	}

	// ── 对账 ──────────────────────────────────────────────────
	enumerated := map[string][]string{}
	for _, s := range sites {
		enumerated[s.key] = append(enumerated[s.key], s.site)
	}
	if len(enumerated) < 40 {
		t.Fatalf("census enumerated only %d distinct config keys — enumeration channels are broken, gate would pass vacuously", len(enumerated))
	}
	var missing []string
	for key, where := range enumerated {
		if universe[key] {
			continue
		}
		exempt := false
		for _, prefix := range backupFilterExemptConfigKeyPrefixes {
			if strings.HasPrefix(key, prefix) {
				exempt = true
				break
			}
		}
		if exempt {
			continue
		}
		sort.Strings(where)
		missing = append(missing, fmt.Sprintf("%s (referenced at %s)", key, strings.Join(dedupeStrings(where), ", ")))
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("config key %s is referenced by code but not registered in config_policy.go policy tables — classify it (public/sensitive/authenticated/required/system-managed) or register it as a runtime-ledger key with a reason", m)
	}
	t.Logf("config key census: %d distinct keys enumerated, registry union %d, runtime-ledger %d, universe %d",
		len(enumerated), len(registry), len(backupRuntimeExcludedConfigKeys), len(universe))
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
