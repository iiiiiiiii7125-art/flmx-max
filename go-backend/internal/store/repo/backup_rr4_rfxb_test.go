package repo

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// RR4-F1-01 / RR4-F4-01 回归钉：备份恢复面对订阅行自身的引用做存
// 在性对账——同批被跳过的源/入站与从未存在的幽灵 id 不再逐字落库，
// 回执汇总 warning 点名受影响订阅并真计数；全部引用有效时逐字保真。
// 本地导入经 repo.Import 同一路径，此钉覆盖恢复面。
func TestImportReconcilesSubscriptionRefs(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	createRow(t, dst, &model.Node{ID: 3, Name: "n3", Secret: "s", ServerIP: "10.0.0.3", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Kernels: []model.KernelBackup{
				{ID: 31, NodeID: 3, KernelType: "xray", Version: "1.0", Enabled: 1, CreatedTime: now},
			},
			Sources: []model.RemoteSourceBackup{
				{ID: 5, Name: "ok-src", URL: "https://ok.example.com/sub", Enabled: 1, CreatedTime: now},
				// 空标签 URL：同批被跳过（该跳过本身另有 warning）。
				{ID: 7, Name: "bad-src", URL: "http://a..b/sub", Enabled: 1, CreatedTime: now},
			},
			Inbounds: []model.InboundBackup{
				{ID: 10, KernelID: 31, NodeID: 3, Name: "in-ok", Protocol: "vless", Listen: "0.0.0.0", Port: 24443,
					Transport: `{"type":"tcp"}`, Security: `{"type":"none"}`, Settings: `{}`, Enabled: 1, CreatedTime: now},
				// 内核 999 不存在：同批被跳过。
				{ID: 9, KernelID: 999, NodeID: 3, Name: "in-orphan", Protocol: "vless", Listen: "0.0.0.0", Port: 24444,
					Transport: `{"type":"tcp"}`, Security: `{"type":"none"}`, Settings: `{}`, Enabled: 1, CreatedTime: now},
			},
			Subscriptions: []model.SubscriptionBackup{
				{ID: 51, Name: "甲", Kind: "share", Token: "tokrefa000000000000000000000000a", UUID: "u-a", Password: "p-a",
					SourceIDs: "5,999", InboundIDs: "9,10",
					Options: `{"chainLandingSources":[998],"chainRelaySources":[999],"chainSourceFilters":{"5":"ok","999":"bad"}}`,
					Enabled: 1, CreatedTime: now},
				{ID: 52, Name: "乙", Kind: "share", Token: "tokrefb000000000000000000000000b", UUID: "u-b", Password: "p-b",
					SourceIDs: "7", Enabled: 1, CreatedTime: now},
				{ID: 53, Name: "丙", Kind: "share", Token: "tokrefc000000000000000000000000c", UUID: "u-c", Password: "p-c",
					SourceIDs: "5", InboundIDs: "10", Options: `{"chainRelaySources":[5]}`,
					Enabled: 1, CreatedTime: now},
			},
		},
	}
	result, err := dst.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}

	subByToken := func(token string) model.Subscription {
		t.Helper()
		var row model.Subscription
		if err := dst.DB().Where("token = ?", token).First(&row).Error; err != nil {
			t.Fatalf("load subscription %s: %v", token, err)
		}
		return row
	}

	a := subByToken("tokrefa000000000000000000000000a")
	if a.SourceIDs != "5" {
		t.Errorf("甲 SourceIDs = %q, want %q (ghost 999 stripped)", a.SourceIDs, "5")
	}
	if a.InboundIDs != "10" {
		t.Errorf("甲 InboundIDs = %q, want %q (skipped inbound 9 stripped)", a.InboundIDs, "10")
	}
	var aOpts struct {
		ChainLandingSources []int64          `json:"chainLandingSources"`
		ChainRelaySources   []int64          `json:"chainRelaySources"`
		ChainSourceFilters  map[int64]string `json:"chainSourceFilters"`
	}
	if err := json.Unmarshal([]byte(a.Options), &aOpts); err != nil {
		t.Fatalf("甲 Options unparseable after restore: %v (%q)", err, a.Options)
	}
	if len(aOpts.ChainLandingSources) != 0 || len(aOpts.ChainRelaySources) != 0 {
		t.Errorf("甲 chain sources = %v/%v, want both empty (998/999 stripped)",
			aOpts.ChainLandingSources, aOpts.ChainRelaySources)
	}
	if len(aOpts.ChainSourceFilters) != 1 || aOpts.ChainSourceFilters[5] != "ok" {
		t.Errorf("甲 chainSourceFilters = %v, want only key 5 kept", aOpts.ChainSourceFilters)
	}

	b := subByToken("tokrefb000000000000000000000000b")
	if b.SourceIDs != "" {
		t.Errorf("乙 SourceIDs = %q, want empty (source 7 skipped in the same import)", b.SourceIDs)
	}

	c := subByToken("tokrefc000000000000000000000000c")
	if c.SourceIDs != "5" || c.InboundIDs != "10" || c.Options != `{"chainRelaySources":[5]}` {
		t.Errorf("丙 must restore verbatim, got SourceIDs=%q InboundIDs=%q Options=%q",
			c.SourceIDs, c.InboundIDs, c.Options)
	}

	warnText := strings.Join(result.ImportWarnings, "\n")
	if !strings.Contains(warnText, "2 条订阅引用了未恢复的订阅源/入站") {
		t.Errorf("receipt must summarize 2 affected subscriptions, warnings=%v", result.ImportWarnings)
	}
	if !strings.Contains(warnText, "已剔除悬空引用 4 处（源 3、入站 1）") {
		t.Errorf("receipt must carry true strip counts （源 3、入站 1), warnings=%v", result.ImportWarnings)
	}
	if !strings.Contains(warnText, "「甲」") || !strings.Contains(warnText, "999") || !strings.Contains(warnText, "998") ||
		!strings.Contains(warnText, "「乙」") {
		t.Errorf("receipt must name affected subscriptions and stripped ids, warnings=%v", result.ImportWarnings)
	}
	if strings.Contains(warnText, "「丙」 源引用") || strings.Contains(warnText, "「丙」 入站引用") {
		t.Errorf("丙 has no dangling refs and must not be named as affected, warnings=%v", result.ImportWarnings)
	}
}

