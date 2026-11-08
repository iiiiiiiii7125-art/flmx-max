package kernel

import (
	"regexp"
	"testing"
)

// R2-F2-A1 regression gate: every pinned SHA-256 in the catalog must be a
// full 64-char lowercase hex digest. A truncated pin once shipped in a
// pinned set; because the panel forwards the whole pin map and the agent
// validates every entry, that one bad value made the version
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
	// The realm download is listed explicitly as well: enumerate it by
	// name so no pinned set sits outside the gate.
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
	// nothing (today: realm 3 pins).
	if total < 3 {
		t.Fatalf("pinned census too small: got %d, want at least 3", total)
	}
}
