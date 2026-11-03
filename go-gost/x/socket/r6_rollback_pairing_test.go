package socket

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// R6-05 regression gates: the rollback backup pairing. The panel
// upgrade path and the install.sh script update path both replace
// the agent binary, but only the panel path maintains
// flux_agent.old — a script update in between used to make a panel
// rollback silently skip a generation (A->B by panel, B->C by
// script, rollback lands on A) with a receipt that never named the
// landing version. planAgentRollback must now prove the pairing
// before any script is launched, and name the landing version in
// every outcome.

// writeFakeAgentBinary writes an executable shell script that
// answers -V with the agent banner for the given version. The
// marker makes same-version files differ in content (and hash).
func writeFakeAgentBinary(t *testing.T, dir, name, version, marker string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake binaries are POSIX shell scripts")
	}
	path := filepath.Join(dir, name)
	body := "#!/bin/sh\nif [ \"$1\" = \"-V\" ]; then echo \"gost " + version + " (go1.25.0 linux/amd64)\"; fi\n# " + marker + "\n"
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func pairMeta(t *testing.T, backupPath, installedPath, installedVersion string) {
	t.Helper()
	backupHash, err := hashFileSHA256(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	installedHash, err := hashFileSHA256(installedPath)
	if err != nil {
		t.Fatal(err)
	}
	meta := &agentBackupMeta{BackupSHA256: backupHash, InstalledVersion: installedVersion, InstalledSHA256: installedHash}
	if err := writeAgentBackupMeta(backupPath, meta); err != nil {
		t.Fatal(err)
	}
}

func TestPlanAgentRollbackNoBackup(t *testing.T) {
	dir := t.TempDir()
	_, err := planAgentRollback(filepath.Join(dir, "flux_agent"), filepath.Join(dir, "flux_agent.old"))
	if err == nil || !strings.Contains(err.Error(), "没有可用的备份文件") {
		t.Fatalf("missing backup must refuse with the clean miss, got: %v", err)
	}
}

func TestPlanAgentRollbackPreviousGeneration(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeAgentBinary(t, dir, "flux_agent", "2.0.0", "current-B")
	backup := writeFakeAgentBinary(t, dir, "flux_agent.old", "1.0.0", "backup-A")
	pairMeta(t, backup, bin, "2.0.0")

	plan, err := planAgentRollback(bin, backup)
	if err != nil {
		t.Fatalf("paired rollback must be admitted: %v", err)
	}
	if plan.Generation != rollbackGenerationPrevious {
		t.Fatalf("generation = %q, want %q", plan.Generation, rollbackGenerationPrevious)
	}
	if plan.FromVersion != "2.0.0" || plan.ToVersion != "1.0.0" {
		t.Fatalf("plan must name both versions, got from=%q to=%q", plan.FromVersion, plan.ToVersion)
	}
}

// The defect cell itself: panel upgrade A->B (backup=A, paired with
// B), then a script update replaces the binary with C. The backup
// is no longer the immediately previous generation; the rollback
// must refuse and name all three versions rather than silently
// landing on A.
func TestPlanAgentRollbackScriptUpdateBetweenRefuses(t *testing.T) {
	dir := t.TempDir()
	installedB := writeFakeAgentBinary(t, dir, "installed-b", "2.0.0", "panel-installed-B")
	backup := writeFakeAgentBinary(t, dir, "flux_agent.old", "1.0.0", "backup-A")
	pairMeta(t, backup, installedB, "2.0.0")
	currentC := writeFakeAgentBinary(t, dir, "flux_agent", "3.0.0", "script-installed-C")

	_, err := planAgentRollback(currentC, backup)
	if err == nil {
		t.Fatal("stale-generation backup must be refused")
	}
	for _, want := range []string{"不是紧邻上一代", "2.0.0", "3.0.0", "1.0.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal must name %q, got: %v", want, err)
		}
	}
}

