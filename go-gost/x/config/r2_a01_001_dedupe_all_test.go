package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// R2-A01-001 回归：启动自愈去重必须覆盖 Config 的全部命名切片。
//
// 背景：R17/R18/INT 三代自愈只接线 service/chain/limiter/climiter/
// rlimiter 五类，其余 12 类（hop、auther、admission、bypass、resolver、
// hosts、ingress、router、sd、recorder、observer、logger）同名毒化时
// loader.Load 在 Register 处报 registry: duplicate object、agent 致命
// 退出且毒化文件不被清理，节点永久离线（hop/bypass/resolver 已实证）。
//
// 本文件的枚举清单即闭合全集：namedSliceEnumeration 逐字段列出全部
// 17 类；TestNamedHealKindsCoverAllConfigNamedSlices 以反射枚举 Config
// 的命名切片字段与清单、与 namedHealKinds 接线表三方对账，任一差集
// 非 ∅ 即失败——将来 Config 新增命名切片而未接线自愈，测试先红。

// namedSliceEnumeration 是 Config 命名切片的全集枚举（字段名 → 自愈
// 类别标签），顺序与 Config 结构定义一致。
var namedSliceEnumeration = []struct {
	field string
	kind  string
}{
	{"Services", "service"},
	{"Chains", "chain"},
	{"Hops", "hop"},
	{"Authers", "auther"},
	{"Admissions", "admission"},
	{"Bypasses", "bypass"},
	{"Resolvers", "resolver"},
	{"Hosts", "hosts"},
	{"Ingresses", "ingress"},
	{"Routers", "router"},
	{"SDs", "sd"},
	{"Recorders", "recorder"},
	{"Limiters", "limiter"},
	{"CLimiters", "conn limiter"},
	{"RLimiters", "rate limiter"},
	{"Observers", "observer"},
	{"Loggers", "logger"},
}

// configNamedSliceFields 反射枚举 Config 的命名切片字段：切片元素为
// 结构体指针且结构体带 Name string 字段者。
func configNamedSliceFields(t *testing.T) []string {
	t.Helper()
	typ := reflect.TypeOf(Config{})
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type.Kind() != reflect.Slice {
			continue
		}
		elem := f.Type.Elem()
		if elem.Kind() != reflect.Ptr || elem.Elem().Kind() != reflect.Struct {
			continue
		}
		nameField, ok := elem.Elem().FieldByName("Name")
		if !ok || nameField.Type.Kind() != reflect.String {
			continue
		}
		fields = append(fields, f.Name)
	}
	return fields
}

func TestNamedHealKindsCoverAllConfigNamedSlices(t *testing.T) {
	reflected := configNamedSliceFields(t)
	if len(reflected) != len(namedSliceEnumeration) {
		t.Fatalf("Config named slices = %v (%d), enumeration lists %d — 差集非 ∅，新增命名切片必须同步接线自愈并补枚举",
			reflected, len(reflected), len(namedSliceEnumeration))
	}
	enumFields := make([]string, 0, len(namedSliceEnumeration))
	for _, e := range namedSliceEnumeration {
		enumFields = append(enumFields, e.field)
	}
	sort.Strings(reflected)
	sort.Strings(enumFields)
	if !reflect.DeepEqual(reflected, enumFields) {
		t.Fatalf("Config named slices %v != enumeration %v — 差集非 ∅", reflected, enumFields)
	}

	if len(namedHealKinds) != len(namedSliceEnumeration) {
		t.Fatalf("namedHealKinds has %d entries, enumeration has %d — 接线表与全集差集非 ∅",
			len(namedHealKinds), len(namedSliceEnumeration))
	}
	for i, e := range namedSliceEnumeration {
		if namedHealKinds[i].kind != e.kind {
			t.Fatalf("namedHealKinds[%d].kind = %q, enumeration kind for %s = %q", i, namedHealKinds[i].kind, e.field, e.kind)
		}
	}
}

