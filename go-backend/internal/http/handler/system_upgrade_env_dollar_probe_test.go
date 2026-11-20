package handler

import (
	"testing"
)

// —— R18-A2-04 · D-4 (b)：findEnvDollarValue 探测单元测试 ——
// 本文件直接引用新探测函数，旧代码上编译红（undefined:
// findEnvDollarValue）；行为红见 system_upgrade_env_dollar_test.go。

func TestFindEnvDollarValue(t *testing.T) {
	// 9 个安全相关键逐个毒化：都须被点名。
	poisoned := []struct {
		key   string
		value string
	}{
		{envKeySQLiteVolume, "${MY_VOL:-sqlite_data}"},
		{envKeyPostgresVolume, "$PG_VOL"},
		{envKeyNetwork, "${MY_NET}"},
		{envKeyComposeProject, "${MY_PROJECT}"},
		{envKeyPanelBackendContainer, "${CUSTOM_BACKEND}"},
		{envKeyPanelFrontendContainer, "$FRONTEND"},
		{envKeyPanelPostgresContainer, "${PG_CONTAINER}"},
		{envKeyBackendPort, "${BACKEND_PORT}"},
		{envKeyFrontendPort, "$FRONTEND_PORT"},
	}
	for _, tc := range poisoned {
		env := map[string]string{tc.key: tc.value}
		key, value, ok := findEnvDollarValue(env)
		if !ok {
			t.Fatalf("findEnvDollarValue must flag %s=%q", tc.key, tc.value)
		}
		if key != tc.key || value != tc.value {
			t.Fatalf("findEnvDollarValue = (%q, %q), want (%q, %q)", key, value, tc.key, tc.value)
		}
	}

	// 干净值、空值、缺键：不触发。
	clean := map[string]string{
		envKeySQLiteVolume:           "sqlite_data",
		envKeyPostgresVolume:         "postgres_data",
		envKeyNetwork:                "gost-network",
		envKeyComposeProject:         "flmx-panel",
		envKeyPanelBackendContainer:  "flmx-panel-backend",
		envKeyPanelFrontendContainer: "flmx-panel-frontend",
		envKeyPanelPostgresContainer: "flmx-panel-postgres",
		envKeyBackendPort:            "6365",
		envKeyFrontendPort:           "6366",
	}
	if key, value, ok := findEnvDollarValue(clean); ok {
		t.Fatalf("clean env must not trip the probe, got (%q, %q)", key, value)
	}
	if _, _, ok := findEnvDollarValue(map[string]string{envKeySQLiteVolume: ""}); ok {
		t.Fatal("empty value must not trip the probe")
	}
	if _, _, ok := findEnvDollarValue(map[string]string{}); ok {
		t.Fatal("missing keys must not trip the probe")
	}

	// 非安全键含 $ 不探测：口令/密钥/模式键/实例 id 均不在键表。
	nonSecurity := map[string]string{
		envKeySQLiteVolume:  "sqlite_data",
		"JWT_SECRET":        "a$b$c",
		"POSTGRES_PASSWORD": "p$$w0rd",
		envKeyDBType:        "${DB_TYPE}",
		envKeyInstance:      "${INSTANCE}",
	}
	if key, value, ok := findEnvDollarValue(nonSecurity); ok {
		t.Fatalf("$ outside the security key set must not trip the probe, got (%q, %q)", key, value)
	}
}

func TestFindEnvDollarValueFromParsedEnv(t *testing.T) {
	// 从原始 .env 文本经 parseEnvFileValues 解析后再探测：未引号
	// ${...}、单引号字面 ${...}、双引号 \$ 转义后的字面 $ 都须被
	// 拒——解析值含 $ 即拒，不再分转义与插值（合法资源名不可能含
	// $）；口令里的 $ 不触发。
	cases := []struct {
		name string
		data string
		want bool
	}{
		{
			name: "unquoted interpolation",
			data: "FLMX_SQLITE_VOLUME=${MY_VOL:-sqlite_data}\n",
			want: true,
		},
		{
			name: "double-quoted interpolation",
			data: "FLMX_SQLITE_VOLUME=\"${MY_VOL}\"\n",
			want: true,
		},
		{
			name: "single-quoted literal still rejected",
			data: "FLMX_SQLITE_VOLUME='${MY_VOL}'\n",
			want: true,
		},
		{
			name: "double-quoted escaped dollar expands to literal dollar",
			data: "FLMX_SQLITE_VOLUME=\"a\\$b\"\n",
			want: true,
		},
		{
			name: "clean volume with dollar password elsewhere",
			data: "FLMX_SQLITE_VOLUME=sqlite_data\nPOSTGRES_PASSWORD='p$$w0rd'\n",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := parseEnvFileValues([]byte(tc.data))
			_, _, ok := findEnvDollarValue(env)
			if ok != tc.want {
				t.Fatalf("findEnvDollarValue(parse(%q)) = %v, want %v", tc.data, ok, tc.want)
			}
		})
	}
}
