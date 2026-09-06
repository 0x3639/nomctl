// nomctl-relay forwards alerts from paired nomctl nodes to their operators'
// Telegram chats. It holds the bot token so nodes never need it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/0x3639/nomctl/internal/relay"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local /healthz endpoint and exit (for container health checks)")
	flag.Parse()

	listen := envOr("RELAY_LISTEN", ":8080")
	if *healthcheck {
		os.Exit(probe(listen))
	}

	level := slog.LevelInfo
	if envOr("RELAY_LOG_LEVEL", "info") == "debug" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	token := os.Getenv("RELAY_TELEGRAM_TOKEN")
	if token == "" {
		slog.Error("RELAY_TELEGRAM_TOKEN is required")
		os.Exit(2)
	}
	silentAfter, err := time.ParseDuration(envOr("RELAY_SILENT_AFTER", "5m"))
	if err != nil {
		slog.Error("bad RELAY_SILENT_AFTER", "err", err)
		os.Exit(2)
	}
	dbPath := envOr("RELAY_DB", "/data/relay.db")
	store, err := relay.OpenSQLite(dbPath)
	if err != nil {
		slog.Error("open database", "path", dbPath, "err", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	tg := relay.NewTelegram(token)
	srv := relay.NewServer(store, tg, relay.Options{SilentAfter: silentAfter, PublicURL: os.Getenv("RELAY_PUBLIC_URL")})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpServer := &http.Server{Addr: listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	go func() {
		slog.Info("relay listening", "addr", listen, "version", relay.Version)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server", "err", err)
			stop()
		}
	}()
	go srv.RunSilentTicker(ctx, 30*time.Second)
	go pollTelegram(ctx, tg, srv)

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}

// pollTelegram long-polls getUpdates and dispatches commands.
func pollTelegram(ctx context.Context, tg *relay.Telegram, srv *relay.Server) {
	var offset int64
	for ctx.Err() == nil {
		updates, err := tg.Updates(ctx, offset, 50*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("getUpdates failed", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			if err := srv.HandleUpdate(ctx, u); err != nil {
				slog.Warn("handle update", "chat", u.ChatID, "err", err)
			}
		}
	}
}

func probe(listen string) int {
	addr := listen
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	res, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
