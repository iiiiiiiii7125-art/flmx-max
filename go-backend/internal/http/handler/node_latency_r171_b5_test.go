package handler

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R171-B5 H-171-01 (save-side half): the save-side validator this fix
// mirrors must give the same verdict as the restore-side validator in
// TestR171B5ValidateLatencyTargetsConfigValue on every corpus case.
// The save side already behaved this way on the baseline; this test
// locks the corpus so the two copies cannot drift apart.
func TestR171B5LatencyTargetsSaveSideCorpus(t *testing.T) {
	many := func(n int, pinned int) string {
		parts := make([]string, 0, n)
		for i := 0; i < n; i++ {
			p := false
			if i < pinned {
				p = true
			}
			parts = append(parts, fmt.Sprintf(`{"name":"t%d","host":"8.8.8.8","port":443,"enabled":true,"pinned":%v}`, i, p))
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"badjson", "not-json", true},
		{"badhost", `[{"name":"x","host":"bad host!!","port":0,"enabled":true}]`, true},
		{"object-not-array", `{"name":"x"}`, true},
		{"empty-name", `[{"name":"","host":"8.8.8.8","port":443,"enabled":true}]`, true},
		{"long-name", `[{"name":"012345678901234567890","host":"8.8.8.8","port":443,"enabled":true}]`, true},
		{"dup-names", `[{"name":"x","host":"8.8.8.8","port":443,"enabled":true},{"name":"x","host":"8.8.4.4","port":443,"enabled":true}]`, true},
		{"too-many", many(31, 0), true},
		{"too-many-pinned", many(4, 4), true},
		{"bad-port-zero", `[{"name":"x","host":"8.8.8.8","port":0,"enabled":true}]`, true},
		{"bad-port-over", `[{"name":"x","host":"8.8.8.8","port":70000,"enabled":true}]`, true},
		{"empty-label-host", `[{"name":"x","host":"a..b","port":443,"enabled":true}]`, true},
		{"loopback", `[{"name":"x","host":"127.0.0.1","port":443,"enabled":true}]`, true},
		{"localhost", `[{"name":"x","host":"localhost","port":443,"enabled":true}]`, true},
		// R2-B1 R2-H-01: 13 个确定性政策拒字面量（H 域 10 格 + L 域 7 格
		// 并集）——保存侧本就拒收，本表锁定该 verdict，防恢复侧修偏。
		{"policy-metadata-v4", `[{"name":"x","host":"169.254.169.254","port":443,"enabled":true}]`, true},
		{"policy-metadata-ali", `[{"name":"x","host":"100.100.100.200","port":443,"enabled":true}]`, true},
		{"policy-linklocal-v4", `[{"name":"x","host":"169.254.0.23","port":443,"enabled":true}]`, true},
		{"policy-linklocal-v6", `[{"name":"x","host":"fe80::1","port":443,"enabled":true}]`, true},
		{"policy-linklocal-v6-bracket", `[{"name":"x","host":"[fe80::1]","port":443,"enabled":true}]`, true},
		{"policy-multicast", `[{"name":"x","host":"224.0.0.1","port":443,"enabled":true}]`, true},
		{"policy-reserved-benchmark", `[{"name":"x","host":"198.18.1.1","port":443,"enabled":true}]`, true},
		{"policy-reserved-240", `[{"name":"x","host":"240.1.2.3","port":443,"enabled":true}]`, true},
		{"policy-metadata-4in6", `[{"name":"x","host":"::ffff:169.254.169.254","port":443,"enabled":true}]`, true},
		{"policy-metadata-v6", `[{"name":"x","host":"fd00:ec2::254","port":443,"enabled":true}]`, true},
		{"policy-nat64-local", `[{"name":"x","host":"64:ff9b:1::1","port":443,"enabled":true}]`, true},
		{"policy-reserved-240-zero", `[{"name":"x","host":"240.0.0.1","port":443,"enabled":true}]`, true},
		{"policy-reserved-benchmark-zero", `[{"name":"x","host":"198.18.0.1","port":443,"enabled":true}]`, true},
		{"policy-reserved-zero-net", `[{"name":"x","host":"0.0.0.1","port":443,"enabled":true}]`, true},
		// 放行对照：私网/CGNAT 与恢复侧同口径放行（严禁过修）。
		{"allow-private", `[{"name":"x","host":"10.0.0.1","port":443,"enabled":true}]`, false},
		{"allow-cgnat", `[{"name":"x","host":"100.64.0.1","port":443,"enabled":true}]`, false},
		{"scheme-like-host", `[{"name":"x","host":"https://example.com","port":443,"enabled":true}]`, true},
		{"legal", `[{"name":"电信","host":"8.8.8.8","port":443,"enabled":true}]`, false},
		{"legal-max", many(30, 3), false},
		{"empty", "", false},
		{"null", "null", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeLatencyTargetsConfig(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("normalizeLatencyTargetsConfig(%q) err = %v, wantErr = %v", tc.raw, err, tc.wantErr)
			}
		})
	}
}

// TestR171B5LatencyTargetsRestoredValueParsesAsPool closes the H-171-01
// chain at the read side: a legally restored latency_targets value must
// parse into a non-empty latency pool (measurements keep running);
// the pre-fix hazard shape — a bad value stored — still yields an
// empty pool at read time, which is why restore must keep it out.
func TestR171B5LatencyTargetsRestoredValueParsesAsPool(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	legal := `[{"name":"电信","host":"8.8.8.8","port":443,"enabled":true}]`
	if err := r.UpsertConfig(latencyTargetsConfigKey, legal, time.Now().UnixMilli()); err != nil {
		t.Fatalf("upsert legal targets: %v", err)
	}
	pool := h.latencyPool()
	if len(pool) != 1 || pool[0].Name != "电信" || pool[0].Host != "8.8.8.8" || pool[0].Port != 443 {
		t.Fatalf("latencyPool() = %+v, want one target 电信/8.8.8.8:443", pool)
	}

	if err := r.UpsertConfig(latencyTargetsConfigKey, "not-json", time.Now().UnixMilli()); err != nil {
		t.Fatalf("upsert bad targets: %v", err)
	}
	if pool := h.latencyPool(); len(pool) != 0 {
		t.Fatalf("latencyPool() with bad stored value = %+v, want empty (fail-safe read side)", pool)
	}
}
