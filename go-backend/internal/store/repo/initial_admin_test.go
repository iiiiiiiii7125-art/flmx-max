package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/security"
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
