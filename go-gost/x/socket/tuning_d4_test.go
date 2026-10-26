package socket

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// D4 (P1) regression tests (PORT-3343-BATCH2-D4-FIX.md).
//
//   - D4: persistTuneShape installs the engine test shaper (live HTB
//     1: + class 1:10 + fq 10:) and then runs the persisted shape.sh,
//     whose first line was `tc qdisc replace ... root handle 1: htb`.
//     The kernel refuses replace on a live HTB root that already has
//     children (rc=2, "Change operation not supported by specified
//     qdisc"), so every deep run that found a knee failed at the
//     persist step and the shaping could never be persisted or
//     auto-started. The fix makes the script delete the root first
//     (tolerating a missing/default root) and add it back — the same
//     del+add sequence apply_test_shaper already uses.
//   - The fake tc below models the kernel behaviour the re-verify
//     run measured on the real machine (verify-logs-b2r/19-21):
//     replace on a live HTB root with children fails rc=2; del on
//     an HTB root or an mq root with handle 1 succeeds and the
//     kernel reinstalls the default mq 0: root; plain del on a
//     kernel-default mq root with handle 0 fails (qdisc_remove_root
//     case b in tcpfit.sh:1972); add/replace overwrite a handle-0
//     root (qdisc_remove_root case c comment). A fake that answered
//     rc=0 to every replace would not reproduce D4 at all.

// fakeKernelHTBTC models the kernel side of `tc qdisc/class` for the
// D4 tests. State file holds root/handle/class/leaf, one KEY=VALUE
// per line; FAKE_TC_STATE points at it.
const fakeKernelHTBTC = `#!/bin/sh
: "${FAKE_TC_STATE:?}"
state="$FAKE_TC_STATE"
root=mq; handle=0; class=""; leaf=0
[ -f "$state" ] && . "$state"
save() { printf 'root=%s\nhandle=%s\nclass=%s\nleaf=%s\n' "$root" "$handle" "$class" "$leaf" > "$state"; }
[ -f "$state" ] || save
kind="$1"; verb="$2"; shift 2
parent=""; newhandle=""; rate=""
prev=""
for a in "$@"; do
  [ "$prev" = "parent" ] && parent="$a"
  [ "$prev" = "handle" ] && newhandle="$a"
  [ "$prev" = "rate" ] && rate="${a%mbit}"
  prev="$a"
done
newmajor="${newhandle%%:*}"
case "$*" in *" 1: htb"*) qkind=htb;; *" 1: mq"*) qkind=mq;; *) qkind="";; esac
fail() { echo "Error: $1" >&2; exit 2; }
case "$kind $verb" in
  "qdisc show")
    printf 'qdisc %s %s: root refcnt 2\n' "$root" "$handle"
    [ "$leaf" = "1" ] && printf 'qdisc fq 10: parent 1:10 limit 40960p flow_limit 8192p\n'
    exit 0 ;;
  "class show")
    [ -n "$class" ] && printf 'class htb 1:10 parent 1: root prio 0 rate %sMbit ceil %sMbit burst 2417500b cburst 2417500b\n' "$class" "$class"
    exit 0 ;;
  "qdisc del")
    if [ "$root" = "htb" ]; then root=mq; handle=0; class=""; leaf=0; save; exit 0; fi
    if [ "$root" = "mq" ] && [ "$handle" = "1" ]; then root=mq; handle=0; save; exit 0; fi
    fail "Cannot delete qdisc." ;;
  "qdisc add")
    if [ -n "$parent" ]; then
      if [ -n "$class" ] && [ "$leaf" = "0" ]; then leaf=1; save; exit 0; fi
      fail "Cannot find specified qdisc."
    fi
    if [ "$root" = "none" ]; then root="$qkind"; handle="$newmajor"; save; exit 0; fi
    if [ "$root" = "mq" ] && [ "$handle" = "0" ]; then root="$qkind"; handle="$newmajor"; save; exit 0; fi
    fail "RTNETLINK answers: File exists" ;;
  "qdisc replace")
    if [ -n "$parent" ]; then
      if [ -n "$class" ]; then leaf=1; save; exit 0; fi
      fail "Cannot find specified qdisc."
    fi
    if [ "$root" = "htb" ] && [ "$qkind" = "htb" ] && [ -n "$class" ]; then
      fail "Change operation not supported by specified qdisc."
    fi
    root="$qkind"; handle="$newmajor"
    if [ "$qkind" != "htb" ]; then class=""; leaf=0; fi
    save; exit 0 ;;
  "class add")
    if [ "$root" = "htb" ] && [ "$handle" = "1" ] && [ -z "$class" ]; then class="$rate"; save; exit 0; fi
    fail "Cannot find specified qdisc." ;;
  "class replace")
    if [ "$root" = "htb" ] && [ "$handle" = "1" ]; then class="$rate"; save; exit 0; fi
    fail "Cannot find specified qdisc." ;;
  *)
    echo "fake tc: unknown $kind $verb" >&2; exit 1 ;;
esac
`

