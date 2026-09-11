// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config carries every tunable the server needs.
type Config struct {
	Port                 int
	DataDir              string
	DatabasePath         string
	PublicBaseURL        string
	AppBaseURL           string
	MaxImportConcurrency int
	FetchTimeout         time.Duration
	MaxFetchBytes        int64
	MaxRequestBytes      int64
	MaxRedirects         int
	CloudflareAPIToken   string
	CloudflareZoneID     string
	LogLevel             string
}

// Load reads environment variables and applies phase-1 defaults.
func Load() (Config, error) {
	cfg := Config{
		Port:                 envInt("PORT", 8080),
		DataDir:              envString("DATA_DIR", "./data"),
		DatabasePath:         envString("DATABASE_PATH", "./data/app.db"),
		PublicBaseURL:        envString("PUBLIC_BASE_URL", "http://localhost:8080"),
		AppBaseURL:           envString("APP_BASE_URL", "http://localhost:8080"),
		MaxImportConcurrency: envInt("MAX_IMPORT_CONCURRENCY", 2),
		FetchTimeout:         time.Duration(envInt("FETCH_TIMEOUT_SECONDS", 30)) * time.Second,
		MaxFetchBytes:        int64(envInt("MAX_FETCH_BYTES", 20<<20)),
		MaxRequestBytes:      int64(envInt("MAX_REQUEST_BYTES", 1<<20)),
		MaxRedirects:         envInt("MAX_REDIRECTS", 3),
		CloudflareAPIToken:   envString("CLOUDFLARE_API_TOKEN", ""),
		CloudflareZoneID:     envString("CLOUDFLARE_ZONE_ID", ""),
		LogLevel:             envString("LOG_LEVEL", "info"),
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return Config{}, fmt.Errorf("invalid PORT %d", cfg.Port)
	}
	if cfg.DataDir == "" {
		return Config{}, fmt.Errorf("DATA_DIR must not be empty")
	}
	if cfg.DatabasePath == "" {
		return Config{}, fmt.Errorf("DATABASE_PATH must not be empty")
	}
	if cfg.MaxImportConcurrency < 1 {
		cfg.MaxImportConcurrency = 1
	}
	if cfg.FetchTimeout <= 0 {
		cfg.FetchTimeout = 30 * time.Second
	}
	if cfg.MaxFetchBytes <= 0 {
		cfg.MaxFetchBytes = 20 << 20
	}
	if cfg.MaxRequestBytes <= 0 {
		cfg.MaxRequestBytes = 1 << 20
	}
	return cfg, nil
}

func envString(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