// RR4-F2-01 回归钉：恢复面验什么存什么——靠 Normalize 生成/愈合才
// 合法的入站行以愈合形态落库（ENC 空 secret、UUID/Reality 钥空、
// auth/flow 带空格），不再逐字落备份原文；本身合
// 法的行保真。（原 SnellVersion 0 愈合行已随自建 Snell 入站整删删除。）
func TestImportStoresNormalizedInboundObjects(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	createRow(t, dst, &model.Node{ID: 3, Name: "n3", Secret: "s", ServerIP: "10.0.0.3", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Kernels: []model.KernelBackup{
				{ID: 31, NodeID: 3, KernelType: "xray", Version: "1.0", Enabled: 1, CreatedTime: now},
				{ID: 32, NodeID: 3, KernelType: "singbox", Version: "1.0", Enabled: 1, CreatedTime: now},
			},
			Inbounds: []model.InboundBackup{
				// ENC 开 + auth 带空格 + secret 空：副本靠 trim+生成通过。
				{ID: 10, KernelID: 31, NodeID: 3, Name: "enc", Protocol: "vless", Listen: "0.0.0.0", Port: 24443,
					Transport: `{"type":"tcp"}`, Security: `{"type":"none"}`,
					Settings: `{"encryptionAuth":" mlkem768 ","encryptionSecret":""}`, Enabled: 1, CreatedTime: now},
				// UUID 空 + Reality 钥空：副本靠生成通过。
				{ID: 11, KernelID: 31, NodeID: 3, Name: "reality", Protocol: "vless", Listen: "0.0.0.0", Port: 24444,
					Transport: `{"type":"tcp"}`, Security: `{"type":"reality","sni":"www.example.com"}`,
					Settings: `{}`, Enabled: 1, CreatedTime: now},
				// flow 带空格：副本靠 trim 通过（Vision 组合 TCP+Reality 成立）。
				{ID: 12, KernelID: 31, NodeID: 3, Name: "vision", Protocol: "vless", Listen: "0.0.0.0", Port: 24445,
					Transport: `{"type":"tcp"}`, Security: `{"type":"reality","sni":"www.example.com"}`,
					Settings: `{"uuid":"11111111-2222-3333-4444-555555555555","flow":" xtls-rprx-vision "}`,
					Enabled:  1, CreatedTime: now},
				// 本身合法：UUID 保真。
				{ID: 14, KernelID: 31, NodeID: 3, Name: "legal", Protocol: "vless", Listen: "0.0.0.0", Port: 24447,
					Transport: `{"type":"tcp"}`, Security: `{"type":"none"}`,
					Settings: `{"uuid":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}`, Enabled: 1, CreatedTime: now},
			},
		},
	}
	result, err := dst.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}
	if result.InboundsImported != 4 {
		t.Fatalf("InboundsImported = %d, want 4, warnings=%v", result.InboundsImported, result.ImportWarnings)
	}

	landed := func(id int64) (kernel.Settings, kernel.Security) {
		t.Helper()
		var row model.NodeInbound
		if err := dst.DB().Where("id = ?", id).First(&row).Error; err != nil {
			t.Fatalf("load inbound %d: %v", id, err)
		}
		var st kernel.Settings
		if strings.TrimSpace(row.Settings) != "" {
			if err := json.Unmarshal([]byte(row.Settings), &st); err != nil {
				t.Fatalf("inbound %d settings unparseable: %v (%q)", id, err, row.Settings)
			}
		}
		var sec kernel.Security
		if strings.TrimSpace(row.Security) != "" {
			if err := json.Unmarshal([]byte(row.Security), &sec); err != nil {
				t.Fatalf("inbound %d security unparseable: %v (%q)", id, err, row.Security)
			}
		}
		return st, sec
	}

	enc, _ := landed(10)
	if enc.EncryptionAuth != "mlkem768" {
		t.Errorf("ENC row auth = %q, want trimmed %q", enc.EncryptionAuth, "mlkem768")
	}
	if enc.EncryptionSecret == "" {
		t.Errorf("ENC row secret must be generated at restore, landed settings=%+v", enc)
	}
	if _, err := enc.EncryptionString(); err != nil {
		t.Errorf("restored ENC row must derive its client encryption string, got %v", err)
	}

	rl, rlSec := landed(11)
	if rl.UUID == "" {
		t.Errorf("Reality row UUID must be generated at restore")
	}
	if rlSec.RealityPrivateKey == "" || rlSec.RealityPublicKey == "" {
		t.Errorf("Reality keys must be generated at restore, landed security=%+v", rlSec)
	}

	vs, _ := landed(12)
	if vs.Flow != "xtls-rprx-vision" {
		t.Errorf("Vision row flow = %q, want trimmed %q", vs.Flow, "xtls-rprx-vision")
	}
	if vs.UUID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("Vision row UUID must be preserved, got %q", vs.UUID)
	}

	lg, _ := landed(14)
	if lg.UUID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("legal row UUID must round-trip verbatim, got %q", lg.UUID)
	}
}
