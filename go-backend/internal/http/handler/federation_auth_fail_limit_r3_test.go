package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

// R3-C1-01 回归：federation 对端鉴权面（authPeer，7 路由共用收口）
// 此前无失败节流，未知令牌每次尝试直达 GetPeerShareByToken 查库，
// 与 flow 面 R8-060 同型缺口。修复后与 flow 面同口径：只记未知
// 令牌失败、同 IP 达 60 次/分钟后短路（不再查库、有效令牌同被
// 拒且回包与未知令牌同形）、有效令牌永不计数、异 IP 互不影响、
// 与 flow 面限流器实例独立。

func r3PeerAuthHandler(t *testing.T) *Handler {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "peer-auth-fail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return &Handler{repo: r}
}

// r3PeerAttempt 经 authPeer 收口发一次请求，返回（是否到达 next，
// 信封 code）。next 到达时回 code 0。
func r3PeerAttempt(t *testing.T, h *Handler, token, remoteAddr string) (bool, int) {
	t.Helper()
	reached := false
	next := func(w http.ResponseWriter, r *http.Request) {
		reached = true
		response.WriteJSON(w, response.OKEmpty())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/federation/connect", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.authPeer(next)(rec, req)
	var env response.R
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, rec.Body.String())
	}
	return reached, env.Code
}

func TestAuthPeerThrottledAfterRepeatedUnknownToken(t *testing.T) {
	h := r3PeerAuthHandler(t)
	share := resourceTestShare(t, h, "r3-peer-valid-token")
	attacker := "203.0.113.7:40001"

	// 未知令牌喷洒达上限：每次都 401 且不到达 next（查库未命中）。
	for i := 0; i < flowAuthFailMaxPerWindow; i++ {
		reached, code := r3PeerAttempt(t, h, "no-such-token", attacker)
		if reached || code != 401 {
			t.Fatalf("unknown token attempt %d: reached=%t code=%d, want reached=false code=401", i, reached, code)
		}
	}
	// 达上限后：同 IP 即使持有效令牌也被短路（回包与未知令牌同形）。
	reached, code := r3PeerAttempt(t, h, share.Token, attacker)
	if reached || code != 401 {
		t.Fatalf("throttled valid token: reached=%t code=%d, want reached=false code=401", reached, code)
	}
	// 异 IP 持有效令牌不受影响，照常到达 next。
	reached, code = r3PeerAttempt(t, h, share.Token, "203.0.113.8:40001")
	if !reached || code != 0 {
		t.Fatalf("valid token from another IP: reached=%t code=%d, want reached=true code=0", reached, code)
	}
}

func TestAuthPeerSuccessesNeverCountTowardThrottle(t *testing.T) {
	h := r3PeerAuthHandler(t)
	share := resourceTestShare(t, h, "r3-peer-valid-token-2")
	ip := "203.0.113.9:40001"

	// 59 次失败与 30 次成功交错：成功不计数，失败未达上限，
	// 有效令牌全程可用。
	for i := 0; i < flowAuthFailMaxPerWindow-1; i++ {
		if reached, code := r3PeerAttempt(t, h, "no-such-token", ip); reached || code != 401 {
			t.Fatalf("failure %d: reached=%t code=%d, want reached=false code=401", i, reached, code)
		}
		if i < 30 {
			if reached, code := r3PeerAttempt(t, h, share.Token, ip); !reached || code != 0 {
				t.Fatalf("success %d: reached=%t code=%d, want reached=true code=0", i, reached, code)
			}
		}
	}
	if reached, code := r3PeerAttempt(t, h, share.Token, ip); !reached || code != 0 {
		t.Fatalf("valid token below threshold: reached=%t code=%d, want reached=true code=0", reached, code)
	}
	// 第 60 次失败达上限，其后有效令牌被短路。
	if reached, code := r3PeerAttempt(t, h, "no-such-token", ip); reached || code != 401 {
		t.Fatalf("threshold failure: reached=%t code=%d, want reached=false code=401", reached, code)
	}
	if reached, code := r3PeerAttempt(t, h, share.Token, ip); reached || code != 401 {
		t.Fatalf("valid token at threshold: reached=%t code=%d, want reached=false code=401", reached, code)
	}
}

func TestPeerAndFlowFailLimitersAreIndependentInstances(t *testing.T) {
	h := r3PeerAuthHandler(t)
	if h.peerFailLimiter() == h.flowFailLimiter() {
		t.Fatal("peer and flow fail limiters must be separate instances")
	}
	key := "203.0.113.10"
	now := time.Now()
	for i := 0; i < flowAuthFailMaxPerWindow; i++ {
		h.peerFailLimiter().recordFailure(key, now)
	}
	if !h.peerFailLimiter().blocked(key, now) {
		t.Fatal("peer limiter must block at the shared threshold")
	}
	if h.flowFailLimiter().blocked(key, now) {
		t.Fatal("peer failures must not count toward the flow limiter")
	}
}
