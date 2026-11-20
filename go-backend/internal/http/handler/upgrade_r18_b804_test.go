package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

func TestR18B804NormalizeAgentVersion(t *testing.T) {
	cases := map[string]string{
		"v1.3.5":   "1.3.5",
		"1.3.5":    "1.3.5",
		"  v1.3.5": "1.3.5",
		"v1.3.5  ": "1.3.5",
	}
	for in, want := range cases {
		if got := normalizeAgentVersion(in); got != want {
			t.Fatalf("normalizeAgentVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// R18-B8-04 (M-2): nodeUpgrade accepted a "v"-prefixed version and
// passed it verbatim to preheat (ghMirrorReleaseSHA256 then 404s, so
// the expected-hash anchor is silently lost) and to the agent (whose
// download URL 404s the same way), while the version comparison in
// the same handler strips the "v" — one version, two calibres. The
// panel must normalize at the entry, so the mirror only ever sees
// the bare tag.
//
// The test drives nodeUpgrade end to end with a recording mirror:
// the node is offline, so dispatch fails after preheat — the
// recorded upstream paths are the assertion.
func TestR18B804NodeUpgradePreheatUsesBareTag(t *testing.T) {
	paths := recordB804MirrorPaths(t)

	r, err := repo.Open(t.TempDir() + "/b804.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, port, created_time, status, version) VALUES('b804-node', 's804', '198.51.100.41', '1000-2000', ?, 1, '1.3.4')`, now).Error; err != nil {
		t.Fatal(err)
	}
	nodeID := mustLastInsertID(t, r, "node")

	out := decodeCode(t, postJSONToHandler(t, h.nodeUpgrade, map[string]interface{}{"id": nodeID, "version": "v1.3.5"}).Body.String())
	if out.Code == 0 {
		t.Fatalf("offline node upgrade must fail dispatch, got %+v", out)
	}
	assertB804MirrorPathsBare(t, paths)
}

func TestR18B804NodeBatchUpgradePreheatUsesBareTag(t *testing.T) {
	paths := recordB804MirrorPaths(t)

	r, err := repo.Open(t.TempDir() + "/b804-batch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, port, created_time, status, version) VALUES('b804-node', 's804', '198.51.100.41', '1000-2000', ?, 1, '1.3.4')`, now).Error; err != nil {
		t.Fatal(err)
	}
	nodeID := mustLastInsertID(t, r, "node")

	out := decodeCode(t, postJSONToHandler(t, h.nodeBatchUpgrade, map[string]interface{}{"ids": []int64{nodeID}, "version": "v1.3.5"}).Body.String())
	if out.Code != 0 {
		t.Fatalf("batch upgrade request must be accepted, got %+v", out)
	}
	assertB804MirrorPathsBare(t, paths)
}

// recordB804MirrorPaths swaps the mirror upstream for a recording
// stub that answers every .sha256 fetch with a valid hash, and
// returns a snapshot function for the requested upstream paths.
func recordB804MirrorPaths(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			fmt.Fprint(w, panelSourceTestHashAmd64+"  gost-amd64\n")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	swapGHMirrorGitHubBase(t, srv.URL)
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// assertB804MirrorPathsBare requires that preheat fetched the bare
// tag for both architectures and never requested a "v"-prefixed tag.
func assertB804MirrorPathsBare(t *testing.T, snapshot func() []string) {
	t.Helper()
	got := snapshot()
	found := false
	for _, p := range got {
		if strings.Contains(p, "/releases/download/v1.3.5/") {
			t.Fatalf("preheat requested a v-prefixed tag: %s (all paths %v)", p, got)
		}
		if strings.Contains(p, "/releases/download/1.3.5/gost-") {
			found = true
		}
	}
	if !found {
		t.Fatalf("preheat never requested the bare tag; paths=%v", got)
	}
}
