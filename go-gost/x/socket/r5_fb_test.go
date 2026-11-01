package socket

// Round-5 FB cluster regression tests (L2-03 / L2-04 / L2-07):
// self-upgrade checksum in-leg content validation, binary in-leg
// hash verification with route failover, and the F2 dial judgment
// of the upgrade download client.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-gost/x/internal/util/download"
)

// --- L2-04: the checksum leg must judge content inside the leg,
// like the probe does ---

func TestUpgradeChecksumAttemptRejectsNonHexContent(t *testing.T) {
	// A 200 answer whose first field is not a SHA-256: the probe
	// rejects this shape, so the leg must too — it used to "win"
	// the leg and fail the whole upgrade after the fact.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("landing-page-junk  gost-amd64\n"))
	}))
	defer bad.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := fetchUpgradeChecksumAttempt(ctx, newUpgradeDownloadClient(""), bad.URL+"/gost-amd64.sha256"); err == nil {
		t.Fatal("非 SHA256 内容的校验腿必须失败")
	} else if !strings.Contains(err.Error(), "不是合法 SHA256") {
		t.Fatalf("错误应点名内容非法, got: %v", err)
	}

	payload := []byte("agent-binary")
	sum := sha256.Sum256(payload)
	want := hex.EncodeToString(sum[:])
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(want + "  gost-amd64\n"))
	}))
	defer good.Close()
	got, err := fetchUpgradeChecksumAttempt(ctx, newUpgradeDownloadClient(""), good.URL+"/gost-amd64.sha256")
	if err != nil {
		t.Fatalf("合法校验文件应成功, got: %v", err)
	}
	if got != want {
		t.Fatalf("hash = %q, want %q", got, want)
	}
}

// --- L2-03: a complete-but-wrong body fails its leg and the next
// route's body wins ---

func TestUpgradeBinaryCheckedWrongBodyFallsOver(t *testing.T) {
	good := []byte("the-real-new-agent-binary")
	wrong := []byte("a-complete-but-wrong-body!")
	sum := sha256.Sum256(good)
	want := hex.EncodeToString(sum[:])

	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(wrong)))
		_, _ = w.Write(wrong)
	}))
	defer badSrv.Close()
	goodSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(good)))
		_, _ = w.Write(good)
	}))
	defer goodSrv.Close()

	plan := download.NewStaticPlan([]download.Route{{Prefix: badSrv.URL}, {Prefix: goodSrv.URL}})
	tmp := filepath.Join(t.TempDir(), "flux_agent.upgrade-1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, hash, err := downloadUpgradeBinaryChecked(ctx, newUpgradeDownloadClient(""), plan, "https://example.invalid/gost-amd64", tmp, want, nil)
	if err != nil {
		t.Fatalf("错误本体线路必须被腿内校验废掉并换线成功, got: %v", err)
	}
	if n != int64(len(good)) || hash != want {
		t.Fatalf("结果错误: n=%d hash=%s want %s", n, hash, want)
	}
	content, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(good) {
		t.Fatalf("落盘内容 = %q, want 正确线路的本体", content)
	}
}

// --- L2-07: the upgrade client's dial re-judges at the hop's
// layer (hard-deny at entry, full judgment for cross-host hops) ---

func TestUpgradeDialJudgmentLayers(t *testing.T) {
	oldLookup := upgradeLookupIP
	upgradeLookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		switch host {
		case "meta.test":
			return []net.IP{net.ParseIP("169.254.169.254")}, nil
		case "priv.test":
			return []net.IP{net.ParseIP("192.168.0.9")}, nil
		}
		return oldLookup(ctx, host)
	}
	defer func() { upgradeLookupIP = oldLookup }()

	// Hard-deny applies at every layer, entry included.
	if _, err := upgradeDialContext(context.Background(), "tcp", "meta.test:80"); err == nil ||
		!strings.Contains(err.Error(), "元数据") {
		t.Fatalf("元数据地址必须在拨号期被硬拒, got: %v", err)
	}
	// A cross-host hop's dial (marker set) applies the FULL
	// judgment: a private answer is refused before any dial.
	fullCtx := context.WithValue(context.Background(), upgradeDialFullContextKey{}, true)
	if _, err := upgradeDialContext(fullCtx, "tcp", "priv.test:80"); err == nil ||
		!strings.Contains(err.Error(), "非公网") {
		t.Fatalf("跨 host 跳的拨号必须全量复判, got: %v", err)
	}
	// The entry layer (no marker) judges hard-deny only: a private
	// panel address stays dialable — the attempt must reach the
	// dial itself (and fail there), not the judgment.
	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := upgradeDialContext(dialCtx, "tcp", "priv.test:80"); err != nil &&
		strings.Contains(err.Error(), "已拒绝") {
		t.Fatalf("入口层拨号不得以判定拒绝私有地址（面板源形态）, got: %v", err)
	}
}
