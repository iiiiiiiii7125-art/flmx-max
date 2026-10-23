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

// G2 (X-02): the import post-reference check must treat a metering
// companion as an unusable chain/split target, exactly like a missing
// row — the save surface refuses companions as targets outright
// (validateChain), because chain assembly dials the target's own row
// (a companion's bare identity PSK is rejected in multi-user mode)
// and a later companion recycle would leave the reference dangling
// and fail the whole kernel payload build. References to plain
// inbounds stay untouched.
func TestBackupImportStripsCompanionChainTargets(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	createRow(t, dst, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, dst, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})
	createRow(t, dst, &model.NodeInbound{ID: 51, KernelID: 1, NodeID: 1, Name: "ss-origin", Protocol: "shadowsocks",
		Port: 22001, Settings: `{"method":"2022-blake3-aes-128-gcm","password":"shared"}`,
		MeteringEnabled: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dst, &model.NodeInbound{ID: 59, KernelID: 1, NodeID: 1, Name: "ss-origin（计量）", Protocol: "shadowsocks",
		Port: 22002, Settings: `{"method":"2022-blake3-aes-128-gcm","password":"ipsk"}`,
		CompanionOf: 51, CreatedTime: now, UpdatedTime: now})

	src, err := Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	createRow(t, src, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, src, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})
	ssSettings := `{"method":"2022-blake3-aes-128-gcm","password":"shared"}`
	createRow(t, src, &model.NodeInbound{ID: 53, KernelID: 1, NodeID: 1, Name: "ss-chain-to-companion", Protocol: "shadowsocks",
		Port: 21053, Settings: ssSettings, OutboundID: 59, CreatedTime: now, UpdatedTime: now})
	createRow(t, src, &model.NodeInbound{ID: 54, KernelID: 1, NodeID: 1, Name: "ss-split-to-companion", Protocol: "shadowsocks",
		Port: 21054, Settings: ssSettings,
		SplitRules:  `[{"domains":["x.example"],"action":"proxy","outboundId":59}]`,
		CreatedTime: now, UpdatedTime: now})
	createRow(t, src, &model.NodeInbound{ID: 55, KernelID: 1, NodeID: 1, Name: "ss-chain-to-origin", Protocol: "shadowsocks",
		Port: 21055, Settings: ssSettings, OutboundID: 51, CreatedTime: now, UpdatedTime: now})

	backup, err := src.ExportPartial([]string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := dst.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := dst.GetNodeInbound(53)
	if err != nil || got == nil {
		t.Fatalf("restored row 53: %v", err)
	}
	if got.OutboundID != 0 {
		t.Fatalf("a chained exit pointing at a companion must be stripped to direct, got outbound=%d", got.OutboundID)
	}
	got, err = dst.GetNodeInbound(54)
	if err != nil || got == nil {
		t.Fatalf("restored row 54: %v", err)
	}
	if strings.Contains(got.SplitRules, "59") {
		t.Fatalf("a split rule pointing at a companion must be pruned, got %q", got.SplitRules)
	}
	got, err = dst.GetNodeInbound(55)
	if err != nil || got == nil {
		t.Fatalf("restored row 55: %v", err)
	}
	if got.OutboundID != 51 {
		t.Fatalf("a chained exit pointing at a plain inbound must be kept, got outbound=%d", got.OutboundID)
	}

	warned := false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "后置校验") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("stripping companion targets must be disclosed in import warnings, got %v", result.ImportWarnings)
	}
}
