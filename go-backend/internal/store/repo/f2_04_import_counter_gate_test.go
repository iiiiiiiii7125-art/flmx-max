package repo

import (
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
)

// F2-04: the restore face must gate every traffic counter. Before the
// fix, importers copied counter fields verbatim, so a crafted backup
// could restore a negative counter base (a durable quota bypass —
// every later check sums real traffic onto the negative base) and
// the import tail never re-evaluated quota before repushing
// status=1 forwards.
func TestImportNegativeCountersClampedF204(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "f204.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	if err := r.DB().Create(&model.Node{ID: 701, Name: "n701", Secret: "s701", ServerIP: "203.0.113.1", Port: "1000-2000", CreatedTime: 1000, Status: 1}).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if err := r.DB().Create(&model.Tunnel{ID: 601, Name: "t601", TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: 1000, UpdatedTime: 1000, Status: 1}).Error; err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}

	backup := &model.BackupData{Version: "1.0",
		Users: []model.UserBackup{{
			ID: 501, User: "victim", RoleID: 1, Status: 1,
			InFlow: -1099511627776, OutFlow: -5,
			DailyUsedBytes: -5000000000, MonthlyUsedBytes: -5000000000,
			QuotaDayKey: 20261105, QuotaMonthKey: 202611,
		}},
		Forwards: []model.ForwardBackup{{
			ID: 801, UserID: 501, UserName: "victim", Name: "f801", TunnelID: 601,
			RemoteAddr: "1.1.1.1:80", Status: 1, InFlow: -100, OutFlow: -200,
		}},
		UserTunnels: []model.UserTunnelBackup{{
			ID: 901, UserID: 501, TunnelID: 601, Status: 1, InFlow: -300, OutFlow: -400,
		}},
		PeerShares: []model.PeerShareBackup{{
			ID: 1001, Name: "share1001", NodeID: 701, CurrentFlow: -777,
		}},
		Inbounds: &model.InboundsBackup{
			Kernels: []model.KernelBackup{{
				ID: 1100, NodeID: 701, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: 1000,
			}},
			Inbounds: []model.InboundBackup{{
				ID: 1101, KernelID: 1100, NodeID: 701, Name: "in1101", Protocol: "vless", Port: 443, Enabled: 1,
				Settings:  `{"uuid":"11111111-2222-3333-4444-555555555555"}`,
				Transport: `{"type":"tcp"}`,
				Security:  `{"type":"none"}`,
				UploadBytes: -111, DownloadBytes: -222,
			}},
			Subscriptions: []model.SubscriptionBackup{{
				ID: 1201, Name: "sub1201", Kind: "share", Token: strings.Repeat("d", 32),
				Enabled: 1, UploadBytes: -333, DownloadBytes: -444, TrafficLimit: 1048576,
			}},
			Counters: []model.InboundCounterBackup{{
				InboundID: 1101, Protocol: "vless", Direction: "in", Epoch: "e1", Bytes: -555,
			}},
			SubscriptionCounters: []model.SubscriptionCounterBackup{{
				NodeID: 701, SubscriptionID: 1201, Direction: "down", Epoch: "e1", Bytes: -666,
			}},
		},
	}
	res, err := r.Import(backup, []string{"users", "nodes", "forwards", "userTunnels", "inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	var user model.User
	if err := r.DB().First(&user, 501).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if user.InFlow != 0 || user.OutFlow != 0 {
		t.Fatalf("F2-04: user counters landed verbatim: in=%d out=%d, want 0/0", user.InFlow, user.OutFlow)
	}
	var quota model.UserQuota
	if err := r.DB().Where("user_id = ?", 501).First(&quota).Error; err != nil {
		t.Fatalf("load quota: %v", err)
	}
	if quota.DailyUsedBytes != 0 || quota.MonthlyUsedBytes != 0 {
		t.Fatalf("F2-04: quota used bytes landed verbatim: daily=%d monthly=%d, want 0/0", quota.DailyUsedBytes, quota.MonthlyUsedBytes)
	}
	var fwd model.Forward
	if err := r.DB().First(&fwd, 801).Error; err != nil {
		t.Fatalf("load forward: %v", err)
	}
	if fwd.InFlow != 0 || fwd.OutFlow != 0 {
		t.Fatalf("F2-04: forward counters landed verbatim: in=%d out=%d, want 0/0", fwd.InFlow, fwd.OutFlow)
	}
	var ut model.UserTunnel
	if err := r.DB().First(&ut, 901).Error; err != nil {
		t.Fatalf("load user tunnel: %v", err)
	}
	if ut.InFlow != 0 || ut.OutFlow != 0 {
		t.Fatalf("F2-04: user-tunnel counters landed verbatim: in=%d out=%d, want 0/0", ut.InFlow, ut.OutFlow)
	}
	var share model.PeerShare
	if err := r.DB().First(&share, 1001).Error; err != nil {
		t.Fatalf("load peer share: %v", err)
	}
	if share.CurrentFlow != 0 {
		t.Fatalf("F2-04: peer-share CurrentFlow landed verbatim: %d, want 0", share.CurrentFlow)
	}
	var inb model.NodeInbound
	if err := r.DB().First(&inb, 1101).Error; err != nil {
		t.Fatalf("load inbound: %v", err)
	}
	if inb.UploadBytes != 0 || inb.DownloadBytes != 0 {
		t.Fatalf("F2-04: inbound counters landed verbatim: up=%d down=%d, want 0/0", inb.UploadBytes, inb.DownloadBytes)
	}
	// Subscriptions restore under a fresh row ID (the importer remaps
	// by token), so look the restored row up by its token.
	var sub model.Subscription
	if err := r.DB().Where("token = ?", strings.Repeat("d", 32)).First(&sub).Error; err != nil {
		t.Fatalf("load subscription: %v", err)
	}
	if sub.UploadBytes != 0 || sub.DownloadBytes != 0 {
		t.Fatalf("F2-04: subscription counters landed verbatim: up=%d down=%d, want 0/0", sub.UploadBytes, sub.DownloadBytes)
	}
	var inbCounter model.NodeInboundCounter
	if err := r.DB().Where("inbound_id = ?", 1101).First(&inbCounter).Error; err != nil {
		t.Fatalf("load inbound baseline: %v", err)
	}
	if inbCounter.Bytes != 0 {
		t.Fatalf("F2-04: inbound baseline landed verbatim: %d, want 0", inbCounter.Bytes)
	}
	var subCounter model.SubscriptionCounter
	if err := r.DB().Where("subscription_id = ?", sub.ID).First(&subCounter).Error; err != nil {
		t.Fatalf("load subscription baseline: %v", err)
	}
	if subCounter.Bytes != 0 {
		t.Fatalf("F2-04: subscription baseline landed verbatim: %d, want 0", subCounter.Bytes)
	}

	// The clamping must be disclosed in the import warnings, naming
	// the affected rows — not silently normalised.
	joined := strings.Join(res.ImportWarnings, "\n")
	if !strings.Contains(joined, "负值") {
		t.Fatalf("F2-04: import warnings do not disclose counter clamping: %v", res.ImportWarnings)
	}

	// Positive counters (including legitimately huge ones) restore
	// untouched: the gate is the negative range only.
	backup2 := &model.BackupData{Version: "1.0",
		Users: []model.UserBackup{{ID: 502, User: "honest", RoleID: 1, Status: 1, InFlow: 12345, OutFlow: 6789}},
	}
	if _, err := r.Import(backup2, []string{"users"}); err != nil {
		t.Fatalf("import honest user: %v", err)
	}
	var honest model.User
	if err := r.DB().First(&honest, 502).Error; err != nil {
		t.Fatalf("load honest user: %v", err)
	}
	if honest.InFlow != 12345 || honest.OutFlow != 6789 {
		t.Fatalf("F2-04: positive counters mangled: in=%d out=%d, want 12345/6789", honest.InFlow, honest.OutFlow)
	}
}
