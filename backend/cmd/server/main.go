package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shawn-bluce/renderbin/backend/internal/buildinfo"
	"github.com/shawn-bluce/renderbin/backend/internal/config"
	"github.com/shawn-bluce/renderbin/backend/internal/db"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
	"github.com/shawn-bluce/renderbin/backend/internal/server"
)

// Defaults shared with the CLI subcommands, which have to reach the same
// database the server would.
const (
	defaultListenAddr = ":8080"
	defaultDBPath     = "data/app.db"
)

func main() {
	// Argument-less invocation starts the server (what the container's
	// ENTRYPOINT does); anything else is a subcommand — see cli.go.
	if code, handled := runCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	runtimeConfig, err := config.Load()
	if err != nil {
		logger.Error("load runtime config", "error", err)
		os.Exit(1)
	}

	addr := envOr("LISTEN_ADDR", defaultListenAddr)
	dbPath := envOr("DB_PATH", defaultDBPath)

	if err := os.MkdirAll(dirOf(dbPath), 0o755); err != nil {
		logger.Error("create db dir", "error", err)
		os.Exit(1)
	}

	conn, err := db.Open(dbPath)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer conn.Close()

	queries := sqlcgen.New(conn)
	handler := server.NewWithConfig(queries, conn, logger, runtimeConfig)

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout is intentionally left unset (0): backup and file
		// downloads stream potentially large responses, and a write deadline
		// would truncate them. Slowloris is mitigated by ReadHeaderTimeout.
	}

	// Run the server in the background so main can wait for a shutdown signal.
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr, "version", buildinfo.Version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		logger.Error("server exited", "error", err)
		os.Exit(1)
	case <-ctx.Done():
		stop() // restore default signal handling so a second signal force-quits
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			os.Exit(1)
		}
		logger.Info("shutdown complete")
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}
