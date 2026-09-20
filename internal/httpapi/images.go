package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jihuayu/chatgpt-share-page/internal/conversation"
	"github.com/jihuayu/chatgpt-share-page/internal/fetcher"
)

// handleImage is a bounded, on-demand disk cache, not an arbitrary URL proxy.
func (h *Handler) handleImage(w http.ResponseWriter, r *http.Request) {
	snap, err := h.store.GetSnapshotByID(r.Context(), r.PathValue("id"))
	fileID := r.PathValue("file")
	if err != nil || snap.Status != "active" || !fetcher.ImageFileID.MatchString(fileID) {
		http.NotFound(w, r)
		return
	}
	path := "conversations/" + snap.ID + "/images/" + fileID
	key := sha256.Sum256([]byte(path))
	lock := &h.imageLocks[int(key[0])%len(h.imageLocks)]
	lock.Lock()
	defer lock.Unlock()
	data, err := h.files.ReadFile(path)
	if err != nil {
		raw, readErr := h.files.ReadFile(h.files.RawRelPath(snap.ID))
		var payload map[string]any
		if readErr != nil || json.Unmarshal(raw, &payload) != nil {
			http.NotFound(w, r)
			return
		}
		normalized, normalizeErr := conversation.Normalize(payload, snap.SourceURL, conversation.Options{}, time.Now())
		allowed := false
		if normalizeErr == nil {
			for _, msg := range normalized.Messages {
				if msg.Hidden || msg.Process || msg.Role != "assistant" {
					continue
				}
				for _, block := range msg.Blocks {
					if block.Type == "image" && block.AssetName == fileID {
						allowed = true
					}
				}
			}
		}
		if !allowed {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		select {
		case h.fallbackSem <- struct{}{}:
			defer func() { <-h.fallbackSem }()
		case <-ctx.Done():
			http.Error(w, "image busy", 503)
			return
		}
		data, err = fetcher.NewClient().FetchSharedImage(ctx, snap.SourceURL, fileID)
		if err != nil {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "image unavailable", 502)
			return
		}
		if err := h.files.WriteFileAtomic(path, data); err != nil {
			http.Error(w, "image cache unavailable", 503)
			return
		}
	}
	sum := sha256.Sum256(data)
	etag := fmt.Sprintf(`"%x"`, sum)
	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, no-cache")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(304)
		return
	}
	_, _ = w.Write(data)
}
