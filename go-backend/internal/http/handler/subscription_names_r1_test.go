package handler

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// X-P3-1（B-F1）：subPipeline.normalize 此前唯独 ChainLandingNames /
// ChainRelayNames 未过 normalizeNames——带空格的名字原样落库，与节点
// Original 精确匹配永不命中、链式静默失效；条数/长度上限也缺失。
// 与 ChainNoneNames 同口径：修剪、去重、>1000 条报「选的节点太多」、单名 >200 rune 报「节点名称太长」。
func TestR1B7NormalizeChainNamesTrimmedAndPersisted(t *testing.T) {
	p := subPipeline{
		ChainLandingNames: []string{" 香港01 ", "香港01", " 落地B"},
		ChainRelayNames:   []string{" 中转A ", ""},
	}
	if err := p.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(p.ChainLandingNames) != 2 || p.ChainLandingNames[0] != "香港01" || p.ChainLandingNames[1] != "落地B" {
		t.Fatalf("chainLandingNames not trimmed/deduped: %v", p.ChainLandingNames)
	}
	if len(p.ChainRelayNames) != 1 || p.ChainRelayNames[0] != "中转A" {
		t.Fatalf("chainRelayNames not trimmed: %v", p.ChainRelayNames)
	}

	// 落库往返：保存的 Options JSON 必须是修剪后的值。
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	s := &model.Subscription{Name: "chain-names", Kind: model.SubscriptionShare,
		Token: strings.Repeat("d", 32), UUID: "u", Password: "pw", Enabled: 1,
		Options: string(raw), CreatedTime: now, UpdatedTime: now}
	if err := r.SaveSubscription(s); err != nil {
		t.Fatalf("save subscription: %v", err)
	}
	got, err := r.GetSubscription(s.ID)
	if err != nil || got == nil {
		t.Fatalf("reload subscription: %v", err)
	}
	back := parsePipeline(got.Options)
	if len(back.ChainLandingNames) != 2 || back.ChainLandingNames[0] != "香港01" {
		t.Fatalf("persisted chainLandingNames = %v, want trimmed [香港01 落地B]", back.ChainLandingNames)
	}
}

func TestR1B7NormalizeChainNamesLimits(t *testing.T) {
	many := make([]string, 1001)
	for i := range many {
		many[i] = "node"
	}
	p := subPipeline{ChainLandingNames: many}
	if err := p.normalize(); err == nil || !strings.Contains(err.Error(), "选的节点太多") {
		t.Fatalf("1001 chain landing names must be rejected with 选的节点太多, got %v", err)
	}
	p = subPipeline{ChainRelayNames: many}
	if err := p.normalize(); err == nil || !strings.Contains(err.Error(), "选的节点太多") {
		t.Fatalf("1001 chain relay names must be rejected with 选的节点太多, got %v", err)
	}
	p = subPipeline{ChainLandingNames: []string{strings.Repeat("长", 201)}}
	if err := p.normalize(); err == nil || !strings.Contains(err.Error(), "节点名称太长") {
		t.Fatalf("over-long chain name must be rejected, got %v", err)
	}
}
