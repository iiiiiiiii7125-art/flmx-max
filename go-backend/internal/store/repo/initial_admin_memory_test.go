package repo

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// E-01 (AUDIT-R215)：Open(":memory:") 不得把初始管理员口令文件
// 播种进进程 CWD。根因是 filepath.Dir(":memory:") 得 "."，
// setInitialAdminDataDir(".") 把数据目录置为 CWD，播种随即在
// 测试包目录重生 ./initial-admin-password.txt（仅靠 .gitignore
// 兜底）。修法：Open 在 path == ":memory:" 时显式置空数据目录，
// 文件面关闭、仅内存候选生效；本用例钉住该行为：播种照常发生
// （内存候选已登记），但 CWD 与包目录均无文件生成。
func TestOpenMemoryDoesNotSeedPasswordFileIntoCWD(t *testing.T) {
	oldDir, oldCand, oldFile, oldPending, oldBootstrap :=
		initialAdminDataDir, initialAdminCandidate, initialAdminCandidateFile, initialAdminPendingWrite, adminBootstrapRequired
	initialAdminCandidate, initialAdminCandidateFile, initialAdminPendingWrite = "", "", false
	t.Cleanup(func() {
		initialAdminDataDir, initialAdminCandidate, initialAdminCandidateFile, initialAdminPendingWrite, adminBootstrapRequired =
			oldDir, oldCand, oldFile, oldPending, oldBootstrap
	})
	// 生成路径（非环境值）：若数据目录仍为 "."，播种必落文件。
	t.Setenv(InitialAdminPasswordEnv, "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	_, thisFile, _, _ := runtime.Caller(0)
	pkgDir := filepath.Dir(thisFile)
	targets := []string{
		filepath.Join(cwd, initialAdminPasswordFileName),
		filepath.Join(pkgDir, initialAdminPasswordFileName),
	}
	// 先清残留（此前轮次重生的文件），使本用例只量度本次 Open。
	for _, p := range targets {
		_ = os.Remove(p)
	}
	t.Cleanup(func() {
		for _, p := range targets {
			_ = os.Remove(p)
		}
	})

	r, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open :memory: repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// 播种照常发生，但只登记内存候选、不落文件。
	if got := InitialAdminPasswordCandidate(); got == "" {
		t.Fatal("seed must still register the initial admin password candidate in memory")
	}
	if initialAdminCandidateFile != "" {
		t.Fatalf("candidate file = %q, want empty (memory-only seed)", initialAdminCandidateFile)
	}
	if initialAdminDataDir != "" {
		t.Fatalf("initial admin data dir = %q after Open(\":memory:\"), want empty", initialAdminDataDir)
	}
	if !initialAdminPendingWrite {
		t.Fatal("generated seed with the file face closed must leave the pending-write flag set")
	}
	for _, p := range targets {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("initial admin password file must not be created by Open(\":memory:\") + seed: %s (stat err = %v)", p, err)
		}
	}
}
