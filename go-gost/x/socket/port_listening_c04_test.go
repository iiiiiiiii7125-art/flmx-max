package socket

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// C-04: CheckPortListening must also report ESTABLISHED sockets — pause
// acceptance has to prove "no established connections left", not just
// "listener gone". Fixture rows are real /proc/net shapes (see
// port_listening_test.go): 8080 LISTEN, 8081 ESTABLISHED, 6982 TIME_WAIT.
func TestParseProcNetEstablished(t *testing.T) {
	want := map[int]bool{8080: true, 8081: true, 6982: true}
	got := parseProcNetEstablished(f2port01TCP, want)
	if !reflect.DeepEqual(got, map[int]bool{8081: true}) {
		t.Fatalf("ESTABLISHED parse = %v, want only 8081 (LISTEN 8080 and TIME_WAIT 6982 must not count)", got)
	}
	if got := parseProcNetEstablished(f2port01TCP6, map[int]bool{443: true}); len(got) != 0 {
		t.Fatalf("tcp6 fixture has no ESTABLISHED rows, got %v", got)
	}
}

func TestHandleCheckPortListeningEstablished(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"tcp": f2port01TCP, "tcp6": f2port01TCP6, "udp": f2port01UDP} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := procNetDir
	procNetDir = dir
	t.Cleanup(func() { procNetDir = old })

	resp, err := handleCheckPortListening(map[string]interface{}{
		"ranges": []map[string]interface{}{{"lo": 8000, "hi": 9000}},
	})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	got, ok := resp.(checkPortListeningResponse)
	if !ok {
		t.Fatalf("response type = %T", resp)
	}
	if !reflect.DeepEqual(got.Established, []int{8081}) {
		t.Fatalf("established = %v, want [8081]", got.Established)
	}
	if !reflect.DeepEqual(got.Listening, []int{8080}) {
		t.Fatalf("listening = %v, want [8080]", got.Listening)
	}
}
