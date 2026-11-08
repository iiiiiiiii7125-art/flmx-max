package kcp

import (
	"testing"

	xmetadata "github.com/go-gost/x/metadata"
)

// FIX-KCPMEM（P1-05）：面板关闭档下发的 kcp.fec_off 标记必须把
// FEC 冗余比归零——构造法证据：不带标记时缺省 10/3，带标记时
// DataShard/ParityShard 均为 0，编码器据此不产冗余分片。
func TestFecOffMarkerZeroesShards(t *testing.T) {
	d := NewDialer().(*kcpDialer)
	md := xmetadata.NewMetadata(map[string]any{"kcp.fec_off": true})
	if err := d.parseMetadata(md); err != nil {
		t.Fatalf("parseMetadata: %v", err)
	}
	if d.md.config.DataShard != 0 || d.md.config.ParityShard != 0 {
		t.Fatalf("fec_off must zero shards, got %d/%d",
			d.md.config.DataShard, d.md.config.ParityShard)
	}

	d2 := NewDialer().(*kcpDialer)
	md2 := xmetadata.NewMetadata(map[string]any{"kcp.datashard": 10, "kcp.parityshard": 3})
	if err := d2.parseMetadata(md2); err != nil {
		t.Fatalf("parseMetadata: %v", err)
	}
	if d2.md.config.DataShard != 10 || d2.md.config.ParityShard != 3 {
		t.Fatalf("default mode must keep 10/3, got %d/%d",
			d2.md.config.DataShard, d2.md.config.ParityShard)
	}

	// 标记胜过显式 shard 键（面板关闭档不下发 shard 键，但存量
	// 配置文件可能残留，两者同在时必须以关闭为准）。
	d3 := NewDialer().(*kcpDialer)
	md3 := xmetadata.NewMetadata(map[string]any{
		"kcp.datashard": 10, "kcp.parityshard": 3, "kcp.fec_off": true})
	if err := d3.parseMetadata(md3); err != nil {
		t.Fatalf("parseMetadata: %v", err)
	}
	if d3.md.config.DataShard != 0 || d3.md.config.ParityShard != 0 {
		t.Fatalf("fec_off must win over shard keys, got %d/%d",
			d3.md.config.DataShard, d3.md.config.ParityShard)
	}
}
