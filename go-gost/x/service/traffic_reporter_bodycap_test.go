package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// D2-03 regression: both v1/config report paths read the panel's
// response body with an unbounded ReadFrom, while the v2 path in
// the same package caps at 4096 via LimitReader. A misbehaving or
// hostile endpoint could stream an unbounded body into the root
// agent's memory (the live config path runs every 10 minutes).
// The read must be capped and overflow treated as a failed attempt,
// with the response text never embedded unbounded in errors.

func stubReportBody(t *testing.T, body string) {
	t.Helper()
	orig := reportDo
	t.Cleanup(func() { reportDo = orig })
	reportDo = func(_ context.Context, _ *http.Request, _ time.Duration) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}
}

func TestPostJSONWithFallbackRejectsOversizedResponseBody(t *testing.T) {
	stubReportBody(t, strings.Repeat("x", 64*1024))

	ok, err := postJSONWithFallback(
		context.Background(),
		[]string{"https://panel.example.com/flow/upload"},
		[]byte(`[]`), "GOST-Traffic-Reporter/1.0", 5*time.Second, nil,
	)
	if ok {
		t.Fatal("oversized response body must not be accepted")
	}
	if err == nil {
		t.Fatal("oversized response body must produce an error")
	}
	if len(err.Error()) > 4096 {
		t.Fatalf("error embeds the unbounded response body (%d bytes)", len(err.Error()))
	}

	ok, err = postJSONWithFallbackAndHeaders(
		context.Background(),
		[]string{"https://panel.example.com/flow/upload"},
		[]byte(`[]`), "GOST-Traffic-Reporter/1.0", 5*time.Second, nil, nil,
	)
	if ok || err == nil {
		t.Fatalf("AndHeaders: oversized body must fail, ok=%v err=%v", ok, err)
	}
	if len(err.Error()) > 4096 {
		t.Fatalf("AndHeaders: error embeds the unbounded response body (%d bytes)", len(err.Error()))
	}
}

func TestPostJSONWithFallbackStillAcceptsOK(t *testing.T) {
	stubReportBody(t, "ok")

	ok, err := postJSONWithFallback(
		context.Background(),
		[]string{"https://panel.example.com/flow/upload"},
		[]byte(`[]`), "GOST-Traffic-Reporter/1.0", 5*time.Second, nil,
	)
	if !ok || err != nil {
		t.Fatalf("plain ok body must still succeed, ok=%v err=%v", ok, err)
	}
}
