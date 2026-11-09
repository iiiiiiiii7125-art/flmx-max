package handler

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// A4-V-06 落地核验：面板握有下发目标与重连自报双边事实，必须自
// 动对账。本组测试覆盖命中核销、未命中只观察、超时置 failed 并
// 经通知面告警恰好一次、面板重启后记录仍可核销/置 failed、回
// 退同口径、迟到命中翻正。

func setupUpgradeVerifyHandler(t *testing.T) (*Handler, *repo.Repository) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "uv.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return New(r, "unit-test-secret"), r
}

func insertUpgradeVerifyNode(t *testing.T, r *repo.Repository, id int64, name, version string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(id, name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, '10.0.0.1', '10.0.0.1', '', '40000-40010', '', ?, 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0)
	`, id, name, "secret-x", version, now, now).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
}

func setNodeReportedVersion(t *testing.T, r *repo.Repository, id int64, version string) {
	t.Helper()
	if err := r.UpdateNodeOnline(id, 1, version, 0, 0, 0); err != nil {
		t.Fatalf("update node version: %v", err)
	}
}

func getVerifyRecord(t *testing.T, r *repo.Repository, id int64) *model.NodeUpgradeVerification {
	t.Helper()
	rec, err := r.GetNodeUpgradeVerification(id)
	if err != nil {
		t.Fatalf("get verification record: %v", err)
	}
	return rec
}

func TestUpgradeVerifyTargetMatches(t *testing.T) {
	cases := []struct {
		target, reported string
		want             bool
	}{
		{"2.1.0", "2.1.0 (debian/amd64)", true},
		{"v2.1.0", "2.1.0 (debian/amd64)", true},
		{"2.1.0", "2.0.0 (debian/amd64)", false},
		{"2.1.0", "", false},
		{"2.1.0", "dev", false},
		// 粘连 dev 后缀提取失败 → fail-closed 不命中（与升级前置
		// 比对同口径），不得把 dev 构建误判为正式版落地。
		{"2.1.0", "2.1.0-dev.int8 (debian/amd64)", false},
	}
	for _, c := range cases {
		if got := upgradeVerifyTargetMatches(c.target, c.reported); got != c.want {
			t.Fatalf("matches(%q, %q) = %v, want %v", c.target, c.reported, got, c.want)
		}
	}
}

func TestVerifyLandingMatchResolves(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-match", "2.0.0 (debian/amd64)")
	h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", "sha-x")
	if rec := getVerifyRecord(t, r, 7); rec == nil || rec.Status != repo.NodeUpgradeVerifyPending || rec.FromVersion != "2.0.0 (debian/amd64)" {
		t.Fatalf("record after dispatch mismatch: %+v", rec)
	}
	setNodeReportedVersion(t, r, 7, "2.1.0 (debian/amd64)")
	h.verifyNodeUpgradeLanding(7)
	rec := getVerifyRecord(t, r, 7)
	if rec.Status != repo.NodeUpgradeVerifyLanded || rec.ObservedVersion != "2.1.0 (debian/amd64)" || rec.Late != 0 || rec.ResolvedAt == 0 {
		t.Fatalf("match must resolve landed: %+v", rec)
	}
}

func TestVerifyMismatchOnlyObserves(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-mismatch", "2.0.0 (debian/amd64)")
	h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", "")
	// 旧进程残连/自动回滚后的旧版回连：只记 observed，不判死刑。
	h.verifyNodeUpgradeLanding(7)
	rec := getVerifyRecord(t, r, 7)
	if rec.Status != repo.NodeUpgradeVerifyPending || rec.ObservedVersion != "2.0.0 (debian/amd64)" {
		t.Fatalf("mismatch must stay pending with observation: %+v", rec)
	}
	// 之后真新版回连，照常核销。
	setNodeReportedVersion(t, r, 7, "2.1.0 (debian/amd64)")
	h.verifyNodeUpgradeLanding(7)
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyLanded {
		t.Fatalf("later match must land: %+v", rec)
	}
}

func TestVerifyRollbackRecordSameCaliber(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-rollback", "2.1.0 (debian/amd64)")
	h.recordNodeUpgradeVerification(7, upgradeVerifyKindRollback, "2.0.0", "")
	setNodeReportedVersion(t, r, 7, "2.0.0 (debian/amd64)")
	h.verifyNodeUpgradeLanding(7)
	rec := getVerifyRecord(t, r, 7)
	if rec.Status != repo.NodeUpgradeVerifyLanded || rec.Kind != upgradeVerifyKindRollback {
		t.Fatalf("rollback verification mismatch: %+v", rec)
	}
}

func TestRecordSkipsUnknownTarget(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-legacy", "2.1.0 (debian/amd64)")
	// 无代际配对的老 agent 回退：面板不知道目标，不落记录。
	h.recordNodeUpgradeVerification(7, upgradeVerifyKindRollback, "", "")
	if rec := getVerifyRecord(t, r, 7); rec != nil {
		t.Fatalf("unknown target must not create a record: %+v", rec)
	}
}

func TestSweepFailsOverdueAndAlertsOnce(t *testing.T) {
	h, r, sent := setupNotifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-overdue", "2.0.0 (debian/amd64)")
	past := time.Now().Add(-10 * time.Minute).UnixMilli()
	deadline := time.Now().Add(-5 * time.Minute).UnixMilli()
	if err := r.UpsertNodeUpgradeVerification(&model.NodeUpgradeVerification{
		NodeID: 7, Kind: upgradeVerifyKindUpgrade, TargetVersion: "2.1.0",
		FromVersion: "2.0.0 (debian/amd64)", DispatchedAt: past, DeadlineAt: deadline,
		CreatedTime: past, UpdatedTime: past,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	h.sweepNodeUpgradeVerifications(time.Now())
	rec := getVerifyRecord(t, r, 7)
	if rec.Status != repo.NodeUpgradeVerifyFailed {
		t.Fatalf("overdue pending must resolve failed: %+v", rec)
	}
	msgs := sent()
	if len(msgs) != 1 {
		t.Fatalf("exactly one alert expected, got %d: %v", len(msgs), msgs)
	}
	if msgs[0].Event != notifyEventNodeUpgradeFailed || msgs[0].Title != "节点升级未落地" {
		t.Fatalf("alert event/title mismatch: %+v", msgs[0])
	}
	if !strings.Contains(msgs[0].Message, "node-overdue") || !strings.Contains(msgs[0].Message, "2.1.0") || !strings.Contains(msgs[0].Message, "2.0.0 (debian/amd64)") {
		t.Fatalf("alert must name node, expected and actual versions: %q", msgs[0].Message)
	}
	// 第二轮扫描不得重复告警（记录已 failed、不在 due 集）。
	h.sweepNodeUpgradeVerifications(time.Now())
	if msgs := sent(); len(msgs) != 1 {
		t.Fatalf("second sweep must not re-alert, got %d messages", len(msgs))
	}
}

func TestSweepKeepsWithinWindowPending(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-waiting", "2.0.0 (debian/amd64)")
	h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", "")
	if failures := h.failDueUpgradeVerifications(time.Now()); len(failures) != 0 {
		t.Fatalf("within-window record must not fail: %+v", failures)
	}
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyPending {
		t.Fatalf("record must stay pending: %+v", rec)
	}
}

func TestLateLandingFlipsFailedRecord(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-late", "2.0.0 (debian/amd64)")
	past := time.Now().Add(-10 * time.Minute).UnixMilli()
	if err := r.UpsertNodeUpgradeVerification(&model.NodeUpgradeVerification{
		NodeID: 7, Kind: upgradeVerifyKindUpgrade, TargetVersion: "2.1.0",
		DispatchedAt: past, DeadlineAt: past, CreatedTime: past, UpdatedTime: past,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	h.sweepNodeUpgradeVerifications(time.Now())
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyFailed {
		t.Fatalf("precondition failed state: %+v", rec)
	}
	// 窗口外才以目标版本回连：翻 landed 并标 late，状态面与版本
	// 列不再矛盾；告警已发出、不撤回（不在本测试断言面）。
	setNodeReportedVersion(t, r, 7, "2.1.0 (debian/amd64)")
	h.verifyNodeUpgradeLanding(7)
	rec := getVerifyRecord(t, r, 7)
	if rec.Status != repo.NodeUpgradeVerifyLanded || rec.Late != 1 {
		t.Fatalf("late landing must flip with Late=1: %+v", rec)
	}
}

func TestVerificationSurvivesPanelRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uv-restart.db")
	r1, err := repo.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	h1 := New(r1, "unit-test-secret")
	insertUpgradeVerifyNode(t, r1, 7, "node-restart", "2.0.0 (debian/amd64)")
	h1.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", "")
	if err := r1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 面板重启：全新 Handler / Repository（无任何内存态），同一
	// 库文件。待核验记录必须仍可被重连核销。
	r2, err := repo.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = r2.Close() }()
	h2 := New(r2, "unit-test-secret")
	setNodeReportedVersion(t, r2, 7, "2.1.0 (debian/amd64)")
	h2.verifyNodeUpgradeLanding(7)
	if rec := getVerifyRecord(t, r2, 7); rec == nil || rec.Status != repo.NodeUpgradeVerifyLanded {
		t.Fatalf("pending record must resolve after panel restart: %+v", rec)
	}

	// 同一重启语义下，过期的记录必须能被新进程的首轮扫描置
	// failed（内存态实现会在这里丢单）。
	if err := r2.UpsertNodeUpgradeVerification(&model.NodeUpgradeVerification{
		NodeID: 7, Kind: upgradeVerifyKindUpgrade, TargetVersion: "2.2.0",
		DispatchedAt: 1000, DeadlineAt: 2000, CreatedTime: 1000, UpdatedTime: 1000,
	}); err != nil {
		t.Fatalf("upsert overdue: %v", err)
	}
	failures := h2.failDueUpgradeVerifications(time.Now())
	if len(failures) != 1 || failures[0].Target != "2.2.0" {
		t.Fatalf("overdue record must fail after restart: %+v", failures)
	}
}
