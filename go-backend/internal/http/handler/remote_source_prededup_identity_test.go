package handler

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// J7-01 回归：远程源同步的截断前预去重地址腿必须与主循环 F-025 的
// 身份键同构（"id:"+externalIdentityKey＝地址＋协议＋凭据）。旧实现
// 误用裸 host:port 的 importedAddrKey（删除屏蔽元组的粗粒度腿），
// 同址同端口、靠协议或凭据区分的合法条目（住宅代理同网关多会话、
// 同服务器多协议同端口）在进入主循环前即被当「订阅源内重复条目」
// 静默跳过、永远同步不进来；且截断计数基于被压减后的伪唯一数。
// 本文件两条用例分别钉「全数通过预去重进入同步」与「截断计数以真
// 唯一数计」。

func TestSyncPreDedupKeepsSameAddressDistinctIdentities(t *testing.T) {
	r, h, src, _ := integrityGateSetup(t)

	body := strings.Join([]string{
		"trojan://pass-a@203.0.113.50:443#gw-a",
		"trojan://pass-b@203.0.113.50:443#gw-b",
		"trojan://pass-c@203.0.113.50:443#gw-c",
		"vless://22222222-3333-4444-5555-666666666666@203.0.113.50:443?security=none#gw-d",
	}, "\n")
	res, err := h.syncRemoteSource(context.Background(), src, body)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	for _, e := range res.Errors {
		if strings.Contains(e, "重复条目") {
			t.Fatalf("same-address distinct-identity entries must not be flagged as duplicates: %v", res.Errors)
		}
	}
	if res.Added != 4 {
		t.Fatalf("added = %d, want 4 (all same-address distinct-identity entries synced)", res.Added)
	}
	// 种子两行（A/old）未被本轮匹配、整轮干净 → 照常回收。
	if res.Removed != 2 {
		t.Fatalf("removed = %d, want 2 seeded rows recycled", res.Removed)
	}
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatalf("list inbounds: %v", err)
	}
	gw := 0
	for _, row := range rows {
		if row.SourceID == src.ID && row.Listen == "203.0.113.50" {
			gw++
		}
	}
	if gw != 4 {
		t.Fatalf("gateway rows = %d, want 4 coexisting rows on 203.0.113.50:443", gw)
	}
	if n := sourceRowCount(t, r, src.ID); n != 4 {
		t.Fatalf("source rows = %d, want 4", n)
	}
}

func TestSyncPreDedupTruncationCountsTrueUniques(t *testing.T) {
	r, h, src, _ := integrityGateSetup(t)

	var b strings.Builder
	// 前置 5 条同网关同端口、仅凭据相异的条目（截断保留段内）。
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&b, "trojan://pass-%d@203.0.113.60:443#gw-t%d\n", i, i)
	}
	// 再加 maxImportNodes 条互异 bulk：原始 505 条、真唯一 505 条。
	for i := 0; i < maxImportNodes; i++ {
		fmt.Fprintf(&b, "vless://11111111-2222-3333-4444-555555555555@198.51.%d.%d:443?security=none#bulk-%d\n", i/250+1, i%250+1, i)
	}
	res, err := h.syncRemoteSource(context.Background(), src, b.String())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	// 旧口径下 5 条网关条目被裸地址键压成 1 条唯一 → truncated=1；
	// 身份键口径下真唯一 505 → truncated=5。
	if res.Truncated != 5 {
		t.Fatalf("truncated = %d, want 5 (counted on true identity uniques)", res.Truncated)
	}
	if res.Status != remoteSyncStatusTruncated {
		t.Fatalf("status = %q, want %q", res.Status, remoteSyncStatusTruncated)
	}
	if res.Added != maxImportNodes {
		t.Fatalf("added = %d, want %d kept entries", res.Added, maxImportNodes)
	}
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatalf("list inbounds: %v", err)
	}
	gw := 0
	for _, row := range rows {
		if row.SourceID == src.ID && row.Listen == "203.0.113.60" {
			gw++
		}
	}
	if gw != 5 {
		t.Fatalf("gateway rows = %d, want all 5 same-address entries kept inside the truncation window", gw)
	}
}
