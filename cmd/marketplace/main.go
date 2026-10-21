package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/dub-otrezkov/soa-marketplace-hw2/internal/server"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("startup failed", "error", err.Error())
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	minutes, err := strconv.Atoi(env("ORDER_COOLDOWN_MINUTES", "1"))
	if err != nil || minutes < 0 || minutes > 525600 {
		return errors.New("ORDER_COOLDOWN_MINUTES must be between 0 and 525600")
	}
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := pool.Ping(startup); err != nil {
		return err
	}
	service, err := server.New(startup, pool, server.Config{
		JWTSecret: os.Getenv("JWT_SECRET"), OrderCooldown: time.Duration(minutes) * time.Minute,
		AdminEmail: os.Getenv("BOOTSTRAP_ADMIN_EMAIL"), AdminPassword: os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"),
	})
	if err != nil {
		return err
	}
	handler, err := service.Handler()
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: env("HTTP_ADDR", ":8080"), Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.ListenAndServe() }()
	slog.Info("marketplace listening", "address", httpServer.Addr)
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdown)
	}
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
