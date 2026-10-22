package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// X-P3-4（D-R1-01）：inboundSave 更新分支此前对省略的 port 无回退——
// 只改名不带 port 的更新会被静默改派随机端口；同 handler 的密钥/策略/
// Enabled 都是 presence 保留，唯 port 例外。创建路径不带 port 仍随机。

func callInboundSave(t *testing.T, h *Handler, update bool, body string) response.R {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inbound/save", strings.NewReader(body))
	if update {
		h.inboundUpdate(rec, req)
	} else {
		h.inboundCreate(rec, req)
	}
	var out response.R
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode inbound save resp: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

func TestR1B7InboundUpdateWithoutPortKeepsStoredPort(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/inbound_port.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "port-node", "198.51.100.20")
	box := seedChainKernel(t, r, nodeID, kernel.TypeSingbox)
	entry := seedChainInbound(t, r, box, "port-entry", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388}, 0)

	old, err := inboundFromModel(*entry)
	if err != nil {
		t.Fatal(err)
	}
	// 只改名、不带 port（port 字段缺席 = 0）。
	reqBody := inboundRequest{
		ID: entry.ID, Name: "port-entry-renamed", Protocol: old.Protocol, Listen: old.Listen,
		Settings: old.Settings, Transport: old.Transport, Security: old.Security,
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatal(err)
	}
	if out := callInboundSave(t, h, true, string(raw)); out.Code != 0 {
		t.Fatalf("update must succeed: %+v", out)
	}
	got, err := r.GetNodeInbound(entry.ID)
	if err != nil || got == nil {
		t.Fatalf("reload inbound: %v", err)
	}
	if got.Name != "port-entry-renamed" {
		t.Fatalf("rename must apply: %+v", got)
	}
	if got.Port != 8388 {
		t.Fatalf("update without port must keep stored port 8388, got %d", got.Port)
	}

	// 防过修对照：创建不带 port 仍随机分配（20000-59999 池）。
	createBody := inboundRequest{
		NodeID: nodeID, KernelType: kernel.TypeSingbox, Name: "port-created",
		Protocol: kernel.ProtocolShadowsocks,
		Settings: old.Settings, Security: old.Security,
	}
	raw, err = json.Marshal(createBody)
	if err != nil {
		t.Fatal(err)
	}
	out := callInboundSave(t, h, false, string(raw))
	if out.Code != 0 {
		t.Fatalf("create must succeed: %+v", out)
	}
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == "port-created" {
			if row.Port < 20000 || row.Port > 59999 {
				t.Fatalf("create without port must random-assign from the pool, got %d", row.Port)
			}
			return
		}
	}
	t.Fatal("created inbound not found")
}
