package fetcher

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"testing"
)

type imageTransport func(*http.Request) (*http.Response, error)

func (f imageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSharedImageDownload(t *testing.T) {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	for _, target := range []string{"https://cdn.oaiusercontent.com/image", "https://evil.test/image", "https://cdn.oaiusercontent.com.evil.test/image", "https://user:pass@cdn.oaiusercontent.com/image", "http://cdn.oaiusercontent.com/image"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			c := NewClient()
			c.HTTPClient = &http.Client{Transport: imageTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				var body []byte
				if calls == 1 {
					if r.Header.Get("oai-device-id") == "" || r.Header.Get("Referer") == "" || r.Header.Get("Authorization") != "" {
						t.Error("incorrect anonymous headers")
					}
					body, _ = json.Marshal(map[string]any{"status": "success", "download_url": target})
				} else {
					body = b.Bytes()
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
			})}
			data, err := c.FetchSharedImage(context.Background(), "https://chatgpt.com/share/example", "file_test")
			if imageHost(target) {
				if err != nil || !bytes.Equal(data, b.Bytes()) || calls != 2 {
					t.Fatalf("download failed: %v", err)
				}
			} else if err == nil || calls != 1 {
				t.Fatal("unsafe URL accepted")
			}
		})
	}
}

func TestSharedImageLive(t *testing.T) {
	share, file := os.Getenv("SHARE_IMAGE_TEST_URL"), os.Getenv("SHARE_IMAGE_TEST_ID")
	if share == "" || file == "" {
		t.Skip("optional live public image check")
	}
	data, err := NewClient().FetchSharedImage(context.Background(), share, file)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("image downloaded: %s, %d bytes", http.DetectContentType(data), len(data))
}
