package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// C-03: the metering companions run as a second sing-box instance
// ("singbox-companion"). A companion Apply must not restart the main
// "singbox" instance — and a main Apply must not restart the companion.

const fakeSingboxKernel = `#!/bin/sh
if [ "$1" = "version" ]; then echo "sing-box version 1.14.4 (fake)"; exit 0; fi
echo "starting with $3"
exec sleep 300
`

func serveSingboxKernel(t *testing.T, payload []byte) (*httptest.Server, *int) {
	t.Helper()
	sum := sha256.Sum256(payload)
	downloads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sing-box-linux-test.zip":
			downloads++
			_, _ = w.Write(payload)
		case "/sing-box-linux-test.zip.dgst":
			fmt.Fprintf(w, "MD5= 00000000000000000000000000000000\nSHA2-256= %s\nSHA2-512= %s\n", hex.EncodeToString(sum[:]), strings.Repeat("ab", 64))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &downloads
}

func c03ApplyRequest(srv *httptest.Server, kernelType, config string, enabled bool, port int) ApplyRequest {
	return ApplyRequest{
		Type:    kernelType,
		Version: "1.14.4",
		Download: &Download{
			URL:         srv.URL + "/sing-box-linux-{ARCH}.zip",
			ChecksumURL: srv.URL + "/sing-box-linux-{ARCH}.zip.dgst",
			Archive:     "zip",
			BinName:     "sing-box",
			ArchMap:     map[string]string{runtime.GOARCH: "test"},
		},
		Files:   []File{{Name: "config.json", Content: config}},
		Args:    []string{"run", "-c", "{RUN_DIR}/config.json"},
		Hash:    hashString(config),
		Enabled: enabled,
		Ports:   []Port{{InboundID: 9, Port: port, Network: "tcp"}},
	}
}

func c03InstancePID(t *testing.T, m *Manager, kernelType string) (pid, restarts int) {
	t.Helper()
	m.mu.Lock()
	inst := m.instances[kernelType]
	m.mu.Unlock()
	if inst == nil {
		t.Fatalf("no instance for %q", kernelType)
	}
	if len(inst.procs) != 1 {
		t.Fatalf("instance %q has %d processes, want 1", kernelType, len(inst.procs))
	}
	running, pid, restarts, _, _ := inst.procs[0].snapshot()
	if !running {
		t.Fatalf("instance %q not running", kernelType)
	}
	return pid, restarts
}

func TestCompanionApplyDoesNotRestartMainInstance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stubNft(t)
	srv, downloads := serveSingboxKernel(t, zipWith(t, map[string]string{"sing-box": fakeSingboxKernel}))
	base := t.TempDir()
	m := NewManager(base)
	defer m.Shutdown()

	st, err := m.Apply(c03ApplyRequest(srv, "singbox", `{"v":1}`, true, 24599))
	if err != nil || !st.Running {
		t.Fatalf("apply main v1: %+v %v", st, err)
	}
	mainPID, mainRestarts := c03InstancePID(t, m, "singbox")

	st, err = m.Apply(c03ApplyRequest(srv, "singbox-companion", `{"c":1}`, true, 24600))
	if err != nil || !st.Running {
		t.Fatalf("apply companion v1: %+v %v", st, err)
	}
	compPID, _ := c03InstancePID(t, m, "singbox-companion")
	if *downloads != 2 {
		t.Fatalf("downloads = %d, want 2 (each instance owns its binDir)", *downloads)
	}

	// Companion config change: companion restarts, main must not.
	st, err = m.Apply(c03ApplyRequest(srv, "singbox-companion", `{"c":2}`, true, 24600))
	if err != nil || !st.Running {
		t.Fatalf("apply companion v2: %+v %v", st, err)
	}
	if pid, restarts := c03InstancePID(t, m, "singbox"); pid != mainPID || restarts != mainRestarts {
		t.Fatalf("companion Apply restarted the main instance: pid %d->%d restarts %d->%d", mainPID, pid, mainRestarts, restarts)
	}
	if pid, _ := c03InstancePID(t, m, "singbox-companion"); pid == compPID {
		t.Fatalf("companion Apply did not restart the companion instance (pid still %d)", pid)
	}
	compPID, _ = c03InstancePID(t, m, "singbox-companion")
	cfg, _ := os.ReadFile(filepath.Join(base, "singbox", "run", "config.json"))
	if string(cfg) != `{"v":1}` {
		t.Fatalf("main config changed by companion Apply: %q", cfg)
	}

	// Main config change: main restarts, companion must not.
	st, err = m.Apply(c03ApplyRequest(srv, "singbox", `{"v":2}`, true, 24599))
	if err != nil || !st.Running {
		t.Fatalf("apply main v2: %+v %v", st, err)
	}
	if pid, _ := c03InstancePID(t, m, "singbox-companion"); pid != compPID {
		t.Fatalf("main Apply restarted the companion instance: pid %d->%d", compPID, pid)
	}
	cfg, _ = os.ReadFile(filepath.Join(base, "singbox-companion", "run", "config.json"))
	if string(cfg) != `{"c":2}` {
		t.Fatalf("companion config changed by main Apply: %q", cfg)
	}
}

func TestCompanionTypeValidation(t *testing.T) {
	req := ApplyRequest{Type: "singbox-companion", Version: "1.14.4", Download: &Download{BinName: "sing-box", Archive: "zip"}}
	if err := req.validate(); err != nil {
		t.Fatalf("companion type must validate: %v", err)
	}
	bad := ApplyRequest{Type: "not-a-kernel", Version: "1.0"}
	if err := bad.validate(); err == nil || !strings.Contains(err.Error(), "不支持的内核类型") {
		t.Fatalf("unknown type err = %v, want unsupported-type error", err)
	}
}
