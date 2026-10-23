package repo

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// G1 (X-01): backup import must not resurrect the silent B-01 shape —
// a row restored with the metering switch on but a shape a companion
// can never serve (chained exit / split rules / relay-only / PROXY
// protocol). The save surface refuses that combination and self-heals
// stored ones by closing the switch; the import surface now applies
// the same disposition and discloses it in the import warnings.
func TestBackupImportClosesMeteringOnShapeBlockedRows(t *testing.T) {
	now := time.Now().UnixMilli()
	src, err := Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	createRow(t, src, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, src, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})

	ssSettings := `{"method":"2022-blake3-aes-128-gcm","password":"shared"}`
	target := &model.NodeInbound{KernelID: 1, NodeID: 1, Name: "exit", Protocol: "vless", Port: 21001,
		CreatedTime: now, UpdatedTime: now}
	if err := src.CreateNodeInbound(target); err != nil {
		t.Fatal(err)
	}
	chained := &model.NodeInbound{KernelID: 1, NodeID: 1, Name: "ss-chained", Protocol: "shadowsocks", Port: 21002,
		Settings: ssSettings, MeteringEnabled: 1, OutboundID: target.ID, CreatedTime: now, UpdatedTime: now}
	if err := src.CreateNodeInbound(chained); err != nil {
		t.Fatal(err)
	}
	split := &model.NodeInbound{KernelID: 1, NodeID: 1, Name: "ss-split", Protocol: "shadowsocks", Port: 21003,
		Settings: ssSettings, MeteringEnabled: 1,
		SplitRules:  `[{"domains":["example.com"],"action":"proxy","outboundId":` + strconv.FormatInt(target.ID, 10) + `}]`,
		CreatedTime: now, UpdatedTime: now}
	if err := src.CreateNodeInbound(split); err != nil {
		t.Fatal(err)
	}
	direct := &model.NodeInbound{KernelID: 1, NodeID: 1, Name: "ss-direct", Protocol: "shadowsocks", Port: 21004,
		Settings: ssSettings, MeteringEnabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := src.CreateNodeInbound(direct); err != nil {
		t.Fatal(err)
	}

	backup, err := src.ExportPartial([]string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}

	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	createRow(t, dst, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, dst, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})

	result, err := dst.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := dst.GetNodeInbound(chained.ID)
	if err != nil || got == nil {
		t.Fatalf("restored chained row: %v", err)
	}
	if got.MeteringEnabled != 0 {
		t.Fatalf("chained-exit origin must not restore with metering on (silent never-metered shape), got %d", got.MeteringEnabled)
	}
	if got.OutboundID != target.ID {
		t.Fatalf("the chained exit itself is a valid reference and must be kept, got outbound=%d", got.OutboundID)
	}

	got, err = dst.GetNodeInbound(split.ID)
	if err != nil || got == nil {
		t.Fatalf("restored split row: %v", err)
	}
	if got.MeteringEnabled != 0 {
		t.Fatalf("split-routed origin must not restore with metering on, got %d", got.MeteringEnabled)
	}
	if !strings.Contains(got.SplitRules, "example.com") {
		t.Fatalf("the split rules themselves are valid and must be kept, got %q", got.SplitRules)
	}

	got, err = dst.GetNodeInbound(direct.ID)
	if err != nil || got == nil {
		t.Fatalf("restored direct row: %v", err)
	}
	if got.MeteringEnabled != 1 {
		t.Fatalf("a direct metering origin must round-trip with its switch on, got %d", got.MeteringEnabled)
	}

	warned := false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "计量") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("closing restored metering switches must be disclosed in import warnings, got %v", result.ImportWarnings)
	}
}
