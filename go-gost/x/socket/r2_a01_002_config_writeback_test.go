package socket

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// R2-A01-002 回归：updateLocalConfigJSON 写回不得吞字段。
//
// 此前写回用仅含五字段（addr/secret/http/tls/socks）的结构体整表重构，
// 规范字段全集 {addr, secret, http, tls, socks, proxy} 中的 proxy
// （安装落盘的升级代理前缀）与一切未知字段被静默销毁。本测试以字段集
// 差集为断言口径：写回前后键集必须相等（差集 ∅），proxy 与未知键的
// 值必须原样保留，只有 http/tls/socks 三键按入参更新。

func readConfigKeys(t *testing.T, path string) (map[string]json.RawMessage, []string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatalf("unmarshal written config: %v", err)
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fields, keys
}

func TestUpdateLocalConfigJSONPreservesAllFields(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "config.json")

	original := `{
  "addr": "wss://panel.example:443",
  "secret": "node-secret",
  "http": 0,
  "tls": 0,
  "socks": 0,
  "proxy": "http://127.0.0.1:7890",
  "custom_unknown": {"nested": [1, 2, 3]},
  "note": "keep-me"
}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	_, keysBefore := readConfigKeys(t, path)

	if err := updateLocalConfigJSON(1, 0, 1); err != nil {
		t.Fatalf("updateLocalConfigJSON: %v", err)
	}

	fields, keysAfter := readConfigKeys(t, path)
	if !reflect.DeepEqual(keysBefore, keysAfter) {
		t.Fatalf("字段集写回前后不一致: before %v, after %v — 差集非 ∅（字段被吞或被造）", keysBefore, keysAfter)
	}

	var got struct {
		Addr  string `json:"addr"`
		Proxy string `json:"proxy"`
		Note  string `json:"note"`
		HTTP  int    `json:"http"`
		TLS   int    `json:"tls"`
		Socks int    `json:"socks"`
	}
	raw, _ := json.Marshal(fields)
	_ = raw
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.HTTP != 1 || got.TLS != 0 || got.Socks != 1 {
		t.Fatalf("目标三键 = %d/%d/%d, want 1/0/1", got.HTTP, got.TLS, got.Socks)
	}
	if got.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("proxy 被吞或被改: %q", got.Proxy)
	}
	if got.Addr != "wss://panel.example:443" || got.Note != "keep-me" {
		t.Fatalf("既有字段被改: addr=%q note=%q", got.Addr, got.Note)
	}
	var unknown map[string]interface{}
	if err := json.Unmarshal(fields["custom_unknown"], &unknown); err != nil {
		t.Fatalf("未知字段损坏: %v", err)
	}
	if !reflect.DeepEqual(unknown["nested"], []interface{}{float64(1), float64(2), float64(3)}) {
		t.Fatalf("未知字段值被改: %v", unknown)
	}
}

func TestUpdateLocalConfigJSONCorruptFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "config.json")

	if err := os.WriteFile(path, []byte(`[1,2,3]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := updateLocalConfigJSON(1, 1, 1); err == nil {
		t.Fatal("非对象 JSON 的 config.json 必须 fail-closed 报错，不得整表覆盖")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `[1,2,3]` {
		t.Fatalf("报错后文件被改动: %s", b)
	}
}

func TestUpdateLocalConfigJSONMissingFileCreates(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := updateLocalConfigJSON(0, 1, 0); err != nil {
		t.Fatalf("updateLocalConfigJSON on missing file: %v", err)
	}
	fields, keys := readConfigKeys(t, filepath.Join(dir, "config.json"))
	if !reflect.DeepEqual(keys, []string{"http", "socks", "tls"}) {
		t.Fatalf("新建文件键集 = %v, want [http socks tls]", keys)
	}
	var tlsVal int
	if err := json.Unmarshal(fields["tls"], &tlsVal); err != nil || tlsVal != 1 {
		t.Fatalf("tls = %v (err %v), want 1", tlsVal, err)
	}
}
