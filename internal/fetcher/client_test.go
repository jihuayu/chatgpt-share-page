package fetcher

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestDefaultHTTPClientUsesCompatibleTLSSettings(t *testing.T) {
	t.Parallel()

	client := NewClient().httpClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	want := []tls.CurveID{
		tls.X25519,
		tls.CurveP256,
		tls.CurveP384,
		tls.CurveP521,
	}
	if !reflect.DeepEqual(transport.TLSClientConfig.CurvePreferences, want) {
		t.Fatalf("curve preferences = %v, want %v", transport.TLSClientConfig.CurvePreferences, want)
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 = true, want false")
	}
}

func TestFetchHTMLRetriesEOF(t *testing.T) {
	t.Parallel()

	calls := 0
	client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, io.EOF
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("<html>ok</html>")),
			Request:    r,
		}, nil
	})}}

	got, err := client.FetchHTML(context.Background(), "https://chatgpt.com/share/test")
	if err != nil {
		t.Fatal(err)
	}
	if got != "<html>ok</html>" {
		t.Fatalf("body = %q", got)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}
