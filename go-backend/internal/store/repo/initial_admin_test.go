package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/model"
)

// R1-12-01：新播种的管理员初始口令不得再是公开字面量。本组用例
// 钉住口令来源两路（环境指定/随机生成＋一次性文件）、播种作用域
// （存量零动作）、跨重启候选恢复与改密后失效。包级状态在每个
// 用例前后保存/复原，避免污染同进程其它用例。

func resetInitialAdminState(t *testing.T) {
	t.Helper()
	oldDir, oldCand, oldFile := initialAdminDataDir, initialAdminCandidate, initialAdminCandidateFile
	initialAdminCandidate, initialAdminCandidateFile = "", ""
	t.Cleanup(func() {
		initialAdminDataDir, initialAdminCandidate, initialAdminCandidateFile = oldDir, oldCand, oldFile
	})
}

func openInitialAdminRepo(t *testing.T) (*Repository, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, dir
}

func seededAdmin(t *testing.T, r *Repository) (pwdHash string, changedAt int64) {
	t.Helper()
	u, err := r.GetUserByUsername("admin@user")
	if err != nil || u == nil {
		t.Fatalf("get seeded admin: %v", err)
	}
	return u.Pwd, u.PasswordChangedAt
}

// 生成路径：随机口令落 0600 一次性文件、内容非字面量、哈希可验，
// 且旧公开字面量对新播种哈希必须验不过（本修复的安全属性本身）。
func TestSeedGeneratesRandomInitialAdminPassword(t *testing.T) {
	resetInitialAdminState(t)
	t.Setenv(InitialAdminPasswordEnv, "")
	r, dir := openInitialAdminRepo(t)

	hash, _ := seededAdmin(t, r)
	file := filepath.Join(dir, initialAdminPasswordFileName)
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("initial password file must exist after generated seed: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("initial password file mode = %o, want 0600", perm)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read initial password file: %v", err)
	}
	pwd := strings.TrimSpace(string(raw))
	if pwd == "admin@user" || pwd == "admin_user" {
		t.Fatalf("generated initial password must not be a public literal, got %q", pwd)
	}
	if len(pwd) < 20 {
		t.Fatalf("generated initial password length = %d, want >= 20", len(pwd))
	}
	if ok, _ := security.VerifyPassword(hash, pwd); !ok {
		t.Fatal("stored admin hash must verify against the generated initial password")
	}
	if ok, _ := security.VerifyPassword(hash, "admin@user"); ok {
		t.Fatal("stored admin hash must NOT verify against the old public literal admin@user")
	}
	if got := InitialAdminPasswordCandidate(); got != pwd {
		t.Fatalf("registered candidate = %q, want the generated password", got)
	}
}

// 环境路径：用环境值播种、不落文件、候选登记为环境值。
func TestSeedUsesEnvInitialAdminPassword(t *testing.T) {
	resetInitialAdminState(t)
	t.Setenv(InitialAdminPasswordEnv, "env-chosen-initial-1")
	r, dir := openInitialAdminRepo(t)

	hash, _ := seededAdmin(t, r)
	if ok, _ := security.VerifyPassword(hash, "env-chosen-initial-1"); !ok {
		t.Fatal("stored admin hash must verify against the env-provided initial password")
	}
	if _, err := os.Stat(filepath.Join(dir, initialAdminPasswordFileName)); !os.IsNotExist(err) {
		t.Fatalf("env path must not write the one-time file, stat err = %v", err)
	}
	if got := InitialAdminPasswordCandidate(); got != "env-chosen-initial-1" {
		t.Fatalf("registered candidate = %q, want the env value", got)
	}
}

