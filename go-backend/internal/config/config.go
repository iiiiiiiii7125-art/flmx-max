package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"strings"
)

type Config struct {
	Addr         string
	DBType       string
	DBPath       string
	DatabaseURL  string
	JWTSecret    string
	JWTEphemeral bool // true when JWTSecret was auto-generated (sessions won't survive restart)
	LogDir       string
}

func FromEnv() Config {
	cfg := Config{
		Addr:        getEnv("SERVER_ADDR", ":6365"),
		DBType:      getEnv("DB_TYPE", "sqlite"),
		DBPath:      getEnv("DB_PATH", "/app/data/gost.db"),
		DatabaseURL: getEnv("DATABASE_URL", ""),
		JWTSecret:   getEnv("JWT_SECRET", ""),
		LogDir:      getEnv("LOG_DIR", "/app/logs"),
	}
	if cfg.JWTSecret == "" {
		// Never use an empty HMAC key: it makes JWTs and the subscription
		// ?user= sig (subURLSig) forgeable. Generate an ephemeral secret.
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			log.Fatalf("config: cannot generate ephemeral JWT secret: %v", err)
		}
		cfg.JWTSecret = hex.EncodeToString(b[:])
		cfg.JWTEphemeral = true
	}

	return cfg
}

// NormalizeDBType 是 DB_TYPE 字面值的单点归一（R1-01-M2）：
// TrimSpace＋ToLower 后把别名 postgresql 折为 postgres，接受集
// {""、sqlite、postgres} 与后端启动判定、脚本面 normalize_db_type
// 逐字同集。一切 DB_TYPE 模式判定面必须先过本归一，不许各自再
// 写字面量比较——面板内升级执行器曾只认 postgres、对
// DB_TYPE=postgresql 的 PG 部署整格失去 PG 保护（卷门不启用、
// 改名误删在跑 PG 容器、快照错排），根源即判定面各写各的。
func NormalizeDBType(v string) string {
	n := strings.ToLower(strings.TrimSpace(v))
	if n == "postgresql" {
		n = "postgres"
	}
	return n
}

// DBTypeIsPostgres 判 DB_TYPE 是否选择 PostgreSQL（含别名拼写）。
func DBTypeIsPostgres(v string) bool {
	return NormalizeDBType(v) == "postgres"
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
