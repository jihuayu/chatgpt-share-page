// Command server runs the chatgpt-share-page service.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "time/tzdata" // embedded tz database for Windows deploys

	"github.com/jihuayu/chatgpt-share-page/internal/cache"
	"github.com/jihuayu/chatgpt-share-page/internal/config"
	"github.com/jihuayu/chatgpt-share-page/internal/fetcher"
	"github.com/jihuayu/chatgpt-share-page/internal/httpapi"
	"github.com/jihuayu/chatgpt-share-page/internal/publish"
	"github.com/jihuayu/chatgpt-share-page/internal/renderer"
	"github.com/jihuayu/chatgpt-share-page/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	store, err := storage.Open(cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.Close()

	files, err := storage.NewFileStore(cfg.DataDir)
	if err != nil {
		return err
	}
	r, err := renderer.New()
	if err != nil {
		return fmt.Errorf("init renderer: %w", err)
	}
	fetchClient := &fetcher.Client{
		MaxHTMLSize:  cfg.MaxFetchBytes,
		MaxRedirects: cfg.MaxRedirects,
		UserAgent:    "chatgpt-share-page/1.0",
	}
	purger := &cache.CloudflarePurger{
		Token:  cfg.CloudflareAPIToken,
		ZoneID: cfg.CloudflareZoneID,
	}
	svc := publish.NewService(cfg, store, files, fetchClient, r, purger, log)
	handler := httpapi.New(cfg, svc, store, files, r, log)

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           httpapi.Middleware(log, handler.Routes()),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", server.Addr, "public_base_url", cfg.PublicBaseURL)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-stop:
		log.Info("shutting down", "signal", sig.String())
	case err := <-errCh:
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}
