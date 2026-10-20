package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	// The runtime image has no zoneinfo; embed it so TZ (e.g. Asia/Shanghai
	// in the compose files) applies to logs and notification times.
	_ "time/tzdata"

	"go-backend/internal/app"
	"go-backend/internal/config"
	"go-backend/internal/http/handler"
)

func main() {
	cfg := config.FromEnv()
	// 移植自 kure29/flyx：JWT 密钥持久化。未设 JWT_SECRET 时，优先从
	// <DBPath 同级>/jwt_secret 文件加载，没有则生成并写入（0600），
	// 保证重启后旧 token 仍有效。失败时回退到 FromEnv 的临时密钥。
	if generated, err := config.EnsureJWTSecret(&cfg); err != nil {
		log.Printf("warning: JWT 密钥持久化失败 (%v)，使用临时密钥（重启后会话失效）", err)
	} else if generated {
		log.Println("已生成 JWT 密钥并保存到 jwt_secret 文件（0600），重启后会话保持有效")
	} else if cfg.JWTEphemeral {
		log.Println("warning: JWT_SECRET is not set, using an ephemeral secret (sessions will not survive a restart)")
	}
	log.Printf("starting go-backend on %s (db=%s)", cfg.Addr, cfg.DBPath)
	// R5-B2 A-001: 登录限流 XFF 解析的可信跳数（默认 1，覆盖 Caddy→backend
	// 与 nginx→backend 两种默认拓扑；Caddy→nginx→backend 请设为 2）。
	log.Printf("proxy trust: FLMX_TRUSTED_PROXY_HOPS=%d (login XFF parsed right-to-left)", handler.TrustedProxyHops())

	a, err := app.New(cfg)
	if err != nil {
		log.Fatalf("failed to create app: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- a.Run()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("received signal %s, shutting down", sig)
	case runErr := <-errCh:
		if runErr != nil && !errors.Is(runErr, http.ErrServerClosed) {
			log.Fatalf("server stopped unexpectedly: %v", runErr)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Shutdown(ctx); err != nil {
		log.Fatalf("shutdown failed: %v", err)
	}
}