// sliceNames 以反射读出 cfg 某命名切片的全部 Name（nil 项跳过）。
func sliceNames(cfg *Config, field string) []string {
	v := reflect.ValueOf(cfg).Elem().FieldByName(field)
	var names []string
	for i := 0; i < v.Len(); i++ {
		elem := v.Index(i)
		if elem.IsNil() {
			continue
		}
		names = append(names, elem.Elem().FieldByName("Name").String())
	}
	return names
}

// sliceFirstPtr 以反射读出 cfg 某命名切片首元素的指针值（验保留首次
// 出现的是同一实例）。
func sliceFirstPtr(cfg *Config, field string) uintptr {
	v := reflect.ValueOf(cfg).Elem().FieldByName(field)
	if v.Len() == 0 {
		return 0
	}
	return v.Index(0).Pointer()
}

// poisonedAllKinds 构造 17 类全毒化配置：每类 [dup, uniq, dup] 三项
// （同名 dup 两份），外加 services 一枚 nil 项（去重口径为原样保留，
// nil 剥离是 StripNilConfigItems 的职责、不在本条范围）。
func poisonedAllKinds() *Config {
	return &Config{
		Services: []*ServiceConfig{
			{Name: "dup-service"}, {Name: "uniq-service"}, {Name: "dup-service"}, nil,
		},
		Chains: []*ChainConfig{
			{Name: "dup-chain"}, {Name: "uniq-chain"}, {Name: "dup-chain"},
		},
		Hops: []*HopConfig{
			{Name: "dup-hop"}, {Name: "uniq-hop"}, {Name: "dup-hop"},
		},
		Authers: []*AutherConfig{
			{Name: "dup-auther"}, {Name: "uniq-auther"}, {Name: "dup-auther"},
		},
		Admissions: []*AdmissionConfig{
			{Name: "dup-admission"}, {Name: "uniq-admission"}, {Name: "dup-admission"},
		},
		Bypasses: []*BypassConfig{
			{Name: "dup-bypass"}, {Name: "uniq-bypass"}, {Name: "dup-bypass"},
		},
		Resolvers: []*ResolverConfig{
			{Name: "dup-resolver"}, {Name: "uniq-resolver"}, {Name: "dup-resolver"},
		},
		Hosts: []*HostsConfig{
			{Name: "dup-hosts"}, {Name: "uniq-hosts"}, {Name: "dup-hosts"},
		},
		Ingresses: []*IngressConfig{
			{Name: "dup-ingress"}, {Name: "uniq-ingress"}, {Name: "dup-ingress"},
		},
		Routers: []*RouterConfig{
			{Name: "dup-router"}, {Name: "uniq-router"}, {Name: "dup-router"},
		},
		SDs: []*SDConfig{
			{Name: "dup-sd"}, {Name: "uniq-sd"}, {Name: "dup-sd"},
		},
		Recorders: []*RecorderConfig{
			{Name: "dup-recorder"}, {Name: "uniq-recorder"}, {Name: "dup-recorder"},
		},
		Limiters: []*LimiterConfig{
			{Name: "dup-limiter"}, {Name: "uniq-limiter"}, {Name: "dup-limiter"},
		},
		CLimiters: []*LimiterConfig{
			{Name: "dup-climiter"}, {Name: "uniq-climiter"}, {Name: "dup-climiter"},
		},
		RLimiters: []*LimiterConfig{
			{Name: "dup-rlimiter"}, {Name: "uniq-rlimiter"}, {Name: "dup-rlimiter"},
		},
		Observers: []*ObserverConfig{
			{Name: "dup-observer"}, {Name: "uniq-observer"}, {Name: "dup-observer"},
		},
		Loggers: []*LoggerConfig{
			{Name: "dup-logger"}, {Name: "uniq-logger"}, {Name: "dup-logger"},
		},
	}
}

