package app

import (
	"strings"
	"testing"
)

// W1/F-121: validatePostgresPassword must reject empty or well-known
// default PostgreSQL passwords at assembly time (app.New), while leaving
// SQLite and empty-DSN handling completely untouched.
func TestValidatePostgresPassword(t *testing.T) {
	tests := []struct {
		name    string
		dbType  string
		dsn     string
		wantErr bool
	}{
		// Rejections: password empty or a shipped default.
		{"default password rejected", "postgres", "postgres://flux_panel:flux_panel_change_me@db:5432/flux_panel", true},
		{"empty password rejected", "postgres", "postgres://u@h/db", true},
		{"literal postgres password rejected", "postgres", "postgres://u:postgres@h/db", true},
		{"postgresql alias normalized", "PostgreSQL", "postgres://u:flux_panel_change_me@h/db", true},
		{"keyword form default rejected", "postgres", "host=h user=u password=flux_panel_change_me dbname=d", true},

		// Passes: real passwords, including percent-escaped ones.
		{"strong password passes", "postgres", "postgres://u:Str0ng-Random.Passw0rd!@h:5432/db", false},
		{"url-escaped strong password passes", "postgres", "postgres://u:p%40ss%3Aw0rd@h/db", false},
		{"keyword form strong password passes", "postgres", "host=h user=u password=Str0ng-Random dbname=d", false},

		// Untouched paths: sqlite / empty type / empty DSN.
		{"sqlite unaffected", "sqlite", "", false},
		{"sqlite ignores dsn password", "sqlite", "postgres://u:flux_panel_change_me@h/db", false},
		{"empty db type unaffected", "", "", false},
		{"empty dsn defers to OpenPostgres error", "postgres", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePostgresPassword(tt.dbType, tt.dsn)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validatePostgresPassword(%q, %q) = nil, want rejection", tt.dbType, tt.dsn)
				}
				if !strings.Contains(err.Error(), "拒绝启动") {
					t.Fatalf("error %q does not contain 拒绝启动", err.Error())
				}
				if !strings.Contains(err.Error(), "POSTGRES_PASSWORD") {
					t.Fatalf("error %q does not explain how to fix it", err.Error())
				}
			} else if err != nil {
				t.Fatalf("validatePostgresPassword(%q, %q) = %v, want nil", tt.dbType, tt.dsn, err)
			}
		})
	}
}
