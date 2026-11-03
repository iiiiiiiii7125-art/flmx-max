package kernel

import (
	"regexp"
	"testing"
)

// R2-F2-A1 regression gate: every pinned SHA-256 in the catalog must be a
// full 64-char lowercase hex digest. The Snell 6.0.0 aarch64 pin once
// shipped truncated to 57 chars; because the panel forwards the whole pin
// map and the agent validates every entry, that one bad value made 6.0.0
// uninstallable on every architecture. This test enumerates the pinned
// set mechanically (catalog downloads + the realm download) so any future
// pin is covered the moment it is added.
func TestPinnedHashesAreFullSHA256Hex(t *testing.T) {
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)

	downloads := map[*Download]string{}
	for _, info := range Catalog() {
		if info.Download != nil {
			downloads[info.Download] = info.Type
		}
	}
	// ShadowTLS is attached to Snell installs outside the catalog, and the
	// realm download is listed explicitly as well: enumerate both by name
	// so no pinned set sits outside the gate.
	downloads[shadowTLSDownload] = "shadow-tls"
	downloads[realmDownload] = TypeRealm

	total := 0
	for dl, owner := range downloads {
		for version, sums := range dl.Pinned {
			if len(sums) == 0 {
				t.Errorf("%s %s: pinned set has no arch hashes", owner, version)
			}
			for arch, sum := range sums {
				total++
				if !hex64.MatchString(sum) {
					t.Errorf("%s %s %s: pinned sha256 %q is not 64-char lowercase hex (len %d)", owner, version, arch, sum, len(sum))
				}
			}
		}
	}
	// Census floor: the enumeration itself must not silently shrink to
	// nothing (today: Snell 8 + ShadowTLS 3 + realm 3 = 14 pins).
	if total < 14 {
		t.Fatalf("pinned census too small: got %d, want at least 14", total)
	}

	// The repaired value itself, verified against two independent
	// downloads of the official artifact (dl.nssurge.com) whose amd64
	// sibling matches its long-standing pin.
	sums, ok := snellDownload.Pinned["6.0.0"]
	if !ok {
		t.Fatal("snell 6.0.0 pinned set missing")
	}
	const wantAarch64 = "a0b2915cbc77dc3baf8fa069e741c20808d8a10c3a8a93e709a0a580645c3bd7"
	if got := sums["aarch64"]; got != wantAarch64 {
		t.Errorf("snell 6.0.0 aarch64 pin = %q, want verified %q", got, wantAarch64)
	}
}
