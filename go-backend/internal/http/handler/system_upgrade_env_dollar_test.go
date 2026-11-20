package handler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// —— R18-A2-04 · D-4 (b)：安全相关键遇 $ 即 fail-closed ——
// 变异自测：毒化必拒、干净必过、旧代码红新代码绿。行为面只走旧代
// 码已有的规划入口（planLegacyResourcePins / applyIdentityRename），
// 故本文件在旧代码上可编译、红是行为红（毒化未被拒）；探测函数
// 本身的单元测试见 system_upgrade_env_dollar_probe_test.go（旧代
// 码上编译红：函数不存在）。

func TestPlanLegacyResourcePinsRejectsDollarInterpolation(t *testing.T) {
	poisoned := []struct {
		name string
		env  map[string]string
		key  string
	}{
		{
			name: "sqlite volume ${VAR:-default}",
			env: map[string]string{
				envKeySQLiteVolume:   "${MY_VOL:-sqlite_data}",
				envKeyPostgresVolume: "postgres_data",
			},
			key: envKeySQLiteVolume,
		},
		{
			name: "postgres volume $VAR",
			env: map[string]string{
				envKeySQLiteVolume:   "sqlite_data",
				envKeyPostgresVolume: "$PG_VOL",
			},
			key: envKeyPostgresVolume,
		},
		{
			name: "network ${VAR}",
			env: map[string]string{
				envKeySQLiteVolume: "sqlite_data",
				envKeyNetwork:      "${MY_NET}",
			},
			key: envKeyNetwork,
		},
		{
			name: "compose project ${VAR}",
			env: map[string]string{
				envKeySQLiteVolume:   "sqlite_data",
				envKeyComposeProject: "${MY_PROJECT}",
			},
			key: envKeyComposeProject,
		},
	}
	for _, tc := range poisoned {
		t.Run(tc.name, func(t *testing.T) {
			_, err := planLegacyResourcePins(tc.env, keylessInspect())
			if err == nil {
				t.Fatalf("planLegacyResourcePins must reject %s with $ (old code: silently plans on the literal)", tc.key)
			}
			if !strings.Contains(err.Error(), tc.key) || !strings.Contains(err.Error(), "$") {
				t.Fatalf("error must name the offending key %s and the $ cause, got %v", tc.key, err)
			}
		})
	}

	t.Run("clean env plans normally", func(t *testing.T) {
		env := map[string]string{
			envKeySQLiteVolume:   "sqlite_data",
			envKeyPostgresVolume: "postgres_data",
			envKeyNetwork:        "gost-network",
			envKeyComposeProject: "flmx-panel",
		}
		if _, err := planLegacyResourcePins(env, keylessInspect()); err != nil {
			t.Fatalf("clean env must plan without error, got %v", err)
		}
	})

	t.Run("dollar in non-security key is untouched", func(t *testing.T) {
		env := map[string]string{
			envKeySQLiteVolume:  "sqlite_data",
			"JWT_SECRET":        "a$b$c",
			"POSTGRES_PASSWORD": "p$$w0rd",
		}
		if _, err := planLegacyResourcePins(env, keylessInspect()); err != nil {
			t.Fatalf("$ in password/secret values must not trip the gate, got %v", err)
		}
	})
}

func TestApplyIdentityRenameRejectsDollarContainer(t *testing.T) {
	writeFakeDocker(t, fakeDockerLocatePG)
	t.Setenv(envKeyInstance, "")
	t.Setenv(envKeyIdentityRename, "")
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	original := "JWT_SECRET=<redacted>\nPANEL_BACKEND_CONTAINER=${CUSTOM_BACKEND}\nPANEL_FRONTEND_CONTAINER=vite-frontend\nFLMX_VERSION=1.9.8\n"
	if err := os.WriteFile(envPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flux-panel-backend"}
	err := executor.applyIdentityRename(context.Background())
	if err == nil {
		t.Fatal("applyIdentityRename must reject a $ in PANEL_BACKEND_CONTAINER (old code: silently keeps/rewrites around it)")
	}
	if !strings.Contains(err.Error(), envKeyPanelBackendContainer) || !strings.Contains(err.Error(), "$") {
		t.Fatalf("error must name %s and the $ cause, got %v", envKeyPanelBackendContainer, err)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile(.env) error = %v", err)
	}
	if string(data) != original {
		t.Fatalf(".env must be byte-identical when the $ gate aborts:\n%s", string(data))
	}
}
