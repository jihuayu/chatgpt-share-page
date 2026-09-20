package fetcher

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/jihuayu/chatgpt-share-page/internal/security"
)

var ImageFileID = regexp.MustCompile(`^file_[A-Za-z0-9]{1,100}$`)

func imageHost(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Port() == "" && strings.HasSuffix(strings.ToLower(u.Hostname()), ".oaiusercontent.com")
}

// FetchSharedImage resolves an anonymous shared-file URL and downloads its bytes.
// Signed URLs and anonymous device identifiers are never persisted or logged.
func (c *Client) FetchSharedImage(ctx context.Context, shareURL, fileID string) ([]byte, error) {
	if security.ValidateShareURL(shareURL) != nil || !ImageFileID.MatchString(fileID) {
		return nil, errors.New("invalid shared image")
	}
	client := *c.httpClient()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error { return errors.New("metadata redirect denied") }
	var device [16]byte
	if _, err := rand.Read(device[:]); err != nil {
		return nil, err
	}
	endpoint := "https://chatgpt.com/backend-anon/files/download/" + fileID + "?shared_conversation_id=" + url.QueryEscape(security.ExtractShareID(shareURL)) + "&inline=false&download_intent=false"
	req, _ := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	req.Header.Set("oai-device-id", fmt.Sprintf("%x-%x-%x-%x-%x", device[:4], device[4:6], device[6:8], device[8:10], device[10:]))
	req.Header.Set("Referer", shareURL)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	response, err := imageRequest(&client, req)
	if err != nil {
		return nil, errors.New("image metadata unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image metadata HTTP %d", response.StatusCode)
	}
	var meta struct {
		Status     string `json:"status"`
		URL        string `json:"download_url"`
		UserUpload bool   `json:"no_auth_user_upload"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&meta); err != nil || meta.Status != "success" || meta.UserUpload || !imageHost(meta.URL) {
		return nil, errors.New("no public image available")
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !imageHost(req.URL.String()) {
			return errors.New("image redirect denied")
		}
		return nil
	}
	req, _ = http.NewRequestWithContext(ctx, "GET", meta.URL, nil)
	response2, err := imageRequest(&client, req)
	if err != nil {
		return nil, errors.New("image download unavailable")
	}
	defer response2.Body.Close()
	if response2.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image download HTTP %d", response2.StatusCode)
	}
	const limit = 20 << 20
	data, err := io.ReadAll(io.LimitReader(response2.Body, limit+1))
	if err != nil || len(data) > limit {
		return nil, errors.New("image size or download error")
	}
	switch http.DetectContentType(data) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return data, nil
	}
	return nil, errors.New("unsupported image type")
}

func imageRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	var response *http.Response
	var err error
	for attempt := 0; attempt < maxFetchAttempts; attempt++ {
		response, err = client.Do(req.Clone(req.Context()))
		if err == nil || !isRetryableEOF(err) {
			return response, err
		}
	}
	return response, err
}
