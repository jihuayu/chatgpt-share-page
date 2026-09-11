// Package cache purges Cloudflare edge cache for stable snapshot URLs. When
// no API token is configured, purges are recorded as failures for manual
// cleanup without blocking the publish flow.
package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrNotConfigured is returned when Cloudflare credentials are missing.
var ErrNotConfigured = errors.New("cloudflare API token or zone id not configured")

// Purger invalidates public URLs at the edge.
type Purger interface {
	PurgeURLs(ctx context.Context, urls []string) error
}

// CloudflarePurger calls the zone purge_cache endpoint with a file list.
type CloudflarePurger struct {
	Token      string
	ZoneID     string
	HTTPClient *http.Client
}

// PurgeURLs purges the given absolute URLs. A nil receiver or missing
// credentials report ErrNotConfigured so callers can log and continue.
func (p *CloudflarePurger) PurgeURLs(ctx context.Context, urls []string) error {
	if p == nil || p.Token == "" || p.ZoneID == "" {
		return ErrNotConfigured
	}
	if len(urls) == 0 {
		return nil
	}
	if len(urls) > 30 {
		urls = urls[:30] // Cloudflare file-purge limit per request
	}
	payload, err := json.Marshal(map[string]any{"files": urls})
	if err != nil {
		return err
	}
	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	endpoint := "https://api.cloudflare.com/client/v4/zones/" + p.ZoneID + "/purge_cache"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare purge: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cloudflare purge: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
