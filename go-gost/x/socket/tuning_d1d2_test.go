package socket

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// D1/D2/D3 regression tests (PORT-3343-BATCH2-D1D2-FIX.md).
//
//   - D1 (P1): the vendored tcpfit engine calls detect_iface() from
//     functions that declare `local iface` without assigning it
//     (cmd_sweep tcpfit.sh:2736, take_snapshot :1082, ...). Under the
//     engine's `set -u`, bridge.sh's detect_iface() — which printed the
//     bridge global $iface — died with `iface: unbound variable`, so
//     every deep scan failed before its first probe. The fix renames
//     the bridge variable to BRIDGE_IFACE, which no tcpfit.sh local
//     can shadow. TestTuneBridgeDetectIfaceSurvivesLocalIface runs
//     the real bridge sweep and fails while the word `iface` is still
//     what detect_iface() reads.
//   - D2 (P1): tuneQueueReplay only repaired the mq root handle when
//     the kernel reported no handle at all (`-z $mqmajor`). A kernel
//     that recreates the deleted mq root as `mq 0:` reports
//     mqmajor=0, the repair is skipped, and leaves bind to
//     `parent 0:N`, which real tc rejects ("Failed to find specified
//     qdisc."). TestTuneQueueReplayMQHandleZero replays the
//     generated script against a fake tc that models that kernel
//     behaviour; TestTuneRecoverTransactionAfterQueueReplayFix
//     proves the stuck-transaction wedge clears once the replay
//     succeeds.
//   - D3 (P2): an auto job reported `succeeded` / `applied: true`
//     while sysctl writes failed (read-only /proc/sys in a
//     container). The warnings were only in tuning-result.json.
//     The fix keeps `applied` semantics and only appends the
//     failed-setting count to the finished job message.

