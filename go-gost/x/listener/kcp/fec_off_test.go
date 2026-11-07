package kcp

import (
	"testing"

	xmetadata "github.com/go-gost/x/metadata"
)

// FIX-KCPMEM（P1-05）：面板关闭档下发的 kcp.fec_off 标记必须把
// FEC 冗余比归零——构造法证据：不带标记时缺省 10/3，带标记时
// DataShard/ParityShard 均为 0，编码器据此不产冗余分片。
func TestFecOffMarkerZeroesShards(t *testing.T) {
	l := NewListener().(*kcpListener)
	md := xmetadata.NewMetadata(map[string]any{"kcp.fec_off": true})
	if err := l.parseMetadata(md); err != nil {
		t.Fatalf("parseMetadata: %v", err)
	}
	if l.md.config.DataShard != 0 || l.md.config.ParityShard != 0 {
		t.Fatalf("fec_off must zero shards, got %d/%d",
			l.md.config.DataShard, l.md.config.ParityShard)
	}

	l2 := NewListener().(*kcpListener)
	md2 := xmetadata.NewMetadata(map[string]any{"kcp.datashard": 10, "kcp.parityshard": 3})
	if err := l2.parseMetadata(md2); err != nil {
		t.Fatalf("parseMetadata: %v", err)
	}
	if l2.md.config.DataShard != 10 || l2.md.config.ParityShard != 3 {
		t.Fatalf("default mode must keep 10/3, got %d/%d",
			l2.md.config.DataShard, l2.md.config.ParityShard)
	}

	// 标记胜过显式 shard 键（面板关闭档不下发 shard 键，但存量
	// 配置文件可能残留，两者同在时必须以关闭为准）。
	l3 := NewListener().(*kcpListener)
	md3 := xmetadata.NewMetadata(map[string]any{
		"kcp.datashard": 10, "kcp.parityshard": 3, "kcp.fec_off": true})
	if err := l3.parseMetadata(md3); err != nil {
		t.Fatalf("parseMetadata: %v", err)
	}
	if l3.md.config.DataShard != 0 || l3.md.config.ParityShard != 0 {
		t.Fatalf("fec_off must win over shard keys, got %d/%d",
			l3.md.config.DataShard, l3.md.config.ParityShard)
	}
}
