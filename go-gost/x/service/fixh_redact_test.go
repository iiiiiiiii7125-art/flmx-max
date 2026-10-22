package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// D1-01 (agent half): report URLs embed the node secret as a query
// parameter. Every log line and aggregated error must mask it (the
// WebSocket side already sanitizes; the HTTP report side did not).
func TestSanitizeReportURLMasksSecret(t *testing.T) {
	in := "https://panel.example.com/flow/upload?secret=TOPSECRET-VALUE"
	out := sanitizeReportURL(in)
	if strings.Contains(out, "TOPSECRET-VALUE") {
		t.Fatalf("sanitized URL still contains the secret: %s", out)
	}
	if !strings.Contains(out, "secret=***") {
		t.Fatalf("expected masked secret parameter, got %s", out)
	}
	if !strings.Contains(out, "panel.example.com/flow/upload") {
		t.Fatalf("sanitization must keep the URL identifiable: %s", out)
	}
}

func TestPostJSONWithFallbackDoesNotLeakSecretInErrors(t *testing.T) {
	orig := reportDo
	defer func() { reportDo = orig }()
	reportDo = func(_ context.Context, _ *http.Request, _ time.Duration) (*http.Response, error) {
		return nil, errors.New("dial failed")
	}

	url := "https://panel.example.com/flow/upload?secret=TOPSECRET-VALUE"
	_, err := postJSONWithFallback(context.Background(), []string{url}, []byte("{}"), "test-agent", time.Second, nil)
	if err == nil {
		t.Fatal("expected an error when the transport fails")
	}
	if strings.Contains(err.Error(), "TOPSECRET-VALUE") {
		t.Fatalf("aggregated error leaked the node secret: %v", err)
	}
}

func TestPostJSONWithFallbackAndHeadersDoesNotLeakSecretInErrors(t *testing.T) {
	orig := reportDo
	defer func() { reportDo = orig }()
	reportDo = func(_ context.Context, _ *http.Request, _ time.Duration) (*http.Response, error) {
		return nil, errors.New("dial failed")
	}

	url := "https://panel.example.com/flow/upload?secret=TOPSECRET-VALUE"
	_, err := postJSONWithFallbackAndHeaders(context.Background(), []string{url}, []byte("{}"), "test-agent", time.Second, nil, map[string]string{"X-Flmx-Seq": "1"})
	if err == nil {
		t.Fatal("expected an error when the transport fails")
	}
	if strings.Contains(err.Error(), "TOPSECRET-VALUE") {
		t.Fatalf("aggregated error leaked the node secret: %v", err)
	}
}
