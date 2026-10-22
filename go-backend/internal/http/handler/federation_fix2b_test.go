package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// FIX2-B（V5-3）：联邦版本可见性与存量期望状态启动复检。
//   - Connect 回执携带提供方 panelVersion；消费方版本缺失（旧版）或
//     低于凭据感知版本（1.3.4，D3-04 relay 鉴权上线版本）时，回执带
//     versionWarning 且提供方记日志，不再静默断裂。
//   - 启动时对存量 peer_share_resource 期望状态补跑类型白名单复检，
//     危险存量置 paused 并告警。

func TestFix2BFederationConnectVersionWarning(t *testing.T) {
	h, _, share := fix132ShareFixture(t, 31000, 31099)

	call := func(consumerVersion string, setHeader bool) (string, string) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/federation/connect", nil)
		req.Header.Set("Authorization", "Bearer "+share.Token)
		if setHeader {
			req.Header.Set("X-Panel-Version", consumerVersion)
		}
		rec := httptest.NewRecorder()
		h.federationConnect(rec, req)
		var env struct {
			Code int `json:"code"`
			Data struct {
				PanelVersion   string `json:"panelVersion"`
				VersionWarning string `json:"versionWarning"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode connect response: %v (body=%s)", err, rec.Body.String())
		}
		if env.Code != 0 {
			t.Fatalf("connect code = %d (body=%s)", env.Code, rec.Body.String())
		}
		return env.Data.PanelVersion, env.Data.VersionWarning
	}

	pv, warn := call("", false)
	if pv == "" {
		t.Fatal("V5-3: connect response must carry provider panelVersion")
	}
	if warn == "" {
		t.Fatal("V5-3: version-less (pre-1.3.4) consumer must get a versionWarning, got none")
	}
	if _, warn := call("1.2.0", true); warn == "" {
		t.Fatal("V5-3: consumer 1.2.0 (< 1.3.4 relay-auth version) must get a versionWarning")
	}
	if _, warn := call("1.3.3", true); warn == "" {
		t.Fatal("V5-3: consumer 1.3.3 (cache hotfix, < 1.3.4 relay-auth version) must get a versionWarning")
	}
	if _, warn := call("1.3.4", true); warn != "" {
		t.Fatalf("V5-3: consumer 1.3.4 must not be warned, got %q", warn)
	}
	if _, warn := call("1.4.0", true); warn != "" {
		t.Fatalf("V5-3: consumer 1.4.0 must not be warned, got %q", warn)
	}
	if _, warn := call("dev", true); warn != "" {
		t.Fatalf("V5-3: unparseable consumer version must not be warned (cannot judge), got %q", warn)
	}
}

func TestFix2BStartupRevalidationPausesDangerousStoredResources(t *testing.T) {
	h, r, share := fix132ShareFixture(t, 31000, 31099)
	now := time.Now().UnixMilli()
	seed := []repo.PeerShareResource{
		{ShareID: share.ID, NodeID: share.NodeID, Kind: "service", OriginalName: "evil-file", RuntimeName: "peer-evil-file",
			Config: `{"name":"peer-evil-file","handler":{"type":"file"},"listener":{"type":"tcp"}}`, DesiredState: "active", UpdatedTime: now},
		{ShareID: share.ID, NodeID: share.NodeID, Kind: "chain", OriginalName: "evil-chain", RuntimeName: "peer-evil-chain",
			Config: `{"name":"peer-evil-chain","hops":[{"nodes":[{"name":"n1","connector":{"type":"sshd"},"dialer":{"type":"tls"}}]}]}`, DesiredState: "active", UpdatedTime: now},
		{ShareID: share.ID, NodeID: share.NodeID, Kind: "service", OriginalName: "good", RuntimeName: "peer-good",
			Config: `{"name":"peer-good","handler":{"type":"tcp"},"listener":{"type":"tcp"}}`, DesiredState: "active", UpdatedTime: now},
		{ShareID: share.ID, NodeID: share.NodeID, Kind: "limiter", OriginalName: "lim", RuntimeName: "peer-lim",
			Config: `{"handler":{"type":"file"}}`, DesiredState: "active", UpdatedTime: now},
		{ShareID: share.ID, NodeID: share.NodeID, Kind: "service", OriginalName: "gone", RuntimeName: "peer-gone",
			Config: `{"name":"peer-gone","handler":{"type":"file"},"listener":{"type":"tcp"}}`, DesiredState: "deleted", UpdatedTime: now},
		{ShareID: share.ID, NodeID: share.NodeID, Kind: "service", OriginalName: "broken", RuntimeName: "peer-broken",
			Config: `{not-json`, DesiredState: "active", UpdatedTime: now},
	}
	if err := r.SavePeerShareResources(seed); err != nil {
		t.Fatalf("seed resources: %v", err)
	}

	h.revalidateStoredPeerShareResources()

	items, err := r.ListPeerShareResourcesByNode(share.NodeID)
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	states := make(map[string]string, len(items))
	for _, it := range items {
		states[it.OriginalName] = it.DesiredState
	}
	want := map[string]string{
		"evil-file":  "paused",  // handler=file 在白名单外
		"evil-chain": "paused",  // connector=sshd 在白名单外
		"good":       "active",  // 合法面板类型不动
		"lim":        "active",  // limiter 不在类型复检面
		"gone":       "deleted", // 墓碑不动
		"broken":     "paused",  // 配置不可解析 fail-closed
	}
	for name, w := range want {
		if got := states[name]; got != w {
			t.Fatalf("V5-3: resource %q DesiredState = %q, want %q (all: %v)", name, got, w, states)
		}
	}
}
