package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// no-nft status tests (R4C-02): on a node where the nft binary is
// absent and no table was ever built, Status must NOT report the
// missing binary as NftReadError — NftReadError means "a table we
// had is gone" (R3A-NEW-1), and the panel turns it into failing +
// a kernel re-push every poll. The never-installed case is a
// legitimate shape (minimal / unprivileged-container nodes): it only
// loses counters, so it gets its own non-failing signal,
// NftNotInstalled. Ported from the R4 audit reproduction
// (no_nft_status_audit_test.go), which ran with a PATH without nft
// and pinned the buggy NftReadError; the assertions below pin the
// fixed contract instead.
//
// The tests strip PATH to an empty directory so the real
// exec.LookPath probe fails, exactly like a minimal node without
// nftables installed.

func TestNoNftStatusReportsNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	m := NewManager(t.TempDir())
	m.instances["singbox"] = &instance{
		req:  ApplyRequest{Type: "singbox", Enabled: true, Ports: tableLossPorts, UserMarks: []int64{2}},
		logs: &logRing{},
	}
	st := m.Status("")
	if st.NftAvailable {
		t.Fatal("nft must be unavailable under the stripped PATH")
	}
	if st.NftReadError != "" {
		t.Fatalf("no-nft status carries NftReadError=%q, want empty (never-installed is not a table loss)", st.NftReadError)
	}
	if !st.NftNotInstalled {
		t.Fatal("no-nft status must set NftNotInstalled")
	}
	if len(st.Counters) != 0 || len(st.Users) != 0 {
		t.Fatalf("no-nft status counters=%v users=%v, want empty", st.Counters, st.Users)
	}
}

func TestNoNftStatusWithAppliedStateKeepsReadError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	// The agent previously built a table (nft.json survives
	// restarts); losing the nft binary afterwards is a regression,
	// not a never-installed node — it still reports NftReadError.
	if err := os.WriteFile(filepath.Join(dir, "nft.json"), []byte(`{"signature":"seed-sig","epoch":"e1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir)
	m.instances["singbox"] = &instance{
		req:  ApplyRequest{Type: "singbox", Enabled: true, Ports: tableLossPorts, UserMarks: []int64{2}},
		logs: &logRing{},
	}
	st := m.Status("")
	if st.NftAvailable {
		t.Fatal("nft must be unavailable under the stripped PATH")
	}
	if st.NftNotInstalled {
		t.Fatal("a node that previously built a table must not report NftNotInstalled")
	}
	if st.NftReadError == "" {
		t.Fatal("previously-built-table status carries no NftReadError")
	}
	if !strings.Contains(st.NftReadError, "未安装 nftables") {
		t.Fatalf("NftReadError=%q, want the nft-missing text", st.NftReadError)
	}
}
