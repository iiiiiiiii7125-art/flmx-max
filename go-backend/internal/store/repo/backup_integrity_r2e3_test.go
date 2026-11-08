package repo

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"go-backend/internal/store/model"
)

// R2-E3-F03: config keys retired with a removed feature (the gist
// set) must never resurrect through a backup restore. The v8
// migration deletes the stored rows once, but the restore face
// used to pass unclassified keys through verbatim, so restoring
// any pre-removal backup re-created all ten keys — including the
// gist_token credential — with no reader and no cleanup path.
// The fix is a single retired-key list consumed by BOTH the
// migration and the backup filter skeleton; these tests pin the
// restore face, the export face, and the receipt naming.
func TestRetiredConfigKeysBlockedOnRestoreR2E3F03(t *testing.T) {
	r := openBackupRepo(t, "r2e3-f03.db")
	if len(retiredConfigKeysList) != 10 {
		t.Fatalf("retired key list must hold the 10 gist keys, got %d", len(retiredConfigKeysList))
	}
	// The retired set must stay OUT of the policy tables: the keys
	// are unregistered (DEV-RM-GIST pins), the retired set is a
	// separate backup/migration concern.
	for _, k := range retiredConfigKeysList {
		if !IsRetiredConfigKey(k) {
			t.Fatalf("%s must be reported retired", k)
		}
		if _, listed := sensitiveConfigKeys[k]; listed {
			t.Fatalf("%s must not re-enter sensitiveConfigKeys", k)
		}
		if _, listed := systemManagedConfigKeys[k]; listed {
			t.Fatalf("%s must not re-enter systemManagedConfigKeys", k)
		}
	}
	if !IsRetiredConfigKey("GIST_TOKEN") {
		t.Fatal("retired-key matching must be case-insensitive (restore keys are normalized)")
	}

	configs := map[string]string{}
	for _, k := range retiredConfigKeysList {
		configs[k] = "resurrected-value"
	}
	configs["app_name"] = "R2E3 Panel"
	configs["custom_probe_r2e3"] = "keep-me"

	result, err := r.Import(&model.BackupData{Version: "1.0", Configs: configs}, []string{"configs"})
	if err != nil {
		t.Fatalf("import configs: %v", err)
	}
	db := r.DB()
	for _, k := range retiredConfigKeysList {
		var count int64
		if err := db.Raw(`SELECT COUNT(*) FROM vite_config WHERE name = ?`, k).Row().Scan(&count); err != nil {
			t.Fatalf("count %s: %v", k, err)
		}
		if count != 0 {
			t.Fatalf("retired key %s must not land on restore, count = %d", k, count)
		}
	}
	var appName string
	if err := db.Raw(`SELECT value FROM vite_config WHERE name = 'app_name'`).Row().Scan(&appName); err != nil {
		t.Fatalf("read app_name: %v", err)
	}
	if appName != "R2E3 Panel" {
		t.Fatalf("normal keys must restore normally, app_name = %q", appName)
	}
	var probe string
	if err := db.Raw(`SELECT value FROM vite_config WHERE name = 'custom_probe_r2e3'`).Row().Scan(&probe); err != nil {
		t.Fatalf("read custom key: %v", err)
	}
	if probe != "keep-me" {
		t.Fatalf("unclassified keys must keep restoring (R3 K5), got %q", probe)
	}
	if result.ConfigsImported != 2 {
		t.Fatalf("ConfigsImported must count only landed keys, got %d", result.ConfigsImported)
	}
	if !b8WarningsContain(result.ImportWarnings, "gist_token") ||
		!b8WarningsContain(result.ImportWarnings, "已随功能下线注销") {
		t.Fatalf("receipt must name the dropped retired keys, got %v", result.ImportWarnings)
	}

	// Export face: the same skeleton keeps retired keys out of a
	// backup file even if a stray row exists in the table.
	filtered := FilterBackupConfigs(map[string]string{
		"gist_token": "ghp_should_never_travel",
		"app_name":   "Panel",
	})
	if _, ok := filtered["gist_token"]; ok {
		t.Fatal("export filter must exclude retired keys")
	}
	if filtered["app_name"] != "Panel" {
		t.Fatalf("export filter must keep normal keys, got %v", filtered)
	}

	// Single-source pin: the migration deletes exactly the shared
	// list (no second hard-coded copy may drift from it).
	if err := r.UpsertConfigs(configs, 1); err != nil {
		t.Fatalf("seed configs: %v", err)
	}
	if err := migrateRemoveGistConfigKeys(db); err != nil {
		t.Fatalf("migrateRemoveGistConfigKeys: %v", err)
	}
	for _, k := range retiredConfigKeysList {
		var count int64
		if err := db.Raw(`SELECT COUNT(*) FROM vite_config WHERE name = ?`, k).Row().Scan(&count); err != nil {
			t.Fatalf("count %s after migration: %v", k, err)
		}
		if count != 0 {
			t.Fatalf("migration must delete retired key %s, count = %d", k, count)
		}
	}
}

