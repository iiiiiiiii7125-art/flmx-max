package repo

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
	"go-backend/internal/store/model"
)

// OPEN-17: an imported `{}` credential unmarshals cleanly with an
// empty ID and an empty public key, and used to land as a dead row —
// never usable at login, and via the OnConflict on id able to
// overwrite a good row with the same id. The import must skip such
// rows with the same skip + per-row warning as unparseable ones.

func TestValidImportedCredential(t *testing.T) {
	cases := []struct {
		name string
		cred webauthn.Credential
		want bool
	}{
		{"both set is valid", webauthn.Credential{ID: []byte("hello"), PublicKey: []byte("pub")}, true},
		{"empty ID is dead", webauthn.Credential{PublicKey: []byte("pub")}, false},
		{"empty public key is dead", webauthn.Credential{ID: []byte("hello")}, false},
		{"empty credential is dead", webauthn.Credential{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validImportedCredential(tc.cred); got != tc.want {
				t.Fatalf("validImportedCredential = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestImportPasskeysSkipsEmptyCredential(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "passkey-import.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	goodJSON := `{"id":"aGVsbG8=","publicKey":"cHVi"}`
	otherJSON := `{"id":"b3RoZXI=","publicKey":"cHViMg=="}`
	rows := []model.PasskeyBackup{
		{ID: "keep-me", UserID: 1, Name: "good", CredentialJSON: goodJSON},
		{ID: "keep-me", UserID: 1, Name: "dead-overwrite", CredentialJSON: `{}`},
		{ID: "dead-empty", UserID: 1, Name: "empty", CredentialJSON: `{}`},
		{ID: "dead-nokey", UserID: 1, Name: "no-key", CredentialJSON: `{"id":"aGVsbG8=","publicKey":""}`},
		{ID: "dead-noid", UserID: 1, Name: "no-id", CredentialJSON: `{"publicKey":"cHVi"}`},
		{ID: "other-good", UserID: 1, Name: "other", CredentialJSON: otherJSON},
	}
	rep := &importReport{skipped: map[string]int{}}
	count, err := importPasskeys(r.db, rows, rep)
	if err != nil {
		t.Fatalf("importPasskeys: %v", err)
	}
	if count != 2 {
		t.Fatalf("imported count = %d, want 2 (only the two usable credentials)", count)
	}
	if got := rep.skipped["passkeys"]; got != 4 {
		t.Fatalf("skipped passkeys = %d, want 4 (the empty-id/key rows)", got)
	}
	for _, id := range []string{"keep-me", "dead-empty", "dead-nokey", "dead-noid"} {
		found := false
		for _, w := range rep.warnings {
			if strings.Contains(w, id) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("warnings must name skipped passkey %q, got %v", id, rep.warnings)
		}
	}

	var kept model.Passkey
	if err := r.db.Where("id = ?", "keep-me").First(&kept).Error; err != nil {
		t.Fatalf("good keep-me row must survive: %v", err)
	}
	if kept.CredentialJSON != goodJSON {
		t.Fatalf("keep-me credential was overwritten by the dead row: got %q, want %q", kept.CredentialJSON, goodJSON)
	}
	var total int64
	if err := r.db.Model(&model.Passkey{}).Count(&total).Error; err != nil {
		t.Fatalf("count passkeys: %v", err)
	}
	if total != 2 {
		t.Fatalf("stored passkeys = %d, want 2 (no dead row may land)", total)
	}
}
