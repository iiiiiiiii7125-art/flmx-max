package socket

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestTuneStatusWireShapeCanonical pins the TuneStatus wire
// contract the panel frontend consumes (NodeTuneStatus):
// the reply must carry the canonical faces / congestion / qdisc
// fields AND the legacy flat fields, with identical values.
// Regression pin for the 2.1.0 defect where the reply was
// flat-only and every real 2.1.0 agent was misread by the
// frontend as an old (pre-four-face) agent.
func TestTuneStatusWireShapeCanonical(t *testing.T) {
	fakeProcSys(t, "reno cubic")

	assertShape := func(t *testing.T) map[string]interface{} {
		t.Helper()
		raw, err := json.Marshal(readTuneStatus())
		if err != nil {
			t.Fatalf("marshal tuneStatus: %v", err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal tuneStatus reply: %v", err)
		}
		// Flat fields stay on the wire (additive change).
		for _, k := range []string{
			"ipv4Priority", "bbr", "kernel", "nic",
			"congestionControl", "defaultQdisc", "rpsSockFlowEntries", "nofile",
		} {
			if _, ok := m[k]; !ok {
				t.Errorf("flat field %q missing from TuneStatus reply: %s", k, raw)
			}
		}
		// Canonical fields exist and mirror the flat values.
		faces, ok := m["faces"].(map[string]interface{})
		if !ok {
			t.Fatalf("canonical field \"faces\" missing or not an object: %s", raw)
		}
		pairs := map[string]string{
			"ipv4": "ipv4Priority", "bbr": "bbr", "kernel": "kernel", "nic": "nic",
		}
		for canon, flat := range pairs {
			cv, ok := faces[canon].(bool)
			if !ok {
				t.Errorf("faces.%s missing or not a bool: %s", canon, raw)
				continue
			}
			fv, _ := m[flat].(bool)
			if cv != fv {
				t.Errorf("faces.%s = %v, want same value as flat %s = %v", canon, cv, flat, fv)
			}
		}
		if m["congestion"] != m["congestionControl"] {
			t.Errorf("congestion = %v, want same value as congestionControl = %v",
				m["congestion"], m["congestionControl"])
		}
		if m["qdisc"] != m["defaultQdisc"] {
			t.Errorf("qdisc = %v, want same value as defaultQdisc = %v",
				m["qdisc"], m["defaultQdisc"])
		}
		return m
	}

	// Seeded defaults: cubic / fq_codel / rps 0 — all faces off.
	m := assertShape(t)
	if m["congestion"] != "cubic" || m["qdisc"] != "fq_codel" {
		t.Errorf("seeded headline values not reflected: congestion=%v qdisc=%v",
			m["congestion"], m["qdisc"])
	}
	if faces := m["faces"].(map[string]interface{}); faces["bbr"] != false || faces["nic"] != false {
		t.Errorf("seeded faces should be off, got %v", faces)
	}

	// Flip the live values the faces derive from: the canonical
	// fields must track the flat ones in the "on" direction too.
	writeSys := func(key, value string) {
		p := sysctlPath(key)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSys("net.ipv4.tcp_congestion_control", "bbr")
	writeSys("net.core.default_qdisc", "fq")
	writeSys("net.core.rps_sock_flow_entries", "32768")
	m = assertShape(t)
	faces := m["faces"].(map[string]interface{})
	if faces["bbr"] != true || faces["nic"] != true {
		t.Errorf("faces should track applied values, got %v", faces)
	}
	if m["congestion"] != "bbr" || m["qdisc"] != "fq" {
		t.Errorf("canonical headline values should track, got congestion=%v qdisc=%v",
			m["congestion"], m["qdisc"])
	}
}
