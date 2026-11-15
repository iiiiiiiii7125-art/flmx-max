package admission

import (
	"testing"

	"github.com/go-gost/x/config"
)

// R9-B-01: ParseAdmission must not panic when no default logger has
// been registered (logger.Default() is nil until SetDefault is called).
// This test deliberately does NOT call SetDefault, so it only
// exercises the nil-default path when it runs before any other test
// file registers a default logger; run it standalone
// (go test -run TestParseAdmissionWithoutDefaultLogger) to hit that path.
func TestParseAdmissionWithoutDefaultLogger(t *testing.T) {
	adm := ParseAdmission(&config.AdmissionConfig{Name: "test"})
	if adm == nil {
		t.Fatal("ParseAdmission returned nil")
	}
}
