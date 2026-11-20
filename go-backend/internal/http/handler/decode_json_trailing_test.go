package handler

import (
	"io"
	"strings"
	"testing"
)

// OPEN-11: decodeJSON did a single decoder.Decode and never looked
// past the first JSON value, so a body like `{"a":1} garbage` or
// `{"a":1}{"a":2}` decoded cleanly while the tail was silently
// dropped — the handler acted on a request the client never fully
// sent (and a smuggled second value was ignored). The trailing-data
// check in decodeJSONWithLimit closes the whole call surface (97
// decodeJSON sites + the one direct decodeJSONWithLimit caller) in
// one place. decodeBackupImportRequest keeps its own decoder and
// is deliberately untouched (owned by another lane).

type decodeTrailingProbe struct {
	A int `json:"a"`
}

func TestDecodeJSONTrailingGarbageRejected(t *testing.T) {
	var out decodeTrailingProbe
	err := decodeJSON(io.NopCloser(strings.NewReader(`{"a":1} garbage`)), &out)
	if err == nil {
		t.Fatal("body with trailing garbage must be rejected, got nil error")
	}
}

func TestDecodeJSONTrailingSecondValueRejected(t *testing.T) {
	var out decodeTrailingProbe
	err := decodeJSON(io.NopCloser(strings.NewReader(`{"a":1}{"a":2}`)), &out)
	if err == nil {
		t.Fatal("body with a second JSON value must be rejected, got nil error")
	}
}

func TestDecodeJSONTrailingWhitespaceAccepted(t *testing.T) {
	var out decodeTrailingProbe
	err := decodeJSON(io.NopCloser(strings.NewReader("{\"a\":1}\n  \n")), &out)
	if err != nil {
		t.Fatalf("trailing whitespace/newline must be accepted, got err %v", err)
	}
	if out.A != 1 {
		t.Fatalf("decoded A = %d, want 1", out.A)
	}
}

func TestDecodeJSONCleanBodyAccepted(t *testing.T) {
	var out decodeTrailingProbe
	err := decodeJSON(io.NopCloser(strings.NewReader(`{"a":7}`)), &out)
	if err != nil {
		t.Fatalf("clean body must be accepted, got err %v", err)
	}
	if out.A != 7 {
		t.Fatalf("decoded A = %d, want 7", out.A)
	}
}
