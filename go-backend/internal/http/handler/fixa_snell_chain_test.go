package handler

import (
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// C2-01 链路钉格：存量 Snell 行（settings 无 snellVersion，即 0）经
// inboundFromModel 直读后，Surge 产物不得出现 version=0。
func TestFixAStoredSnellRowSurgeVersion(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fixa_snell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	row := model.NodeInbound{KernelID: 1, NodeID: 1, Name: "fixa-snell-v0", Enabled: 1}
	legacy := kernel.Inbound{Protocol: kernel.ProtocolSnell, Port: 8388,
		Settings: kernel.Settings{Password: "psk-legacy"}} // SnellVersion 留 0，模拟存量行
	if err := applyInboundToModel(legacy, &row); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateNodeInbound(&row); err != nil {
		t.Fatal(err)
	}
	stored, err := r.GetNodeInbound(row.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload row: %v", err)
	}
	in, err := inboundFromModel(*stored)
	if err != nil {
		t.Fatal(err)
	}
	if in.Settings.SnellVersion != 0 {
		t.Fatalf("fixture must carry version 0, got %d", in.Settings.SnellVersion)
	}
	line, ok := kernel.SurgeLine(kernel.Endpoint{Name: "fixa-snell-v0", Host: "203.0.113.10", Port: 8388, Inbound: in, Direct: true})
	if !ok {
		t.Fatal("SurgeLine must render a stored Snell inbound")
	}
	if strings.Contains(line, "version=0") || !strings.Contains(line, "version=5") {
		t.Fatalf("stored Snell v0 row must render Surge version=5, got %q", line)
	}
}
