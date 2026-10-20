package app

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go-backend/internal/config"
	httpserver "go-backend/internal/http"
	"go-backend/internal/http/handler"
	"go-backend/internal/store/repo"
)

type App struct {
	cfg    config.Config
	server *http.Server
	repo   *repo.Repository
	h      *handler.Handler
}

// validatePostgresPassword rejects empty or well-known default PostgreSQL
// passwords before the connection is opened (W1/F-121). The check lives in
// this assembly layer, not in repo.OpenPostgres, so library callers and
// tests that open repositories directly are unaffected.
//
// Only the postgres/postgresql DB types are checked: SQLite (and an empty
// DB_TYPE, which selects SQLite) returns nil unconditionally. An empty DSN
// also returns nil — repo.OpenPostgres already reports "empty postgres dsn"
// for it, and that error must not be masked or duplicated here.
func validatePostgresPassword(dbType, dsn string) error {
	switch strings.ToLower(strings.TrimSpace(dbType)) {
	case "postgres", "postgresql":
	default:
		return nil
	}
	if strings.TrimSpace(dsn) == "" {
		return nil
	}
	password := postgresDSNPassword(dsn)
	if password == "" || password == "flux_panel_change_me" || password == "postgres" {
		return fmt.Errorf("PostgreSQL 密码未设置或仍为默认值，拒绝启动。请在 .env 中设置强密码 POSTGRES_PASSWORD，并确保 DATABASE_URL 中的密码与之一致。")
	}
	return nil
}

// postgresDSNPassword extracts the password from a PostgreSQL DSN. The
// canonical form is a URL (postgres://user:pass@host/db), parsed with
// net/url so percent-escapes are decoded correctly; the keyword form
// (host=... password=...) is handled as a fallback. A DSN whose password
// cannot be determined yields "", which the caller treats as unset.
func postgresDSNPassword(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && u.User != nil {
		password, _ := u.User.Password()
		return password
	}
	// Keyword form: space-separated key=value pairs, values may be
	// single-quoted; "password = value" with spaced "=" is also valid.
	fields := strings.Fields(dsn)
	for i, f := range fields {
		if f == "password" && i+2 < len(fields) && fields[i+1] == "=" {
			return strings.Trim(fields[i+2], "'")
		}
		if strings.HasPrefix(f, "password=") {
			return strings.Trim(strings.TrimPrefix(f, "password="), "'")
		}
	}
	return ""
}

func New(cfg config.Config) (*App, error) {
	var (
		r   *repo.Repository
		err error
	)

	switch strings.ToLower(strings.TrimSpace(cfg.DBType)) {
	case "", "sqlite":
		r, err = repo.Open(cfg.DBPath)
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}
	case "postgres", "postgresql":
		if err := validatePostgresPassword(cfg.DBType, cfg.DatabaseURL); err != nil {
			return nil, err
		}
		r, err = repo.OpenPostgres(cfg.DatabaseURL)
		if err != nil {
			return nil, fmt.Errorf("open postgres: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported DB_TYPE %q", cfg.DBType)
	}

	h := handler.New(r, cfg.JWTSecret)
	router := httpserver.NewRouter(h, cfg.JWTSecret)

	s := &http.Server{
		Addr:              cfg.Addr,
		Handler:           router,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}

	return &App{cfg: cfg, server: s, repo: r, h: h}, nil
}

func (a *App) Run() error {
	if a.h != nil {
		a.h.StartBackgroundJobs()
	}
	return a.server.ListenAndServe()
}

func (a *App) Shutdown(ctx context.Context) error {
	if a.h != nil {
		a.h.StopBackgroundJobs()
	}
	shutdownErr := a.server.Shutdown(ctx)
	closeErr := a.repo.Close()
	if shutdownErr != nil {
		return shutdownErr
	}
	return closeErr
}
