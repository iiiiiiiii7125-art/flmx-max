package repo

import "testing"

// TestNodeTunnelTLSReadyRoundTrip pins the R18-F1-01 readiness store:
// mark → get → batch list → clear → get. A missing node reads as
// not-ready (nil state), never as an error.
func TestNodeTunnelTLSReadyRoundTrip(t *testing.T) {
	r, err := Open(t.TempDir() + "/tunnel-tls.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if st, err := r.GetNodeTunnelTLS(7); err != nil || st != nil {
		t.Fatalf("unissued node: state=%v err=%v, want nil/nil", st, err)
	}
	if err := r.MarkNodeTunnelTLSReady(7, "caaaa", "cbbbb", "node-7.flmx.internal", 1000); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	st, err := r.GetNodeTunnelTLS(7)
	if err != nil || st == nil {
		t.Fatalf("get after mark: state=%v err=%v", st, err)
	}
	if st.Ready != 1 || st.CASha256 != "caaaa" || st.CertSha256 != "cbbbb" || st.ServerName != "node-7.flmx.internal" || st.IssuedAt != 1000 || st.VerifiedAt != 1000 {
		t.Fatalf("stored state = %+v, want ready fingerprints + timestamps", st)
	}
	// Re-mark with new fingerprints overwrites (re-issue path).
	if err := r.MarkNodeTunnelTLSReady(7, "caaaa2", "cbbbb2", "node-7.flmx.internal", 2000); err != nil {
		t.Fatalf("re-mark ready: %v", err)
	}
	st, _ = r.GetNodeTunnelTLS(7)
	if st.CASha256 != "caaaa2" || st.IssuedAt != 2000 {
		t.Fatalf("re-mark did not overwrite: %+v", st)
	}
	ready, err := r.ListNodeTunnelTLSReady([]int64{7, 8, 9})
	if err != nil {
		t.Fatalf("list ready: %v", err)
	}
	if !ready[7] || ready[8] || ready[9] {
		t.Fatalf("ready set = %v, want only node 7", ready)
	}
	if ready, err := r.ListNodeTunnelTLSReady(nil); err != nil || len(ready) != 0 {
		t.Fatalf("empty list = %v err=%v, want empty/nil", ready, err)
	}
	if err := r.ClearNodeTunnelTLSReady(7); err != nil {
		t.Fatalf("clear ready: %v", err)
	}
	st, _ = r.GetNodeTunnelTLS(7)
	if st == nil || st.Ready != 0 {
		t.Fatalf("after clear: %+v, want row kept with ready=0", st)
	}
	if st.CertSha256 != "cbbbb2" {
		t.Fatalf("clear must keep last fingerprints for forensics: %+v", st)
	}
	ready, _ = r.ListNodeTunnelTLSReady([]int64{7})
	if ready[7] {
		t.Fatal("cleared node must not read as ready")
	}
}
