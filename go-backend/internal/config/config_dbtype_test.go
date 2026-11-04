package config

import "testing"

// R1-01-M2：DB_TYPE 归一单点——接受集与后端启动判定、脚本面
// normalize_db_type 逐字同集，别名 postgresql 折为 postgres。
func TestNormalizeDBType(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"", ""},
		{"sqlite", "sqlite"},
		{"SQLite", "sqlite"},
		{" sqlite ", "sqlite"},
		{"postgres", "postgres"},
		{"postgresql", "postgres"},
		{"PostgreSQL", "postgres"},
		{" PostgreSQL ", "postgres"},
		{"POSTGRES", "postgres"},
		{"mysql", "mysql"},
	} {
		if got := NormalizeDBType(tc.in); got != tc.want {
			t.Fatalf("NormalizeDBType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDBTypeIsPostgres(t *testing.T) {
	for _, v := range []string{"postgres", "postgresql", "PostgreSQL", " postgresql "} {
		if !DBTypeIsPostgres(v) {
			t.Fatalf("DBTypeIsPostgres(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "sqlite", "mysql", "postgres2"} {
		if DBTypeIsPostgres(v) {
			t.Fatalf("DBTypeIsPostgres(%q) = true, want false", v)
		}
	}
}
