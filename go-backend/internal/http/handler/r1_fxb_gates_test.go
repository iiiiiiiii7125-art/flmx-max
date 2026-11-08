package handler

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// R1-03-M1 回归钉：链路策略写面门（normalizeChainStrategy）——
// 合法集单点 kernel.ChainStrategyLegal 的分段口径：跳行无 best、
// 入口/出口含 best、大小写敏感、空白先 TrimSpace、空串归一 round。
func TestNormalizeChainStrategy(t *testing.T) {
	cases := []struct {
		chainType int
		raw       string
		want      string
		wantErr   bool
	}{
		{1, "fifo", "fifo", false},
		{1, "best", "best", false},
		{3, "best", "best", false},
		{2, "best", "", true},
		{2, "rand", "rand", false},
		{1, "banana", "", true},
		{1, "FIFO", "", true},
		{3, "hash", "", true},
		{1, "", "round", false},
		{2, " round ", "round", false},
		{99, "round", "round", false},
		{99, "best", "", true},
	}
	for _, c := range cases {
		got, err := normalizeChainStrategy(c.chainType, c.raw)
		if c.wantErr {
			if err == nil {
				t.Fatalf("normalizeChainStrategy(%d, %q) = %q, want error", c.chainType, c.raw, got)
			}
			if !strings.Contains(err.Error(), "合法值") {
				t.Fatalf("error must name the legal set, got %v", err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Fatalf("normalizeChainStrategy(%d, %q) = %q, %v; want %q", c.chainType, c.raw, got, err, c.want)
		}
	}
}

// R1-07-F4 回归钉：本地导入未知字段点名提取 + Gist 恢复未知顶层段
// 机械普查（已知集由 BackupData 结构体反射推导）。
func TestUnknownFieldNaming(t *testing.T) {
	var dst struct {
		Name string `json:"name"`
	}
	dec := json.NewDecoder(strings.NewReader(`{"name":"x","bogusKey":1}`))
	dec.DisallowUnknownFields()
	err := dec.Decode(&dst)
	if err == nil {
		t.Fatal("expected unknown field error")
	}
	if got := unknownJSONFieldName(err); got != "bogusKey" {
		t.Fatalf("unknownJSONFieldName = %q, want bogusKey", got)
	}
	if got := unknownJSONFieldName(errors.New("some other error")); got != "" {
		t.Fatalf("unknownJSONFieldName(other) = %q, want empty", got)
	}

	plain := []byte(`{"exportedAt":1,"version":"1.0","bogusSegment":[],"anotherNew":{}}`)
	unknown := unknownBackupTopLevelKeys(plain)
	if len(unknown) != 2 || unknown[0] != "anotherNew" || unknown[1] != "bogusSegment" {
		t.Fatalf("unknownBackupTopLevelKeys = %v, want [anotherNew bogusSegment]", unknown)
	}
	if got := unknownBackupTopLevelKeys([]byte(`{"exportedAt":1,"forwards":[]}`)); len(got) != 0 {
		t.Fatalf("clean backup must have no unknown keys, got %v", got)
	}
}