// fakeBridgeBin writes the fake system tools the real bridge needs
// for a sweep, into dir/bin. The fake tc is strict: any empty
// argument fails, like real tc failing on an empty device name —
// that is what a D1-broken sweep feeds it (iface="").
func fakeBridgeBin(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	tools := map[string]string{
		"id":      "#!/bin/sh\nprintf '0\\n'\n",
		"timeout": "#!/bin/sh\n[ \"$1\" != --foreground ] || shift\nshift\nexec \"$@\"\n",
		"pkill":   "#!/bin/sh\nexit 1\n",
		"iperf3":  "#!/bin/sh\nprintf '[SUM] 0.00-10.00 sec 1.10 GBytes 1000 Mbits/sec 0 sender\\n[SUM] 0.00-10.00 sec 1.10 GBytes 950 Mbits/sec receiver\\n'\n",
		"tc":      "#!/bin/sh\nfor a in \"$@\"; do [ -n \"$a\" ] || { echo \"Cannot find device\" >&2; exit 1; }; done\nprintf 'qdisc fq 0: root refcnt 2 limit 10000p flow_limit 100p\\n'\n",
		"ip":      "#!/bin/sh\nprintf 'default via 192.0.2.1 dev eth0\\n'\n",
	}
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

func copyRealBridge(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"tcpfit.sh", "bridge.sh"} {
		b, err := tuneEngine.ReadFile("tcpfit/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), b, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTuneBridgeDetectIfaceSurvivesLocalIface(t *testing.T) {
	dir := t.TempDir()
	copyRealBridge(t, dir)
	bin := fakeBridgeBin(t, dir)
	// The bridge sweep traps EXIT to replay the captured queues, as
	// in TestTuneTCPFitBridgeMeasurementAndCleanScan.
	if err := os.WriteFile(filepath.Join(dir, "current-qdisc.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a := []string{filepath.Join(dir, "bridge.sh"), dir, "sweep", "eth0", "127.0.0.1", "5201", "-4", "1000", "50"}
	cmd := exec.CommandContext(ctx, "bash", a...)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	// D1: before the fix the output contains
	// "bridge.sh: line 11: iface: unbound variable" — detect_iface()
	// read the bridge global $iface while cmd_sweep's `local iface`
	// shadowed it unassigned under `set -u`.
	if strings.Contains(string(out), "unbound variable") {
		t.Fatalf("D1: detect_iface died on the shadowed local iface:\n%s", out)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || !strings.Contains(string(out), "FLYX_SWEEP_NO_KNEE=1") {
		t.Fatalf("clean scan: %v\n%s", err, out)
	}
}

// fakeKernelMQTC models the kernel side of a queue replay: deleting
// the root recreates an mq root with handle 0, a parent whose major
// is 0 is rejected exactly like real tc ("Failed to find specified
// qdisc."), and `replace root handle 1: mq` moves the root to 1.
const fakeKernelMQTC = `#!/bin/sh
state="$FAKE_TC_STATE"
log="$FAKE_TC_LOG"
[ -f "$state" ] || printf '0\n' > "$state"
handle=$(cat "$state")
shift # qdisc
case "$1" in
  show) printf 'qdisc mq %s: root\n' "$handle" ;;
  del) printf '0\n' > "$state" ;;
  replace)
    shift
    parent=""; newhandle=""
    prev=""
    for a in "$@"; do
      if [ "$prev" = "parent" ]; then parent="$a"; fi
      if [ "$prev" = "handle" ]; then newhandle="$a"; fi
      prev="$a"
    done
    if [ -n "$newhandle" ]; then
      printf '%s\n' "${newhandle%%:*}" > "$state"
      exit 0
    fi
    if [ -n "$parent" ]; then
      major="${parent%%:*}"
      if [ "$major" = "0" ] || [ "$major" != "$handle" ]; then
        echo "Error: Failed to find specified qdisc." >&2
        exit 2
      fi
      printf '%s\n' "$parent" >> "$log"
      exit 0
    fi
    exit 0 ;;
esac
`

func TestTuneQueueReplayMQHandleZero(t *testing.T) {
	// The exact capture from the B2 verify run (ens5, mq 0: root).
	queue := "qdisc mq 0: root\n" +
		"qdisc fq_codel 0: parent :1 limit 10240p flows 1024 quantum 1514 target 5ms interval 100ms memory_limit 32Mb ecn\n" +
		"qdisc fq_codel 0: parent :2 limit 10240p flows 1024 quantum 1514 target 5ms interval 100ms memory_limit 32Mb ecn"
	script, err := tuneQueueReplay("eth0", queue)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "original-qdisc.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-n", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("replay script fails bash -n: %v\n%s\n%s", err, out, script)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tc"), []byte(fakeKernelMQTC), 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "tc-state")
	log := filepath.Join(dir, "tc-log")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_TC_STATE="+state, "FAKE_TC_LOG="+log, "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	// D2: before the fix the script skips the handle repair for
	// mqmajor=0 and the first leaf fails with exit 2.
	if err != nil {
		t.Fatalf("D2: replay on mq 0: root failed: %v\n%s\n%s", err, out, script)
	}
	bound, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bound), "1:1") || !strings.Contains(string(bound), "1:2") {
		t.Fatalf("leaves did not bind to the repaired handle 1: %q\n%s", bound, script)
	}
}

func TestTuneRecoverTransactionAfterQueueReplayFix(t *testing.T) {
	// The D2 wedge: a failed deep scan leaves tuning-transaction.json
	// behind; startTuneJob refuses new work until recoverTuneTransaction
	// succeeds, and that recovery itself replayed the same broken
	// script, so the wedge never cleared on its own.
	fakeProcSys(t, "reno cubic bbr")
	oldUnit := tuneShapeUnitPath
	unitDir := t.TempDir()
	tuneShapeUnitPath = func() string { return filepath.Join(unitDir, "flmx-tuning-qdisc.service") }
	t.Cleanup(func() { tuneShapeUnitPath = oldUnit })
	if err := os.MkdirAll(tuneEngineDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	queue := "qdisc mq 0: root\n" +
		"qdisc fq_codel 0: parent :1 limit 10240p flows 1024 quantum 1514 target 5ms interval 100ms memory_limit 32Mb ecn\n" +
		"qdisc fq_codel 0: parent :2 limit 10240p flows 1024 quantum 1514 target 5ms interval 100ms memory_limit 32Mb ecn"
	script, err := tuneQueueReplay("eth0", queue)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tuneEngineDir(), "current-qdisc.sh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	files, err := captureTuneShapeFiles()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveTuneJSON(tuneStatePath("tuning-transaction.json"), files); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tc"), []byte(fakeKernelMQTC), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_TC_STATE", filepath.Join(dir, "tc-state"))
	t.Setenv("FAKE_TC_LOG", filepath.Join(dir, "tc-log"))
	base := tuneCommand
	tuneCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "systemctl" {
			return "", nil
		}
		return base(ctx, name, args...)
	}
	t.Cleanup(func() { tuneCommand = base })
	if err := recoverTuneTransaction(); err != nil {
		t.Fatalf("D2 wedge: recovery still fails: %v", err)
	}
	if _, err := os.Stat(tuneStatePath("tuning-transaction.json")); !os.IsNotExist(err) {
		t.Fatal("D2 wedge: transaction file not cleaned after recovery")
	}
}

func TestTuneAutoJobMessageCountsFailedSettings(t *testing.T) {
	// D3: an auto job in a restricted container (read-only /proc/sys)
	// finished `succeeded` / applied:true with 21 warnings and a bare
	// "调优完成" message. applied stays what it was (the job did run
	// and apply what it could); only the finished message now carries
	// the failed-setting count, so the success is not overstated.
	resetTuneTasks(t)
	fakeTuneAutomation(t)
	oldWrite := tuneWrite
	tuneWrite = func(key, value string) error {
		if key == "net.core.somaxconn" {
			return fmt.Errorf("open /proc/sys/net/core/somaxconn: read-only file system")
		}
		return oldWrite(key, value)
	}
	t.Cleanup(func() { tuneWrite = oldWrite })
	if _, err := startTuneJob("auto", 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if j := readTuneJob(); j != nil && j.State == "succeeded" {
			if !strings.Contains(j.Message, "1 项参数未生效") {
				t.Fatalf("D3: succeeded message hides the failed setting: %q", j.Message)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("auto job never succeeded: %+v", readTuneJob())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