// writeFakeKernelHTBTC installs the kernel-modeling fake tc into
// dir/bin and returns the bin dir and the state file path. The caller
// writes the initial state (root/handle/class/leaf lines) before
// running anything against it.
func writeFakeKernelHTBTC(t *testing.T, dir string) (bin, state string) {
	t.Helper()
	bin = filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tc"), []byte(fakeKernelHTBTC), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, filepath.Join(dir, "tc-state")
}

func writeTCState(t *testing.T, state, root, handle, class, leaf string) {
	t.Helper()
	body := "root=" + root + "\nhandle=" + handle + "\nclass=" + class + "\nleaf=" + leaf + "\n"
	if err := os.WriteFile(state, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTCState(t *testing.T, state string) string {
	t.Helper()
	b, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func runShapeScript(t *testing.T, scriptPath, bin, state string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_TC_STATE="+state, "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestTuneShapeScriptReplacesLiveHTBRoot(t *testing.T) {
	// D4 core: the exact state the engine shape leaves behind —
	// live HTB 1: root with class 1:10 and fq leaf 10:.
	dir := t.TempDir()
	bin, state := writeFakeKernelHTBTC(t, dir)
	writeTCState(t, state, "htb", "1", "4835", "1")
	script := tuneShapeScript("eth0", 4835)
	scriptPath := filepath.Join(dir, "shape.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-n", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("shape.sh fails bash -n: %v\n%s\n%s", err, out, script)
	}
	out, err := runShapeScript(t, scriptPath, bin, state)
	// D4: before the fix the first line (`tc qdisc replace ... root
	// handle 1: htb`) fails rc=2 on the live HTB root and `set -e`
	// aborts the script — persist always failed.
	if err != nil {
		t.Fatalf("D4: shape.sh on live HTB root failed: %v\n%s\n%s", err, out, script)
	}
	got := readTCState(t, state)
	if !strings.Contains(got, "root=htb") || !strings.Contains(got, "class=4835") || !strings.Contains(got, "leaf=1") {
		t.Fatalf("shape.sh did not leave HTB 4835 in place: %q\n%s", got, script)
	}
}

func TestTuneShapeScriptOnMQRoot(t *testing.T) {
	// Boot-path guard: the persisted script also runs from the
	// systemd unit at boot, on a plain kernel-default mq root. It
	// must keep working there. This passes both before and after
	// the D4 fix (replace already worked on mq); it guards the fix
	// against breaking the boot path — it is not the red test.
	dir := t.TempDir()
	bin, state := writeFakeKernelHTBTC(t, dir)
	writeTCState(t, state, "mq", "0", "", "0")
	script := tuneShapeScript("eth0", 4835)
	scriptPath := filepath.Join(dir, "shape.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := runShapeScript(t, scriptPath, bin, state)
	if err != nil {
		t.Fatalf("shape.sh on mq root failed: %v\n%s\n%s", err, out, script)
	}
	got := readTCState(t, state)
	if !strings.Contains(got, "root=htb") || !strings.Contains(got, "class=4835") || !strings.Contains(got, "leaf=1") {
		t.Fatalf("shape.sh did not leave HTB 4835 in place: %q\n%s", got, script)
	}
}

func TestTuneShapeScriptAfterEngineShape(t *testing.T) {
	// The exact product sequence: the real bridge `shape` action
	// installs the test shaper, then the persisted shape.sh runs on
	// top of it — the sequence persistTuneShape performs.
	dir := t.TempDir()
	copyRealBridge(t, dir)
	bin := fakeBridgeBin(t, dir)
	// Replace the D1 static tc with the kernel-modeling one.
	if err := os.WriteFile(filepath.Join(bin, "tc"), []byte(fakeKernelHTBTC), 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "tc-state")
	writeTCState(t, state, "mq", "0", "", "0")
	env := append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_TC_STATE="+state, "LC_ALL=C")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bridge := exec.CommandContext(ctx, "bash", filepath.Join(dir, "bridge.sh"), dir, "shape", "eth0", "127.0.0.1", "5201", "-4", "4835")
	bridge.Env = env
	out, err := bridge.CombinedOutput()
	if err != nil {
		t.Fatalf("engine shape failed: %v\n%s", err, out)
	}
	mid := readTCState(t, state)
	if !strings.Contains(mid, "root=htb") || !strings.Contains(mid, "class=4835") || !strings.Contains(mid, "leaf=1") {
		t.Fatalf("engine shape did not install the live test shaper: %q\n%s", mid, out)
	}
	script := tuneShapeScript("eth0", 4835)
	scriptPath := filepath.Join(dir, "shape.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	out2, err := runShapeScript(t, scriptPath, bin, state)
	// D4: before the fix shape.sh dies on the live HTB root the
	// engine just installed — persistTuneShape returns the error
	// and the deep task fails.
	if err != nil {
		t.Fatalf("D4: shape.sh after engine shape failed: %v\n%s\n%s", err, out2, script)
	}
	got := readTCState(t, state)
	if !strings.Contains(got, "root=htb") || !strings.Contains(got, "class=4835") || !strings.Contains(got, "leaf=1") {
		t.Fatalf("shape.sh did not leave HTB 4835 in place: %q\n%s", got, script)
	}
}