// Legacy backups (written before pairing records existed) still
// roll back — the fleet's existing .old files must not be stranded
// — with the generation reported as unverified and the landing
// version named.
func TestPlanAgentRollbackLegacyBackupAdmitted(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeAgentBinary(t, dir, "flux_agent", "2.0.0", "current-B")
	backup := writeFakeAgentBinary(t, dir, "flux_agent.old", "1.0.0", "backup-A")

	plan, err := planAgentRollback(bin, backup)
	if err != nil {
		t.Fatalf("legacy backup must be admitted: %v", err)
	}
	if plan.Generation != rollbackGenerationLegacy {
		t.Fatalf("generation = %q, want %q", plan.Generation, rollbackGenerationLegacy)
	}
	if plan.ToVersion != "1.0.0" {
		t.Fatalf("landing version must be named, got %q", plan.ToVersion)
	}
}

func TestPlanAgentRollbackAlreadyAtBackup(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeAgentBinary(t, dir, "flux_agent", "1.0.0", "same-bytes")
	backup := filepath.Join(dir, "flux_agent.old")
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, data, 0755); err != nil {
		t.Fatal(err)
	}
	_, err = planAgentRollback(bin, backup)
	if err == nil || !strings.Contains(err.Error(), "已在备份版本") {
		t.Fatalf("rollback onto the running bytes must refuse as a no-op, got: %v", err)
	}
}

// A backup replaced after its pairing record was written (content
// no longer matches the record) is not the backup the panel made;
// refuse rather than swap in an unknown binary.
func TestPlanAgentRollbackBackupReplacedAfterPairing(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeAgentBinary(t, dir, "flux_agent", "2.0.0", "current-B")
	backup := writeFakeAgentBinary(t, dir, "flux_agent.old", "1.0.0", "backup-A")
	pairMeta(t, backup, bin, "2.0.0")
	writeFakeAgentBinary(t, dir, "flux_agent.old", "1.0.0", "tampered-A")

	_, err := planAgentRollback(bin, backup)
	if err == nil || !strings.Contains(err.Error(), "与配对记录不一致") {
		t.Fatalf("replaced backup must refuse, got: %v", err)
	}
}

// A backup that cannot prove its own version (not an executable
// agent binary) must never be swapped in: the rollback script has
// no second fallback if the restored binary cannot start.
func TestPlanAgentRollbackUnverifiableBackupRefuses(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeAgentBinary(t, dir, "flux_agent", "2.0.0", "current-B")
	backup := filepath.Join(dir, "flux_agent.old")
	if err := os.WriteFile(backup, []byte("#!/bin/sh\necho hello\n"), 0755); err != nil {
		t.Fatal(err)
	}
	_, err := planAgentRollback(bin, backup)
	if err == nil || !strings.Contains(err.Error(), "回退备份校验失败") {
		t.Fatalf("unverifiable backup must refuse, got: %v", err)
	}
}

func TestAgentBackupMetaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	backup := filepath.Join(dir, "flux_agent.old")
	if got := readAgentBackupMeta(backup); got != nil {
		t.Fatalf("missing meta must read as nil, got %+v", got)
	}
	meta := &agentBackupMeta{BackupSHA256: "aa", InstalledVersion: "2.0.0", InstalledSHA256: "bb"}
	if err := writeAgentBackupMeta(backup, meta); err != nil {
		t.Fatal(err)
	}
	got := readAgentBackupMeta(backup)
	if got == nil || *got != *meta {
		t.Fatalf("round trip = %+v, want %+v", got, meta)
	}
	if err := os.WriteFile(agentBackupMetaPath(backup), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := readAgentBackupMeta(backup); got != nil {
		t.Fatalf("malformed meta must read as nil (legacy caliber), got %+v", got)
	}
}

func TestProbeAgentBinaryVersion(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeAgentBinary(t, dir, "fake_agent", "1.9.4", "probe")
	v, err := probeAgentBinaryVersion(bin)
	if err != nil || v != "1.9.4" {
		t.Fatalf("probe = %q, %v; want 1.9.4, nil", v, err)
	}
	bad := filepath.Join(dir, "bad_agent")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho hello\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := probeAgentBinaryVersion(bad); err == nil {
		t.Fatal("non-banner -V output must fail the probe")
	}
}