// 存量零动作：管理员已存在时重开仓库不得改哈希、不得推进
// PasswordChangedAt、不得落文件（即便环境值已不同）。
func TestSeedExistingAdminUntouched(t *testing.T) {
	resetInitialAdminState(t)
	t.Setenv(InitialAdminPasswordEnv, "first-initial-pw")
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "panel.db")
	r1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	hash1, changed1 := seededAdmin(t, r1)
	_ = r1.Close()

	// 模拟重启：清候选、换环境值后重开同一库。
	initialAdminCandidate, initialAdminCandidateFile = "", ""
	t.Setenv(InitialAdminPasswordEnv, "second-initial-pw")
	r2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	t.Cleanup(func() { _ = r2.Close() })
	hash2, changed2 := seededAdmin(t, r2)
	if hash2 != hash1 || changed2 != changed1 {
		t.Fatal("existing admin row must be untouched by reopening (no reseed, no rehash)")
	}
	if ok, _ := security.VerifyPassword(hash2, "second-initial-pw"); ok {
		t.Fatal("changed env value must not take over an existing admin")
	}
	if _, err := os.Stat(filepath.Join(dir, initialAdminPasswordFileName)); !os.IsNotExist(err) {
		t.Fatalf("no one-time file may appear for an existing admin, stat err = %v", err)
	}
	if got := InitialAdminPasswordCandidate(); got != "" {
		t.Fatalf("candidate must stay empty when env no longer matches the stored hash, got %q", got)
	}
}

// 跨重启恢复：候选登记丢失后，启动探针凭一次性文件恢复登记
// （强制改密不因重启失效）。
func TestInitialAdminProbeRestoresCandidateAcrossRestart(t *testing.T) {
	resetInitialAdminState(t)
	t.Setenv(InitialAdminPasswordEnv, "")
	r, _ := openInitialAdminRepo(t)
	want := InitialAdminPasswordCandidate()
	if want == "" {
		t.Fatal("candidate must be registered right after generated seed")
	}
	initialAdminCandidate, initialAdminCandidateFile = "", ""
	seedData(r.db)
	if got := InitialAdminPasswordCandidate(); got != want {
		t.Fatalf("probe must restore candidate from the one-time file: got %q, want %q", got, want)
	}
}

// 改密失效：本人改密写点成功后候选清除、一次性文件删除，
// 且新口令不被裁决为初始口令（经存储哈希核对候选落空）。
func TestInitialAdminClearedOnPasswordChange(t *testing.T) {
	resetInitialAdminState(t)
	t.Setenv(InitialAdminPasswordEnv, "")
	r, dir := openInitialAdminRepo(t)
	file := filepath.Join(dir, initialAdminPasswordFileName)
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("one-time file must exist before the change: %v", err)
	}
	newHash, err := security.HashPassword("brand-new-admin-pw")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := r.UpdateUserNameAndPassword(1, "admin@user", newHash, time.Now().UnixMilli()); err != nil {
		t.Fatalf("update password: %v", err)
	}
	if got := InitialAdminPasswordCandidate(); got != "" {
		t.Fatalf("candidate must be cleared after password change, got %q", got)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("one-time file must be deleted after password change, stat err = %v", err)
	}
}

// C1-02：落库（db.Create）失败支必须与生成/落盘/哈希失败支同
// 标准置 adminBootstrapRequired——否则面板无管理员而状态面恒报
// 无事可做。用 BEFORE INSERT 触发器确定性制造非唯一约束的落库
// 失败（模拟只读/满盘类退化态）。
func TestSeedCreateFailureSetsAdminBootstrapRequired(t *testing.T) {
	resetInitialAdminState(t)
	t.Setenv(InitialAdminPasswordEnv, "env-chosen-initial-1")
	oldFlag := adminBootstrapRequired
	t.Cleanup(func() { adminBootstrapRequired = oldFlag })
	r, _ := openInitialAdminRepo(t)

	if err := r.db.Exec(`DELETE FROM "user" WHERE id = 1`).Error; err != nil {
		t.Fatalf("delete seeded admin: %v", err)
	}
	if err := r.db.Exec(`CREATE TRIGGER fail_admin_insert BEFORE INSERT ON "user" BEGIN SELECT RAISE(ABORT, 'forced create failure'); END`).Error; err != nil {
		t.Fatalf("create sabotage trigger: %v", err)
	}
	adminBootstrapRequired = false
	seedInitialAdmin(r.db)
	if !IsAdminBootstrapRequired() {
		t.Fatal("db.Create failure in seedInitialAdmin must set adminBootstrapRequired (panel has no admin; status face must say so)")
	}
	var count int64
	if err := r.db.Exec(`DROP TRIGGER fail_admin_insert`).Error; err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if err := r.db.Model(&model.User{}).Where("id = ?", 1).Count(&count).Error; err != nil {
		t.Fatalf("count admin rows: %v", err)
	} else if count != 0 {
		t.Fatalf("no admin row may exist after failed create, got %d", count)
	}
}

