package socket

import (
	"os"
	"strings"
	"testing"
)

// machineBootID must be stable within the process (it is resolved
// once) and, on Linux, equal the kernel's boot_id when that file is
// readable — the panel keys NIC counter regression semantics on this
// identity, so it must identify the boot and nothing else.
func TestMachineBootIDStable(t *testing.T) {
	first := machineBootID()
	if second := machineBootID(); second != first {
		t.Fatalf("boot id must be stable across calls: %q vs %q", first, second)
	}
	if data, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
		if want := strings.TrimSpace(string(data)); first != want {
			t.Fatalf("boot id = %q, want kernel boot_id %q", first, want)
		}
	}
}
