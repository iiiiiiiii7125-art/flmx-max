package socket

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-gost/x/internal/util/download"
)

// FIX-R2-9 面2（agent 自升级第二锚按获胜线路分层，与脚本侧
// R2-01-01 同律）：corroborateUpgradeChecksum 此前对任何获胜线路
// 都无条件点名降级。本组钉分层三态——第三方公共镜像获胜＋复核
// 不可用必须 fail-closed 中止（只剩镜像自给自洽对、等于自证）；
// 镜像获胜＋异路同值复核通过则放行；直连获胜＋复核不可用维持点
// 名降级。另钉两个第一方/边界形态：面板源线路（Prefix 为面板
// origin+panelMirrorPath）属第一方、维持降级；候选集为空（单线
// 路环境）时直连获胜的降级也必须经 note 点名、不得静默放行。

func fixR29HashServer(t *testing.T, hash string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(hash + "  gost-amd64\n"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCorroborateUpgradeChecksumWinnerLayersFixR29(t *testing.T) {
	sum := sha256.Sum256([]byte("new-agent-binary"))
	want := hex.EncodeToString(sum[:])
	otherSum := sha256.Sum256([]byte("tampered-agent-binary"))
	other := hex.EncodeToString(otherSum[:])

	setMirrors := func(t *testing.T, mirrors []string) {
		t.Helper()
		old := download.MirrorPrefixes
		download.MirrorPrefixes = mirrors
		t.Cleanup(func() { download.MirrorPrefixes = old })
	}

	t.Run("proxy-winner-no-corroboration-aborts", func(t *testing.T) {
		direct := fixR29HashServer(t, want, http.StatusInternalServerError)
		proxy := fixR29HashServer(t, want, http.StatusOK)
		setMirrors(t, []string{proxy.URL})
		var notes []string
		note := func(msg string) { notes = append(notes, msg) }
		err := corroborateUpgradeChecksum(context.Background(), newUpgradeDownloadClient(""),
			download.Route{Prefix: proxy.URL}, direct.URL+"/gost-amd64.sha256", want, note)
		if err == nil {
			t.Fatalf("代理获胜且复核不可用必须 fail-closed 中止, got nil (notes=%v)", notes)
		}
		if !strings.Contains(err.Error(), "获胜线路为代理") || !strings.Contains(err.Error(), "已中止升级") {
			t.Fatalf("中止错误须点名获胜线路为代理与原因, got %v", err)
		}
		for _, n := range notes {
			if strings.Contains(n, "已降级") {
				t.Fatalf("fail-closed 时不得再发降级点名, notes=%v", notes)
			}
		}
	})

	t.Run("proxy-winner-corroboration-agrees", func(t *testing.T) {
		direct := fixR29HashServer(t, want, http.StatusOK)
		proxy := fixR29HashServer(t, want, http.StatusOK)
		setMirrors(t, []string{proxy.URL})
		err := corroborateUpgradeChecksum(context.Background(), newUpgradeDownloadClient(""),
			download.Route{Prefix: proxy.URL}, direct.URL+"/gost-amd64.sha256", want, nil)
		if err != nil {
			t.Fatalf("代理获胜但直连异路同值应复核通过, got %v", err)
		}
	})

	t.Run("proxy-winner-corroboration-mismatch-aborts", func(t *testing.T) {
		direct := fixR29HashServer(t, other, http.StatusOK)
		proxy := fixR29HashServer(t, want, http.StatusOK)
		setMirrors(t, []string{proxy.URL})
		err := corroborateUpgradeChecksum(context.Background(), newUpgradeDownloadClient(""),
			download.Route{Prefix: proxy.URL}, direct.URL+"/gost-amd64.sha256", want, nil)
		if err == nil || !strings.Contains(err.Error(), "不一致") {
			t.Fatalf("异路异值必须硬中止并点名不一致, got %v", err)
		}
	})

	t.Run("direct-winner-no-corroboration-degrades", func(t *testing.T) {
		direct := fixR29HashServer(t, want, http.StatusOK)
		deadProxy := fixR29HashServer(t, want, http.StatusInternalServerError)
		setMirrors(t, []string{deadProxy.URL})
		var notes []string
		note := func(msg string) { notes = append(notes, msg) }
		err := corroborateUpgradeChecksum(context.Background(), newUpgradeDownloadClient(""),
			download.Route{}, direct.URL+"/gost-amd64.sha256", want, note)
		if err != nil {
			t.Fatalf("直连获胜无异路应答应降级放行, got %v", err)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "跨线路锚复核已降级") {
			t.Fatalf("降级必须经 note 点名, notes=%v", notes)
		}
	})

	t.Run("panel-winner-no-corroboration-degrades", func(t *testing.T) {
		direct := fixR29HashServer(t, want, http.StatusInternalServerError)
		deadProxy := fixR29HashServer(t, want, http.StatusInternalServerError)
		setMirrors(t, []string{deadProxy.URL})
		var notes []string
		note := func(msg string) { notes = append(notes, msg) }
		winner := download.Route{Prefix: "https://panel.example.com" + panelMirrorPath}
		err := corroborateUpgradeChecksum(context.Background(), newUpgradeDownloadClient(""),
			winner, direct.URL+"/gost-amd64.sha256", want, note)
		if err != nil {
			t.Fatalf("面板源（第一方）获胜无异路应答应降级放行, got %v", err)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "跨线路锚复核已降级") {
			t.Fatalf("降级必须经 note 点名, notes=%v", notes)
		}
	})

	t.Run("single-route-direct-winner-notes-degradation", func(t *testing.T) {
		direct := fixR29HashServer(t, want, http.StatusOK)
		setMirrors(t, nil)
		var notes []string
		note := func(msg string) { notes = append(notes, msg) }
		err := corroborateUpgradeChecksum(context.Background(), newUpgradeDownloadClient(""),
			download.Route{}, direct.URL+"/gost-amd64.sha256", want, note)
		if err != nil {
			t.Fatalf("单线路环境直连获胜应降级放行, got %v", err)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "跨线路锚复核已降级") {
			t.Fatalf("候选集为空时的降级也必须点名、不得静默放行, notes=%v", notes)
		}
	})
}
