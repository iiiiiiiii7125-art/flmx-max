package socket

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A-NEW: the pause kill decision must count every live/draining TCP
// state, not just ESTABLISHED. A bulk relay flow is in FIN-WAIT-2 /
// CLOSE-WAIT by the time the pause lands, with megabytes still
// buffered in the peer's Recv-Q — counting only ESTABLISHED let the
// pause skip the kill entirely and the download complete.
// Fixture rows are real /proc/net shapes: 8080 LISTEN (0A), 8081
// ESTABLISHED (01), 8082 CLOSE_WAIT (08), 8083 FIN_WAIT2 (05), 8084
// TIME_WAIT (06), 8085 FIN_WAIT1 (04), 8086 LAST_ACK (09), 8087
// CLOSING (0B).
const anewTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1F91 00000000:0000 01 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 100 0 0 10 0
   2: 0100007F:1F92 00000000:0000 08 00000000:001786C1 00:00000000 00000000     0        0 12347 1 0000000000000000 100 0 0 10 0
   3: 0100007F:1F93 00000000:0000 05 00000000:00000000 00:00000000 00000000     0        0 12348 1 0000000000000000 100 0 0 10 0
   4: 00000000:1F94 00000000:0000 06 00000000:00000000 00:00000000 00000000     0        0 12349 1 0000000000000000 100 0 0 10 0
   5: 0100007F:1F95 00000000:0000 04 00000000:00000000 00:00000000 00000000     0        0 12350 1 0000000000000000 100 0 0 10 0
   6: 0100007F:1F96 00000000:0000 09 00000000:00000000 00:00000000 00000000     0        0 12351 1 0000000000000000 100 0 0 10 0
   7: 0100007F:1F97 00000000:0000 0B 00000000:00000000 00:00000000 00000000     0        0 12352 1 0000000000000000 100 0 0 10 0
`

func TestParseProcNetConnected(t *testing.T) {
	want := map[int]bool{8080: true, 8081: true, 8082: true, 8083: true, 8084: true, 8085: true, 8086: true, 8087: true}
	got := parseProcNetConnected(anewTCP, want)
	wantPorts := map[int]bool{8081: true, 8082: true, 8083: true, 8085: true, 8086: true, 8087: true}
	if !reflect.DeepEqual(got, wantPorts) {
		t.Fatalf("connected parse = %v, want %v (LISTEN 8080 and TIME_WAIT 8084 must not count)", got, wantPorts)
	}
	// ESTABLISHED-only still sees just 8081 — the old predicate is
	// unchanged and the C-04 probe keeps its meaning.
	if got := parseProcNetEstablished(anewTCP, want); !reflect.DeepEqual(got, map[int]bool{8081: true}) {
		t.Fatalf("established parse = %v, want only 8081", got)
	}
}

func TestScanProcNetConnected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tcp"), []byte(anewTCP), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tcp6"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := scanProcNetConnected(dir, map[int]bool{8080: true, 8081: true, 8082: true, 8083: true, 8084: true, 8085: true, 8086: true, 8087: true})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !reflect.DeepEqual(got, []int{8081, 8082, 8083, 8085, 8086, 8087}) {
		t.Fatalf("scan = %v, want [8081 8082 8083 8085 8086 8087]", got)
	}
}
