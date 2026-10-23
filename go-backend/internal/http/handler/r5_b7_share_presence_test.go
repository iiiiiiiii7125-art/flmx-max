package handler

import (
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R5 批 7 回归（R5X-012 / R5-D-01）：入站部分更新误翻 shareAddressMode。
// 裁定口径：shareAddressMode 解析只认请求显式携带的字段；presence 回填的
// 存量 shareHost 不得被当作显式携带触发模式翻转（休眠 host 不被激活）。
// 两键皆省略时模式与 host 均保持存量；显式携带的旧行为不变——显式
// shareHost 单独在场仍转 custom、显式 mode=ipv4/ipv6 在场仍清 host、
// 前端全量表单（整对象同时发送 mode+host）不回退。

func r5b7SeedDormantInbound(t *testing.T, db, mode, host string, withV6 bool) (*repo.Repository, *Handler, *model.NodeInbound) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/" + db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nodeID := seedRelayTargetNode(t, r, "b7-node", "198.51.100.60")
	if withV6 {
		if err := r.DB().Model(&model.Node{}).Where("id = ?", nodeID).Update("server_ip_v6", "2001:db8::60").Error; err != nil {
			t.Fatal(err)
		}
	}
	entry := seedShareInbound(t, r, nodeID, "b7-entry")
	entry.ShareAddressMode = mode
	entry.ShareHost = host
	if err := r.SaveNodeInbound(entry); err != nil {
		t.Fatal(err)
	}
	stored, err := r.GetNodeInbound(entry.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload seed: %v", err)
	}
	if got := inboundShareMode(stored); got != mode {
		t.Fatalf("seed precondition: stored (%q,%q) resolves to %q, want %q", mode, host, got, mode)
	}
	return r, h, stored
}

func r5b7Reload(t *testing.T, r *repo.Repository, id int64) *model.NodeInbound {
	t.Helper()
	got, err := r.GetNodeInbound(id)
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	return got
}

