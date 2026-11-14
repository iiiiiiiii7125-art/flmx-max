package socket

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// F2-PORT-01：/proc/net 解析用真机行形（tcp/tcp6/udp 的表头＋字段
// 顺序与内核一致，端口为十六进制）：TCP 只认 LISTEN（0A），
// ESTABLISHED（01）与 TIME_WAIT（06）不算占用；UDP 任意表项即
// 已绑定。
const f2port01TCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1F91 00000000:0000 01 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 100 0 0 10 0
   2: 00000000:1B46 00000000:0000 06 00000000:00000000 00:00000000 00000000     0        0 12347 1 0000000000000000 100 0 0 10 0
garbage line without fields
   3: 00000000:ZZZZ 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12348 1 0000000000000000 100 0 0 10 0
`

const f2port01TCP6 = `  sl  local_address                         rem_address                         st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:01BB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12349 1 0000000000000000 100 0 0 10 0
`

const f2port01UDP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
   0: 00000000:14E9 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 12350 2 0000000000000000 0
   1: 00000000:270F 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 12351 2 0000000000000000 0
`

func TestParseProcNetListeningTCP(t *testing.T) {
	want := map[int]bool{8080: true, 8081: true, 6982: true}
	got := parseProcNetListening(f2port01TCP, true, want)
	if !reflect.DeepEqual(got, map[int]bool{8080: true}) {
		t.Fatalf("TCP LISTEN parse = %v, want only 8080 (ESTABLISHED 8081 and TIME_WAIT 6982 must not count)", got)
	}
}

func TestParseProcNetListeningTCP6(t *testing.T) {
	got := parseProcNetListening(f2port01TCP6, true, map[int]bool{443: true, 80: true})
	if !reflect.DeepEqual(got, map[int]bool{443: true}) {
		t.Fatalf("tcp6 LISTEN parse = %v, want only 443", got)
	}
}

func TestParseProcNetListeningUDP(t *testing.T) {
	got := parseProcNetListening(f2port01UDP, false, map[int]bool{5353: true, 1234: true})
	if !reflect.DeepEqual(got, map[int]bool{5353: true}) {
		t.Fatalf("UDP parse = %v, want only 5353 (any UDP entry is bound)", got)
	}
}

func TestScanProcNetPorts(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"tcp": f2port01TCP, "tcp6": f2port01TCP6, "udp": f2port01UDP} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tcp, udp, err := scanProcNetPorts(dir, map[int]bool{8080: true, 443: true, 5353: true})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !reflect.DeepEqual(tcp, []int{443, 8080}) {
		t.Fatalf("tcp = %v, want [443 8080]", tcp)
	}
	if !reflect.DeepEqual(udp, []int{5353}) {
		t.Fatalf("udp = %v, want [5353]", udp)
	}
}

func TestHandleCheckPortListening(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"tcp": f2port01TCP, "udp": f2port01UDP} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := procNetDir
	procNetDir = dir
	t.Cleanup(func() { procNetDir = old })

	resp, err := handleCheckPortListening(map[string]interface{}{
		"ranges": []map[string]interface{}{{"lo": 8000, "hi": 9000}, {"lo": 5353, "hi": 5353}},
	})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	got, ok := resp.(checkPortListeningResponse)
	if !ok {
		t.Fatalf("response type = %T", resp)
	}
	if !reflect.DeepEqual(got.Listening, []int{5353, 8080}) {
		t.Fatalf("listening = %v, want [5353 8080]", got.Listening)
	}
	if !reflect.DeepEqual(got.TCP, []int{8080}) || !reflect.DeepEqual(got.UDP, []int{5353}) {
		t.Fatalf("tcp = %v udp = %v, want [8080] / [5353]", got.TCP, got.UDP)
	}

	for _, bad := range []map[string]interface{}{
		{"ranges": []map[string]interface{}{}},
		{"ranges": []map[string]interface{}{{"lo": 9000, "hi": 8000}}},
		{"ranges": []map[string]interface{}{{"lo": 0, "hi": 80}}},
		{"ranges": []map[string]interface{}{{"lo": 80, "hi": 70000}}},
	} {
		if _, err := handleCheckPortListening(bad); err == nil {
			t.Fatalf("request %v must be rejected", bad)
		}
	}
}
