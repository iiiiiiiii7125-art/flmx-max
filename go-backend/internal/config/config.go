package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
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

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