// r2e3PasskeyFixture builds one passkey row whose CredentialJSON is
// a real webauthn.Credential serialization (public-key material
// only, exactly as handler/passkey.go persists it).
func r2e3PasskeyFixture(t *testing.T, userID int64, seed byte, signCount uint32, createdAt int64) model.Passkey {
	t.Helper()
	cred := webauthn.Credential{
		ID:              []byte{seed, 0x02, 0x03, 0x04},
		PublicKey:       []byte{seed, 0x06, 0x07, 0x08},
		AttestationType: "none",
		Transport:       []protocol.AuthenticatorTransport{protocol.Internal},
		Flags: webauthn.CredentialFlags{
			UserPresent:    true,
			UserVerified:   true,
			BackupEligible: true,
		},
		Authenticator: webauthn.Authenticator{
			AAGUID:    []byte{seed, 0x09},
			SignCount: signCount,
		},
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		t.Fatalf("marshal credential: %v", err)
	}
	return model.Passkey{
		ID:             base64.RawURLEncoding.EncodeToString(cred.ID),
		UserID:         userID,
		Name:           "fixture key",
		CredentialJSON: string(raw),
		CreatedAt:      createdAt,
		LastUsedAt:     createdAt + 5,
	}
}

// R2-E3-F02: passkeys were the one user-owned table missing from
// the backup segment set — a disaster-recovery restore silently
// lost every user's passkey. The segment now round-trips the full
// usability payload (credential JSON incl. sign counter), and the
// receipt counts landings.
func TestPasskeyBackupRoundTripR2E3F02(t *testing.T) {
	const now = int64(1700000000)
	src := openBackupRepo(t, "r2e3-f02-src.db")
	uidA, err := src.CreateUser("pk-user-a", "hash", 1, 0, 0, 0, 0, 1, 0, now)
	if err != nil {
		t.Fatalf("create user A: %v", err)
	}
	uidB, err := src.CreateUser("pk-user-b", "hash", 1, 0, 0, 0, 0, 1, 0, now)
	if err != nil {
		t.Fatalf("create user B: %v", err)
	}
	keyA := r2e3PasskeyFixture(t, uidA, 0xA1, 7, now)
	keyB := r2e3PasskeyFixture(t, uidB, 0xB2, 3, now)
	if err := src.CreatePasskey(&keyA); err != nil {
		t.Fatalf("create passkey A: %v", err)
	}
	if err := src.CreatePasskey(&keyB); err != nil {
		t.Fatalf("create passkey B: %v", err)
	}

	backup, err := src.ExportAll()
	if err != nil {
		t.Fatalf("export all: %v", err)
	}
	if len(backup.Passkeys) != 2 {
		t.Fatalf("ExportAll must carry the passkeys segment, got %d rows", len(backup.Passkeys))
	}
	partial, err := src.ExportPartial([]string{"passkeys"})
	if err != nil {
		t.Fatalf("ExportPartial(passkeys) must be a legal type: %v", err)
	}
	if len(partial.Passkeys) != 2 {
		t.Fatalf("ExportPartial(passkeys) must carry 2 rows, got %d", len(partial.Passkeys))
	}

	// On-disk form: JSON round trip, then restore into a fresh repo
	// (users first, as a real full restore would).
	raw, err := json.Marshal(backup)
	if err != nil {
		t.Fatalf("marshal backup: %v", err)
	}
	var decoded model.BackupData
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal backup: %v", err)
	}
	dst := openBackupRepo(t, "r2e3-f02-dst.db")
	result, err := dst.Import(&decoded, []string{"users", "passkeys"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.PasskeysImported != 2 {
		t.Fatalf("PasskeysImported = %d, want 2", result.PasskeysImported)
	}
	for _, want := range []model.Passkey{keyA, keyB} {
		keys, err := dst.ListPasskeys(want.UserID)
		if err != nil {
			t.Fatalf("list passkeys for user %d: %v", want.UserID, err)
		}
		if len(keys) != 1 {
			t.Fatalf("user %d must hold exactly 1 restored passkey, got %d", want.UserID, len(keys))
		}
		got := keys[0]
		if got.ID != want.ID || got.Name != want.Name ||
			got.CredentialJSON != want.CredentialJSON ||
			got.CreatedAt != want.CreatedAt || got.LastUsedAt != want.LastUsedAt {
			t.Fatalf("restored passkey mismatch:\n got %+v\nwant %+v", got, want)
		}
		// The restored credential must still parse and keep its
		// sign counter — the usability payload, not just the row.
		var cred webauthn.Credential
		if err := json.Unmarshal([]byte(got.CredentialJSON), &cred); err != nil {
			t.Fatalf("restored credential must parse: %v", err)
		}
		var wantCred webauthn.Credential
		if err := json.Unmarshal([]byte(want.CredentialJSON), &wantCred); err != nil {
			t.Fatalf("fixture credential must parse: %v", err)
		}
		if cred.Authenticator.SignCount != wantCred.Authenticator.SignCount {
			t.Fatalf("sign counter lost in round trip: got %d, want %d",
				cred.Authenticator.SignCount, wantCred.Authenticator.SignCount)
		}
	}
}

// R2-E3-F02 skip accounting: rows whose user does not exist in the
// target are skipped and counted in SkippedRows["passkeys"];
// corrupt credential payloads are skipped and named, never landed.
func TestPasskeyRestoreSkipsMissingUserR2E3F02(t *testing.T) {
	const now = int64(1700000000)
	src := openBackupRepo(t, "r2e3-f02skip-src.db")
	uidA, err := src.CreateUser("pk-skip-a", "hash", 1, 0, 0, 0, 0, 1, 0, now)
	if err != nil {
		t.Fatalf("create user A: %v", err)
	}
	uidB, err := src.CreateUser("pk-skip-b", "hash", 1, 0, 0, 0, 0, 1, 0, now)
	if err != nil {
		t.Fatalf("create user B: %v", err)
	}
	if err := src.CreatePasskey(func() *model.Passkey { k := r2e3PasskeyFixture(t, uidA, 0xC1, 1, now); return &k }()); err != nil {
		t.Fatalf("create passkey A: %v", err)
	}
	if err := src.CreatePasskey(func() *model.Passkey { k := r2e3PasskeyFixture(t, uidB, 0xD2, 1, now); return &k }()); err != nil {
		t.Fatalf("create passkey B: %v", err)
	}
	backup, err := src.ExportAll()
	if err != nil {
		t.Fatalf("export all: %v", err)
	}

	// No users in the target: every passkey row skips, counted.
	dst := openBackupRepo(t, "r2e3-f02skip-dst.db")
	result, err := dst.Import(backup, []string{"passkeys"})
	if err != nil {
		t.Fatalf("import passkeys only: %v", err)
	}
	if result.PasskeysImported != 0 {
		t.Fatalf("PasskeysImported = %d, want 0 (no users exist)", result.PasskeysImported)
	}
	if result.SkippedRows["passkeys"] != 2 {
		t.Fatalf("SkippedRows[passkeys] = %d, want 2; receipt: %+v", result.SkippedRows["passkeys"], result.SkippedRows)
	}

	// Once the users land, the same segment restores in full.
	result, err = dst.Import(backup, []string{"users", "passkeys"})
	if err != nil {
		t.Fatalf("import users+passkeys: %v", err)
	}
	if result.PasskeysImported != 2 {
		t.Fatalf("PasskeysImported = %d, want 2 after users restored", result.PasskeysImported)
	}

	// Corrupt credential JSON: skipped, counted and named — the row
	// must not land (it could never be used at login anyway).
	bad := &model.BackupData{Version: "1.0", Passkeys: []model.PasskeyBackup{{
		ID: "corrupt-row-id", UserID: uidA, Name: "bad",
		CredentialJSON: "{not-json", CreatedAt: now,
	}}}
	result, err = dst.Import(bad, []string{"passkeys"})
	if err != nil {
		t.Fatalf("import corrupt passkey: %v", err)
	}
	if result.PasskeysImported != 0 || result.SkippedRows["passkeys"] != 1 {
		t.Fatalf("corrupt row must skip once: imported=%d skipped=%d",
			result.PasskeysImported, result.SkippedRows["passkeys"])
	}
	if !b8WarningsContain(result.ImportWarnings, "corrupt-row-id") {
		t.Fatalf("corrupt row must be named in the receipt, got %v", result.ImportWarnings)
	}
	if key, err := dst.GetPasskeyByID("corrupt-row-id"); err != nil || key != nil {
		t.Fatalf("corrupt passkey row must not land, got key=%v err=%v", key, err)
	}
}

// R2-E3 resurrection sweep (Snell cell): the rmsnell_v1 migration
// disables stored snell kernels/inbounds once, but an old backup
// restore used to write the rows back verbatim — snell kernel
// back enabled with its installed version, kernel-managed snell
// inbounds back enabled=1 — reversing the migration. Restore now
// applies the migration's own disposition: snell kernels land
// disabled with the installed version cleared, kernel-managed
// snell inbounds land disabled, and imported snell nodes
// (kernel_id=0, a kept feature) restore untouched.
func TestRestoreSnellRowsNeutralizedR2E3(t *testing.T) {
	r := openBackupRepo(t, "r2e3-snell.db")
	const now = int64(1700000000)
	db := r.DB()
	if err := db.Exec(`INSERT INTO node(id, name, secret, server_ip, port, created_time, updated_time, status) VALUES(10, 'n1', 's', '203.0.113.1', 0, ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	backup := &model.BackupData{Version: "1.0", Inbounds: &model.InboundsBackup{
		Kernels: []model.KernelBackup{
			{ID: 11, NodeID: 10, KernelType: "snell", Version: "latest", Enabled: 1, InstalledVersion: "5.0.1", CreatedTime: now},
			{ID: 12, NodeID: 10, KernelType: "singbox", Version: "latest", Enabled: 1, InstalledVersion: "1.12.0", CreatedTime: now},
		},
		Inbounds: []model.InboundBackup{
			{ID: 21, KernelID: 11, NodeID: 10, Name: "snell-on-snell-kernel", Protocol: "snell", Port: 21001, Enabled: 1, CreatedTime: now},
			{ID: 22, KernelID: 12, NodeID: 10, Name: "snell-on-singbox", Protocol: "snell", Port: 21002, Enabled: 1, CreatedTime: now},
			{ID: 23, KernelID: 0, NodeID: 0, Name: "imported-snell", Protocol: "snell", Port: 21003, Enabled: 1, CreatedTime: now,
				Extra: `{"type":"snell","server":"203.0.113.9","port":21003,"psk":"demo-psk","version":4}`},
		},
	}}
	result, err := r.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import inbounds: %v", err)
	}
	if result.InboundsImported != 3 {
		t.Fatalf("all three inbound rows must land, InboundsImported = %d (warnings: %v)", result.InboundsImported, result.ImportWarnings)
	}

	var k11 model.NodeKernel
	if err := db.First(&k11, 11).Error; err != nil {
		t.Fatalf("load snell kernel: %v", err)
	}
	if k11.Enabled != 0 || k11.InstalledVersion != "" {
		t.Fatalf("snell kernel must land disabled with installed version cleared, got enabled=%d installed=%q", k11.Enabled, k11.InstalledVersion)
	}
	var k12 model.NodeKernel
	if err := db.First(&k12, 12).Error; err != nil {
		t.Fatalf("load singbox kernel: %v", err)
	}
	if k12.Enabled != 1 || k12.InstalledVersion != "1.12.0" {
		t.Fatalf("singbox kernel must restore untouched, got enabled=%d installed=%q", k12.Enabled, k12.InstalledVersion)
	}

	enabledOf := func(id int64) int {
		t.Helper()
		var row model.NodeInbound
		if err := db.First(&row, id).Error; err != nil {
			t.Fatalf("load inbound %d: %v", id, err)
		}
		return row.Enabled
	}
	if got := enabledOf(21); got != 0 {
		t.Fatalf("snell inbound on snell kernel must land disabled, got enabled=%d", got)
	}
	if got := enabledOf(22); got != 0 {
		t.Fatalf("snell inbound on singbox kernel must land disabled, got enabled=%d", got)
	}
	if got := enabledOf(23); got != 1 {
		t.Fatalf("imported snell node (kept feature) must stay enabled, got enabled=%d", got)
	}
	if !b8WarningsContain(result.ImportWarnings, "Snell") ||
		!b8WarningsContain(result.ImportWarnings, "rmsnell_v1") {
		t.Fatalf("receipt must name the neutralized snell rows, got %v", result.ImportWarnings)
	}
	if !strings.Contains(strings.Join(result.ImportWarnings, "\n"), "snell-on-snell-kernel") {
		t.Fatalf("receipt must name the disabled inbound rows, got %v", result.ImportWarnings)
	}
}
