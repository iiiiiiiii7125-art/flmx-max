package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// R6-L8-F-01：面板自升级整份覆写 compose 时，FLMX_TRUSTED_PROXY_HOPS
// 传递行不得被冲掉——候选缺失时由 ensureComposeTrustedProxyHops
// 保留（存量显式值）或再生（模板标准行）。缺行 + 代码默认 1 跳与
// 脚本默认两跳拓扑错配，登录限流键会收敛为全站共用一桶。

// 集成对账（G3a×G3b）：夹具须含 backend＋frontend 两服务——G3b 的
// validateComposeAsset 要求候选资产含两预期服务，只有 backend 的
// 夹具会在 replaceCompose 全链测试里被内容校验正当拒绝。
const r6ComposeCandidateNoHops = `services:
  backend:
    image: example/backend:1.9.3
    environment:
      DB_TYPE: sqlite
      SERVER_ADDR: :6365
  frontend:
    image: example/frontend:1.9.3
networks:
  gost-network:
    name: gost-network
`

const r6ComposeExistingNoHops = `services:
  backend:
    image: example/backend:1.9.2
    environment:
      DB_TYPE: sqlite
      SERVER_ADDR: :6365
  frontend:
    image: example/frontend:1.9.2
networks:
  gost-network:
    name: gost-network
`

// r6HopsValue 解析 compose 并回读 backend environment 中的
// FLMX_TRUSTED_PROXY_HOPS（映射/列表两形都认），返回（值， 是否存在）。
func r6HopsValue(t *testing.T, compose []byte) (string, bool) {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		t.Fatalf("compose must stay valid YAML: %v\n%s", err, compose)
	}
	env := composeBackendEnvironment(&doc)
	if env == nil {
		return "", false
	}
	if v := yamlMappingChild(env, trustedProxyHopsEnvKey); v != nil {
		return v.Value, true
	}
	if item := composeEnvListItem(env, trustedProxyHopsEnvKey); item != "" {
		_, value, _ := strings.Cut(item, "=")
		return value, true
	}
	return "", false
}

func TestR6L8F01EnsureInjectsStandardLineWhenBothMissing(t *testing.T) {
	out := ensureComposeTrustedProxyHops([]byte(r6ComposeExistingNoHops), []byte(r6ComposeCandidateNoHops))
	value, ok := r6HopsValue(t, out)
	if !ok {
		t.Fatalf("hops line must be regenerated on upgrade write:\n%s", out)
	}
	if value != trustedProxyHopsComposeValue {
		t.Fatalf("regenerated value = %q, want template standard %q", value, trustedProxyHopsComposeValue)
	}
	if !strings.Contains(string(out), "example/backend:1.9.3") {
		t.Fatalf("candidate content must be kept:\n%s", out)
	}
}

func TestR6L8F01EnsureKeepsCandidateLineUntouched(t *testing.T) {
	candidate := strings.Replace(r6ComposeCandidateNoHops,
		"      SERVER_ADDR: :6365\n",
		"      SERVER_ADDR: :6365\n      FLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2}\n", 1)
	out := ensureComposeTrustedProxyHops([]byte(r6ComposeExistingNoHops), []byte(candidate))
	if string(out) != candidate {
		t.Fatalf("candidate already carrying the line must pass through byte-identical:\n%s", out)
	}
}

func TestR6L8F01EnsurePreservesExistingExplicitValue(t *testing.T) {
	existing := strings.Replace(r6ComposeExistingNoHops,
		"      SERVER_ADDR: :6365\n",
		"      SERVER_ADDR: :6365\n      FLMX_TRUSTED_PROXY_HOPS: 1\n", 1)
	out := ensureComposeTrustedProxyHops([]byte(existing), []byte(r6ComposeCandidateNoHops))
	value, ok := r6HopsValue(t, out)
	if !ok || value != "1" {
		t.Fatalf("existing explicit value must be carried over, got %q (present=%v):\n%s", value, ok, out)
	}
}

func TestR6L8F01EnsureListFormCandidate(t *testing.T) {
	candidate := `services:
  backend:
    image: example/backend:1.9.3
    environment:
      - DB_TYPE=sqlite
      - SERVER_ADDR=:6365
`
	out := ensureComposeTrustedProxyHops([]byte(r6ComposeExistingNoHops), []byte(candidate))
	value, ok := r6HopsValue(t, out)
	if !ok || value != trustedProxyHopsComposeValue {
		t.Fatalf("list-form environment must gain a list item, got %q (present=%v):\n%s", value, ok, out)
	}
	if !strings.Contains(string(out), "- FLMX_TRUSTED_PROXY_HOPS=") {
		t.Fatalf("injected entry must use list form:\n%s", out)
	}
}

func TestR6L8F01EnsureListFormExistingValueToMappingCandidate(t *testing.T) {
	existing := `services:
  backend:
    image: example/backend:1.9.2
    environment:
      - DB_TYPE=sqlite
      - FLMX_TRUSTED_PROXY_HOPS=3
`
	out := ensureComposeTrustedProxyHops([]byte(existing), []byte(r6ComposeCandidateNoHops))
	value, ok := r6HopsValue(t, out)
	if !ok || value != "3" {
		t.Fatalf("existing list-form value must be carried into the mapping, got %q (present=%v):\n%s", value, ok, out)
	}
}

func TestR6L8F01EnsureDegenerateInputsPassThrough(t *testing.T) {
	if out := ensureComposeTrustedProxyHops(nil, []byte("services: {}\n")); string(out) != "services: {}\n" {
		t.Fatalf("candidate without backend environment must pass through, got:\n%s", out)
	}
	if out := ensureComposeTrustedProxyHops(nil, []byte("::: not yaml :::\n")); string(out) != "::: not yaml :::\n" {
		t.Fatalf("unparseable candidate must pass through, got:\n%s", out)
	}
}

func TestR6L8F01ReplaceComposeRegeneratesHopsLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(path, []byte(r6ComposeExistingNoHops), 0o644); err != nil {
		t.Fatalf("seed existing compose: %v", err)
	}
	exec := &systemUpgradeExecutor{deployDir: dir}
	if err := exec.replaceCompose(path, []byte(r6ComposeCandidateNoHops)); err != nil {
		t.Fatalf("replaceCompose: %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written compose: %v", err)
	}
	value, ok := r6HopsValue(t, written)
	if !ok || value != trustedProxyHopsComposeValue {
		t.Fatalf("written compose must carry the hops line, got %q (present=%v):\n%s", value, ok, written)
	}
	if !strings.Contains(string(written), "example/backend:1.9.3") {
		t.Fatalf("written compose must be the candidate otherwise:\n%s", written)
	}
}
