package handler

import (
	"encoding/json"
	"testing"

	"go-backend/internal/store/repo"
)

// B6-06：存量联邦资源启动复检此前只重跑 D3-03 类型白名单、且整体跳过
// limiter——升级前植入的引用面违规（shell hook、注册表引用、hop
// loader）、B6-01 的证书路径与 limiter loader 都能原样存活并在节点
// 重连对账时物化；且对端可用一条 ResumeService 把复检置 paused 的
// 植入配置直接复活。本文件钉修复后的同律口径：复检跑与写入面完全
// 相同的校验全集（引用＋类型＋本机路径，含 limiter/climiter），不
// 过者置 paused；resume 前对存量配置重跑同一全集，不过不许复活。

func b606SeedResource(t *testing.T, h *Handler, share *repo.PeerShare, kind, name, desired string, config map[string]interface{}) {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	item := repo.PeerShareResource{
		ShareID: share.ID, NodeID: share.NodeID, Kind: kind, OriginalName: name,
		RuntimeName: peerShareResourceName(share.ID, kind, name),
		Config: string(raw), DesiredState: desired, Applied: 1,
	}
	if err := h.repo.SavePeerShareResources([]repo.PeerShareResource{item}); err != nil {
		t.Fatal(err)
	}
}

func b606DesiredState(t *testing.T, h *Handler, share *repo.PeerShare, kind, name string) string {
	t.Helper()
	item, err := h.repo.GetPeerShareResource(share.ID, kind, name)
	if err != nil {
		t.Fatal(err)
	}
	if item == nil {
		t.Fatalf("resource %s/%s missing", kind, name)
	}
	return item.DesiredState
}

func TestB606StoredRevalidationRunsFullWriteTimeValidation(t *testing.T) {
	a := newCleanupAgent(t)
	share := resourceTestShare(t, a.h, "b606-share")

	b606SeedResource(t, a.h, share, "service", "tlspath", "active", map[string]interface{}{
		"handler":  map[string]interface{}{"type": "tcp"},
		"listener": map[string]interface{}{"type": "tls", "tls": map[string]interface{}{"certFile": "/etc/flmx_agent/cert.pem", "keyFile": "/etc/flmx_agent/key.pem"}},
	})
	b606SeedResource(t, a.h, share, "service", "hooksvc", "active", map[string]interface{}{
		"handler":  map[string]interface{}{"type": "tcp", "metadata": map[string]interface{}{"preup": "id > /tmp/pwned"}},
		"listener": map[string]interface{}{"type": "tcp"},
	})
	b606SeedResource(t, a.h, share, "chain", "chaintls", "active", map[string]interface{}{
		"hops": []interface{}{map[string]interface{}{"name": "h1", "nodes": []interface{}{map[string]interface{}{"name": "n1", "addr": "127.0.0.1:8080", "connector": map[string]interface{}{"type": "relay"}, "dialer": map[string]interface{}{"type": "tls", "tls": map[string]interface{}{"keyFile": "/root/key.pem"}}}}}},
	})
	b606SeedResource(t, a.h, share, "limiter", "filelimiter", "active", map[string]interface{}{
		"name": "filelimiter",
		"file": map[string]interface{}{"path": "/tmp/limits.txt"},
	})
	b606SeedResource(t, a.h, share, "service", "goodsvc", "active", map[string]interface{}{
		"handler":  map[string]interface{}{"type": "tcp"},
		"listener": map[string]interface{}{"type": "tcp"},
	})
	b606SeedResource(t, a.h, share, "service", "sshdsvc", "active", map[string]interface{}{
		"handler":  map[string]interface{}{"type": "sshd"},
		"listener": map[string]interface{}{"type": "tcp"},
	})

	restarted := &Handler{repo: a.h.repo}
	restarted.revalidateStoredPeerShareResources()

	for _, name := range []string{"tlspath", "hooksvc", "sshdsvc"} {
		if got := b606DesiredState(t, a.h, share, "service", name); got != "paused" {
			t.Fatalf("stored service %q desired_state = %q, want paused", name, got)
		}
	}
	if got := b606DesiredState(t, a.h, share, "chain", "chaintls"); got != "paused" {
		t.Fatalf("stored chain with dialer keyFile desired_state = %q, want paused", got)
	}
	if got := b606DesiredState(t, a.h, share, "limiter", "filelimiter"); got != "paused" {
		t.Fatalf("stored limiter with file loader desired_state = %q, want paused", got)
	}
	if got := b606DesiredState(t, a.h, share, "service", "goodsvc"); got != "active" {
		t.Fatalf("valid stored service desired_state = %q, want active (unchanged)", got)
	}
}

func TestB606ResumeRechecksStoredConfig(t *testing.T) {
	a := newCleanupAgent(t)
	share := resourceTestShare(t, a.h, "b606-resume-share")

	b606SeedResource(t, a.h, share, "service", "planted", "paused", map[string]interface{}{
		"handler":  map[string]interface{}{"type": "tcp"},
		"listener": map[string]interface{}{"type": "tls", "tls": map[string]interface{}{"certFile": "/etc/flmx_agent/cert.pem", "keyFile": "/etc/flmx_agent/key.pem"}},
	})
	b606SeedResource(t, a.h, share, "service", "goodpaused", "paused", map[string]interface{}{
		"handler":  map[string]interface{}{"type": "tcp"},
		"listener": map[string]interface{}{"type": "tcp"},
	})

	before := len(a.commandsOfType("UpdateService"))
	result := resourceTestCommand(t, a.h, share, "ResumeService", map[string]interface{}{"services": []string{"planted"}})
	if result.Code == 0 {
		t.Fatalf("resume of a stored config carrying cert file paths was accepted: %+v", result)
	}
	if got := b606DesiredState(t, a.h, share, "service", "planted"); got != "paused" {
		t.Fatalf("planted resource desired_state = %q after refused resume, want paused", got)
	}
	if after := len(a.commandsOfType("UpdateService")); after != before {
		t.Fatalf("refused resume still sent %d UpdateService command(s) to the node", after-before)
	}

	ok := resourceTestCommand(t, a.h, share, "ResumeService", map[string]interface{}{"services": []string{"goodpaused"}})
	if ok.Code != 0 {
		t.Fatalf("resume of a valid paused resource rejected: %s", ok.Msg)
	}
	if got := b606DesiredState(t, a.h, share, "service", "goodpaused"); got != "active" {
		t.Fatalf("valid resumed resource desired_state = %q, want active", got)
	}
}