// FIX-CI-1：一次性文件落点不可写时不得中止播种。CI 的 PG 契约
// 形态正是落点推导出不可写的 /app 且管理员从未建成（自签 JWT 被
// 中间件复核 401）；真部署里 PG 裸二进制未设 DB_PATH、或数据卷
// 只读时同因永久无管理员。本用例以「路径穿过普通文件」构造任何
// 权限下都失败的落点（MkdirAll 恒 ENOTDIR），断言管理员仍建成。
func TestSeedAdminCreatedWhenPasswordFileUnwritable(t *testing.T) {
	resetInitialAdminState(t)
	t.Setenv(InitialAdminPasswordEnv, "")
	r, _ := openInitialAdminRepo(t)

	if err := r.db.Exec(`DELETE FROM "user" WHERE id = 1`).Error; err != nil {
		t.Fatalf("delete seeded admin: %v", err)
	}
	initialAdminCandidate, initialAdminCandidateFile = "", ""
	oldFlag := adminBootstrapRequired
	t.Cleanup(func() { adminBootstrapRequired = oldFlag })
	adminBootstrapRequired = false
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	initialAdminDataDir = filepath.Join(blocker, "data")
	seedData(r.db)

	var count int64
	if err := r.db.Model(&model.User{}).Where("id = ?", 1).Count(&count).Error; err != nil {
		t.Fatalf("count admin rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("seed must create the admin even when the one-time password file cannot be written, got %d admin rows", count)
	}
	if got := InitialAdminPasswordCandidate(); got == "" {
		t.Fatal("candidate must be registered even when the password file cannot be written")
	}
	if initialAdminCandidateFile != "" {
		written := initialAdminCandidateFile
		t.Cleanup(func() { _ = os.Remove(written) })
	}
}

// stubInitialAdminFallbacks 把落点链的后备落点换成受控目录，
// 避免用例污染真实的可执行文件目录/系统临时目录。
func stubInitialAdminFallbacks(t *testing.T, dirs ...string) {
	t.Helper()
	old := initialAdminFallbackDirs
	initialAdminFallbackDirs = func() []string { return dirs }
	t.Cleanup(func() { initialAdminFallbackDirs = old })
	oldPending := initialAdminPendingWrite
	initialAdminPendingWrite = false
	t.Cleanup(func() { initialAdminPendingWrite = oldPending })
}

// FIX-CI-1：数据目录落点不可写时，落点链应落到可写的后备落点
// （0600、哈希可验），且跨重启探针能从后备落点恢复候选登记
// （强制改密不因落点降级失效）。
func TestSeedFallsBackToWritableLanding(t *testing.T) {
	resetInitialAdminState(t)
	t.Setenv(InitialAdminPasswordEnv, "")
	r, _ := openInitialAdminRepo(t)
	if err := r.db.Exec(`DELETE FROM "user" WHERE id = 1`).Error; err != nil {
		t.Fatalf("delete seeded admin: %v", err)
	}
	initialAdminCandidate, initialAdminCandidateFile = "", ""
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	initialAdminDataDir = filepath.Join(blocker, "data")
	fallback := t.TempDir()
	stubInitialAdminFallbacks(t, fallback)
	seedData(r.db)

	wantFile := filepath.Join(fallback, initialAdminPasswordFileName)
	if initialAdminCandidateFile != wantFile {
		t.Fatalf("candidate file = %q, want fallback landing %q", initialAdminCandidateFile, wantFile)
	}
	info, err := os.Stat(wantFile)
	if err != nil {
		t.Fatalf("fallback one-time file must exist: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("fallback one-time file mode = %o, want 0600", perm)
	}
	if initialAdminPendingWrite {
		t.Fatal("pending write must be clear after a successful fallback landing")
	}
	pwd := InitialAdminPasswordCandidate()
	hash, _ := seededAdmin(t, r)
	if ok, _ := security.VerifyPassword(hash, pwd); !ok {
		t.Fatal("stored admin hash must verify against the fallback-landed initial password")
	}
	initialAdminCandidate, initialAdminCandidateFile = "", ""
	seedData(r.db)
	if got := InitialAdminPasswordCandidate(); got != pwd {
		t.Fatalf("probe must restore candidate from the fallback landing: got %q, want %q", got, pwd)
	}
}

// FIX-CI-1：落点链全败时管理员仍建成、候选只在内存登记并置
// 待重试标；落点恢复可写后，下一次 seedData 的探针补落盘清标。
func TestSeedPendingWriteRetriedByProbe(t *testing.T) {
	resetInitialAdminState(t)
	t.Setenv(InitialAdminPasswordEnv, "")
	r, _ := openInitialAdminRepo(t)
	if err := r.db.Exec(`DELETE FROM "user" WHERE id = 1`).Error; err != nil {
		t.Fatalf("delete seeded admin: %v", err)
	}
	initialAdminCandidate, initialAdminCandidateFile = "", ""
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	initialAdminDataDir = filepath.Join(blocker, "data")
	stubInitialAdminFallbacks(t, filepath.Join(blocker, "fallback"))
	seedData(r.db)

	var count int64
	if err := r.db.Model(&model.User{}).Where("id = ?", 1).Count(&count).Error; err != nil {
		t.Fatalf("count admin rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("admin must be created even when every landing fails, got %d rows", count)
	}
	if InitialAdminPasswordCandidate() == "" {
		t.Fatal("candidate must be registered in memory when every landing fails")
	}
	if !initialAdminPendingWrite {
		t.Fatal("pending write must be set when every landing fails")
	}
	if initialAdminCandidateFile != "" {
		t.Fatalf("no candidate file may be registered when every landing fails, got %q", initialAdminCandidateFile)
	}

	writable := t.TempDir()
	initialAdminFallbackDirs = func() []string { return []string{writable} }
	seedData(r.db)
	if initialAdminPendingWrite {
		t.Fatal("pending write must clear after the probe retry succeeds")
	}
	wantFile := filepath.Join(writable, initialAdminPasswordFileName)
	if initialAdminCandidateFile != wantFile {
		t.Fatalf("candidate file after retry = %q, want %q", initialAdminCandidateFile, wantFile)
	}
	if _, err := os.Stat(wantFile); err != nil {
		t.Fatalf("retried one-time file must exist: %v", err)
	}
}

// 陈旧文件清理：口令已由带外路径变更（哈希不再吻合）时，
// 启动探针删除残留的一次性文件且不登记候选。
func TestInitialAdminProbeRemovesStaleFile(t *testing.T) {
	resetInitialAdminState(t)
	t.Setenv(InitialAdminPasswordEnv, "")
	r, dir := openInitialAdminRepo(t)
	file := filepath.Join(dir, initialAdminPasswordFileName)
	otherHash, err := security.HashPassword("some-other-password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	// MigrateUserPasswordHash 是透明迁移写点（不触发改密清理），
	// 用它把存储哈希换成与文件内容不吻合的值，模拟带外变更后的
	// 陈旧文件形状。
	if err := r.MigrateUserPasswordHash(1, otherHash, time.Now().UnixMilli()); err != nil {
		t.Fatalf("migrate hash: %v", err)
	}
	initialAdminCandidate, initialAdminCandidateFile = "", ""
	seedData(r.db)
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("stale one-time file must be removed by the probe, stat err = %v", err)
	}
	if got := InitialAdminPasswordCandidate(); got != "" {
		t.Fatalf("stale file must not register a candidate, got %q", got)
	}
}