func TestHealPersistedDuplicatesAllKinds(t *testing.T) {
	originalPath := persistPath
	originalEnabled := persistEnable
	persistFile := filepath.Join(t.TempDir(), "gost.json")
	t.Cleanup(func() {
		persistPath = originalPath
		persistEnable = originalEnabled
	})
	persistPath = persistFile
	persistEnable = false // Heal* 不依赖 EnablePersist

	cfg := poisonedAllKinds()
	firstPtrs := make(map[string]uintptr, len(namedSliceEnumeration))
	for _, e := range namedSliceEnumeration {
		firstPtrs[e.field] = sliceFirstPtr(cfg, e.field)
	}

	heals := HealPersistedDuplicates(cfg)

	if len(heals) != len(namedSliceEnumeration) {
		t.Fatalf("HealPersistedDuplicates reported %d kinds, want %d (one per named slice)", len(heals), len(namedSliceEnumeration))
	}
	for i, e := range namedSliceEnumeration {
		if heals[i].Kind != e.kind {
			t.Fatalf("heals[%d].Kind = %q, want %q", i, heals[i].Kind, e.kind)
		}
		if heals[i].Err != nil {
			t.Fatalf("heals[%d] (%s) persist error: %v", i, e.kind, heals[i].Err)
		}
		wantRemoved := "dup-" + e.kind
		if e.kind == "conn limiter" {
			wantRemoved = "dup-climiter"
		}
		if e.kind == "rate limiter" {
			wantRemoved = "dup-rlimiter"
		}
		if len(heals[i].Removed) != 1 || heals[i].Removed[0] != wantRemoved {
			t.Fatalf("heals[%d] (%s).Removed = %v, want [%s]", i, e.kind, heals[i].Removed, wantRemoved)
		}

		names := sliceNames(cfg, e.field)
		if len(names) != 2 || names[0] != wantRemoved || names[1][:5] != "uniq-" {
			t.Fatalf("%s after heal = %v, want [dup-* uniq-*] (first occurrence kept, dup removed)", e.field, names)
		}
		if got := sliceFirstPtr(cfg, e.field); got != firstPtrs[e.field] {
			t.Fatalf("%s: kept first element is not the original first occurrence", e.field)
		}
	}
	// services 的 nil 项按去重口径原样保留（4 项入、去 1 重复、余 3 项含 nil）。
	if len(cfg.Services) != 3 || cfg.Services[2] != nil {
		t.Fatalf("services after heal = %v entries, want 3 with trailing nil kept", len(cfg.Services))
	}

	// 落盘文件同样已愈合：重读后每类只剩两项、无同名。
	data, err := os.ReadFile(persistFile)
	if err != nil {
		t.Fatalf("healed config was not persisted: %v", err)
	}
	var onDisk Config
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshal persisted config: %v", err)
	}
	for _, e := range namedSliceEnumeration {
		names := sliceNames(&onDisk, e.field)
		seen := map[string]int{}
		for _, n := range names {
			seen[n]++
		}
		for n, c := range seen {
			if c != 1 {
				t.Fatalf("persisted %s name %q appears %d times, want 1", e.field, n, c)
			}
		}
	}
}

func TestHealPersistedDuplicatesCleanAndNilAreNoop(t *testing.T) {
	originalPath := persistPath
	dir := t.TempDir()
	t.Cleanup(func() { persistPath = originalPath })
	persistPath = filepath.Join(dir, "gost.json")

	clean := &Config{
		Hops:     []*HopConfig{{Name: "h1"}},
		Bypasses: []*BypassConfig{{Name: "b1"}},
	}
	if heals := HealPersistedDuplicates(clean); heals != nil {
		t.Fatalf("clean config heals = %v, want nil", heals)
	}
	if _, err := os.Stat(persistPath); err == nil {
		t.Fatal("clean config must not trigger a persist write")
	}
	if heals := HealPersistedDuplicates(nil); heals != nil {
		t.Fatalf("HealPersistedDuplicates(nil) = %v, want nil", heals)
	}
}
