package handler

import (
	"strings"
	"testing"
)

// F2-T1-02 钉格（1.9.5 发版后审计）：隧道更新的段口变更回执此前
// 不分成因、统一拼「（原端口已被占用或不在节点端口范围内）」——
// 节点改任另一段（原段口不跨段沿用）时成因与占用/范围无关，按
// 该后缀排查必然扑空。现 tunnelPortChange 带 RoleChanged 标记：
// 改任出「改任另一段不沿用原段口」，占用/越界仍出原后缀。

// 改任场景：节点 16 原任出口段（口 36150），更新后改任链路段。
// 新口在节点规格内随机重挑，与旧口巧合相同（概率 1/100）时不
// 产生变更记录，故最多三轮独立场景取一轮实际变更者断言文案。
func TestTunnelUpdateRoleChangeWarningNamesRealCause(t *testing.T) {
	observed := false
	for attempt := 0; attempt < 3 && !observed; attempt++ {
		h := setupK9Handler(t)
		seedK9Node(t, h.repo, 16, "role-a", "10.0.0.7", "36100-36199")
		seedK6TunnelWithRows(t, h, 96, "k6-role", []k6ChainRow{
			{"1", 10, nil, 1},
			{"3", 16, 36150, 1},
		})

		env := k9Post(t, h, h.tunnelUpdate, "/api/v1/tunnel/update", k6UpdatePayload(96, "k6-role", 16))
		if env.Code != 0 {
			t.Fatalf("role-change update must succeed, code=%d msg=%s", env.Code, env.Msg)
		}
		newPort := k6ChainPortByNode(t, h, 96, "2", 16)
		if newPort < 36100 || newPort > 36199 {
			t.Fatalf("re-picked chain port must stay in node spec, got %d", newPort)
		}
		joined := k6WarningsJoined(env)
		if newPort == 36150 {
			// 随机重挑巧合命中旧口：端口未变、无回执点名，换一轮。
			if joined != "" {
				t.Fatalf("unchanged port must not warn, got %q", joined)
			}
			continue
		}
		observed = true
		if !strings.Contains(joined, "改任") || !strings.Contains(joined, "不沿用原段口") {
			t.Fatalf("role-change warning must name the role change, got %q", joined)
		}
		if !strings.Contains(joined, "36150") {
			t.Fatalf("role-change warning must name the old port, got %q", joined)
		}
		if strings.Contains(joined, "已被占用或不在节点端口范围内") {
			t.Fatalf("role-change warning must not blame occupancy/range, got %q", joined)
		}
	}
	if !observed {
		t.Fatal("no role-change port move observed in 3 attempts (re-pick kept colliding with the old port)")
	}
}

// 对照面：同段内现口被占用的重挑，仍出原后缀（两成因文案不得
// 互相污染）。
func TestTunnelUpdateOccupiedWarningKeepsOriginalSuffix(t *testing.T) {
	h := setupK9Handler(t)
	seedK9Node(t, h.repo, 14, "chain-b", "10.0.0.5", "36500-36510")
	seedK6TunnelWithRows(t, h, 97, "k6-occ", k6TwoHopRows())
	seedK9PeerReservation(t, h, 14, 36508)

	env := k9Post(t, h, h.tunnelUpdate, "/api/v1/tunnel/update", k6UpdatePayload(97, "k6-occ", 14, 12))
	if env.Code != 0 {
		t.Fatalf("occupied update must succeed, code=%d msg=%s", env.Code, env.Msg)
	}
	joined := k6WarningsJoined(env)
	if !strings.Contains(joined, "（原端口已被占用或不在节点端口范围内）") {
		t.Fatalf("occupied re-pick must keep the occupancy/range suffix, got %q", joined)
	}
	if strings.Contains(joined, "改任") {
		t.Fatalf("occupied re-pick must not be worded as a role change, got %q", joined)
	}
}
