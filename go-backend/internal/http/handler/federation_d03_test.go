package handler

import (
	"testing"
)

// D-03（发版后审计）：联邦 federationTunnelCreate 在分享端口段未设置
// （0-0）时放行 RemotePort=0，生成 Port=0 的 runtime；兄弟入口
// pickPeerSharePort 对同种情况 fail-closed。0-0 下 RemotePort=39999
// 早已被既有范围检查挡下（TestFederationPortPathsConsistentOnUnsetRange），
// 但 RemotePort=0 满足 0<0 与 0>0 均不成立、从检查中漏过。
func TestFederationTunnelCreateRejectsPortZeroOnUnsetRange(t *testing.T) {
	h, r, share := fix132ShareFixture(t, 0, 0)

	payload := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, federationTunnelRequest{
		Protocol: "tcp", RemotePort: 0, Target: "1.1.1.1:443",
	}, h.federationTunnelCreate)
	if payload.Code != 403 {
		t.Fatalf("tunnel/create RemotePort=0 on unset range: expected 403, got %d (%s)", payload.Code, payload.Msg)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(1) FROM tunnel WHERE name LIKE 'Share-%-Port-0'`); n != 0 {
		t.Fatalf("rejected create must not persist a tunnel, found %d", n)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(1) FROM peer_share_runtime WHERE share_id = ? AND port = 0`, share.ID); n != 0 {
		t.Fatalf("rejected create must not create a Port=0 runtime, found %d", n)
	}

	// 半设置（start>0, end=0）同样是无效范围，RemotePort=0 须同口径拒绝。
	h2, r2, share2 := fix132ShareFixture(t, 31000, 0)
	payload = fix132PeerRequest(t, h2, "/api/v1/federation/tunnel/create", share2.Token, federationTunnelRequest{
		Protocol: "tcp", RemotePort: 0, Target: "1.1.1.1:443",
	}, h2.federationTunnelCreate)
	if payload.Code != 403 {
		t.Fatalf("tunnel/create RemotePort=0 on half-configured range: expected 403, got %d (%s)", payload.Code, payload.Msg)
	}
	if n := mustQueryInt(t, r2, `SELECT COUNT(1) FROM peer_share_runtime WHERE share_id = ? AND port = 0`, share2.ID); n != 0 {
		t.Fatalf("rejected create must not create a Port=0 runtime, found %d", n)
	}

	// 正例：有效范围 + 范围内端口仍可建（与 pickPeerSharePort 同口径）。
	h3, r3, share3 := fix132ShareFixture(t, 20000, 20010)
	payload = fix132PeerRequest(t, h3, "/api/v1/federation/tunnel/create", share3.Token, federationTunnelRequest{
		Protocol: "tcp", RemotePort: 20005, Target: "1.1.1.1:443",
	}, h3.federationTunnelCreate)
	if payload.Code != 0 {
		t.Fatalf("tunnel/create in-range port must still succeed, got %d (%s)", payload.Code, payload.Msg)
	}
	if n := mustQueryInt(t, r3, `SELECT COUNT(1) FROM peer_share_runtime WHERE share_id = ? AND port = 20005 AND status = 1`, share3.ID); n != 1 {
		t.Fatalf("expected 1 runtime row for port 20005, got %d", n)
	}
}