func TestR5B7InboundPartialUpdateDormantShareHost(t *testing.T) {
	// 存量行显式 ipv4 模式 + 休眠 ShareHost：部分更新同时省略两键，
	// 模式不得被回填的 host 翻转为 custom，host 亦保持存量。
	t.Run("omit both keys keeps ipv4 mode and dormant host", func(t *testing.T) {
		r, h, entry := r5b7SeedDormantInbound(t, "r5b7-ipv4.db", "ipv4", "dormant.example.com", false)
		body := r4b3InboundBody(t, entry, map[string]interface{}{"name": "b7-entry-renamed"})
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("partial update must succeed: %+v", out)
		}
		got := r5b7Reload(t, r, entry.ID)
		if got.Name != "b7-entry-renamed" {
			t.Fatalf("rename must apply: %q", got.Name)
		}
		if got.ShareAddressMode != "ipv4" {
			t.Fatalf("R5X-012: omitted share keys flipped mode to %q, want ipv4 preserved", got.ShareAddressMode)
		}
		if got.ShareHost != "dormant.example.com" {
			t.Fatalf("R5X-012: omitted share keys must preserve dormant host, got %q", got.ShareHost)
		}
	})

	t.Run("omit both keys keeps ipv6 mode and dormant host", func(t *testing.T) {
		r, h, entry := r5b7SeedDormantInbound(t, "r5b7-ipv6.db", "ipv6", "dormant.example.com", true)
		body := r4b3InboundBody(t, entry, map[string]interface{}{"name": "b7-entry-renamed"})
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("partial update must succeed: %+v", out)
		}
		got := r5b7Reload(t, r, entry.ID)
		if got.ShareAddressMode != "ipv6" {
			t.Fatalf("R5X-012: omitted share keys flipped mode to %q, want ipv6 preserved", got.ShareAddressMode)
		}
		if got.ShareHost != "dormant.example.com" {
			t.Fatalf("R5X-012: omitted share keys must preserve dormant host, got %q", got.ShareHost)
		}
	})

	// 变体格：shareAddressMode 显式传 null 与未携带同义（指针 presence
	// 惯例，与 OutboundIPStrategy / Enabled 同口径），不得触发模式解析。
	t.Run("explicit null mode keeps ipv4 mode and dormant host", func(t *testing.T) {
		r, h, entry := r5b7SeedDormantInbound(t, "r5b7-null-mode.db", "ipv4", "dormant.example.com", false)
		body := r4b3InboundBody(t, entry, map[string]interface{}{"shareAddressMode": nil})
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("null-mode update must succeed: %+v", out)
		}
		got := r5b7Reload(t, r, entry.ID)
		if got.ShareAddressMode != "ipv4" || got.ShareHost != "dormant.example.com" {
			t.Fatalf("R5X-012: null mode must not flip stored pair: mode=%q host=%q", got.ShareAddressMode, got.ShareHost)
		}
	})

	// 防过修对照：(custom, host) 存量行省略两键仍保持 custom（既有
	// TestR4B3InboundUpdatePresenceMatrix 第一子项同口径，此处同批复核）。
	t.Run("omit both keys keeps custom mode and host", func(t *testing.T) {
		r, h, entry := r5b7SeedDormantInbound(t, "r5b7-custom.db", "custom", "keep.example.com", false)
		body := r4b3InboundBody(t, entry, map[string]interface{}{"name": "b7-entry-renamed"})
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("partial update must succeed: %+v", out)
		}
		got := r5b7Reload(t, r, entry.ID)
		if got.ShareAddressMode != "custom" || got.ShareHost != "keep.example.com" {
			t.Fatalf("custom row must stay custom with its host: mode=%q host=%q", got.ShareAddressMode, got.ShareHost)
		}
	})

	// 防过修对照：显式 shareHost 单独在场（无 mode）仍按旧行为转 custom。
	t.Run("explicit shareHost alone still switches to custom", func(t *testing.T) {
		r, h, entry := r5b7SeedDormantInbound(t, "r5b7-explicit-host.db", "ipv4", "dormant.example.com", false)
		body := r4b3InboundBody(t, entry, map[string]interface{}{"shareHost": "explicit.example.com"})
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("explicit-host update must succeed: %+v", out)
		}
		got := r5b7Reload(t, r, entry.ID)
		if got.ShareAddressMode != "custom" || got.ShareHost != "explicit.example.com" {
			t.Fatalf("explicit shareHost must switch to custom: mode=%q host=%q", got.ShareAddressMode, got.ShareHost)
		}
	})

	// 防过修对照：前端全量表单路径——整对象同时显式发送 mode+host，
	// 显式 mode=ipv4 仍清 host；显式 custom+host 仍原样落库。
	t.Run("full form explicit ipv4 mode still clears host", func(t *testing.T) {
		r, h, entry := r5b7SeedDormantInbound(t, "r5b7-full-ipv4.db", "ipv4", "dormant.example.com", false)
		body := r4b3InboundBody(t, entry, map[string]interface{}{
			"shareAddressMode": "ipv4", "shareHost": "dormant.example.com",
		})
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("full-form update must succeed: %+v", out)
		}
		got := r5b7Reload(t, r, entry.ID)
		if got.ShareAddressMode != "ipv4" || got.ShareHost != "" {
			t.Fatalf("explicit ipv4 mode must clear host: mode=%q host=%q", got.ShareAddressMode, got.ShareHost)
		}
	})

	t.Run("full form explicit custom mode keeps host", func(t *testing.T) {
		r, h, entry := r5b7SeedDormantInbound(t, "r5b7-full-custom.db", "ipv4", "dormant.example.com", false)
		body := r4b3InboundBody(t, entry, map[string]interface{}{
			"shareAddressMode": "custom", "shareHost": "chosen.example.com",
		})
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("full-form update must succeed: %+v", out)
		}
		got := r5b7Reload(t, r, entry.ID)
		if got.ShareAddressMode != "custom" || got.ShareHost != "chosen.example.com" {
			t.Fatalf("explicit custom mode must keep chosen host: mode=%q host=%q", got.ShareAddressMode, got.ShareHost)
		}
	})
}
