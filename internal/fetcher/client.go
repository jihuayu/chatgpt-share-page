// Package fetcher downloads public ChatGPT share pages with strict limits.
package fetcher

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/jihuayu/chatgpt-share-page/internal/security"
)

const (
	defaultMaxHTML   = 20 << 20
	maxFetchAttempts = 3
	retryDelay       = 100 * time.Millisecond
)

// Keep the fetch path compatible with TLS-inspecting proxies that cannot
// handle Go's hybrid post-quantum ClientHello.
var classicCurvePreferences = []tls.CurveID{
	tls.X25519,
	tls.CurveP256,
	tls.CurveP384,
	tls.CurveP521,
}

// FetchError indicates a failure while downloading the share page.
type FetchError struct {
	URL        string
	StatusCode int
	Err        error
}

func (e *FetchError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("ChatGPT returned HTTP %d", e.StatusCode)
	}
	if e.Err != nil {
		return "could not download ChatGPT share page: " + e.Err.Error()
	}
	return "could not download ChatGPT share page"
}

func (e *FetchError) Unwrap() error { return e.Err }

// Client downloads share pages. Every redirect target is revalidated against
// the share-URL rules, and DNS answers are checked so the client cannot be
// pointed at loopback, private, or link-local addresses.
type Client struct {
	HTTPClient   *http.Client
	MaxHTMLSize  int64
	MaxRedirects int
	UserAgent    string
	// AllowPrivateAddresses disables the SSRF dial check. Intended for tests
	// that fetch from httptest servers on 127.0.0.1.
	AllowPrivateAddresses bool
}

// NewClient returns a client with conservative network defaults.
func NewClient() *Client {
	return &Client{
		MaxHTMLSize:  defaultMaxHTML,
		MaxRedirects: 3,
		UserAgent:    "chatgpt-share-page/1.0",
	}
}

// FetchHTML downloads a share page and limits the response body size.
func (c *Client) FetchHTML(ctx context.Context, rawURL string) (string, error) {
	if err := security.ValidateShareURL(rawURL); err != nil {
		return "", err
	}
	client := c.httpClient()
	clientCopy := *client
	maxRedirects := c.MaxRedirects
	if maxRedirects <= 0 {
		maxRedirects = 3
	}
	clientCopy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errors.New("too many redirects")
		}
		return security.ValidateShareURL(req.URL.String())
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", &FetchError{URL: rawURL, Err: err}
	}
	ua := c.UserAgent
	if ua == "" {
		ua = "chatgpt-share-page/1.0"
	}
	request.Header.Set("User-Agent", ua)
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	request.Header.Set("Accept-Language", "en-US,en;q=0.9")

	limit := c.MaxHTMLSize
	if limit <= 0 {
		limit = defaultMaxHTML
	}
	for attempt := 0; attempt < maxFetchAttempts; attempt++ {
		response, err := clientCopy.Do(request.Clone(ctx))
		if err == nil {
			if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
				_ = response.Body.Close()
				return "", &FetchError{URL: rawURL, StatusCode: response.StatusCode}
			}
			body, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
			_ = response.Body.Close()
			if readErr == nil {
				if int64(len(body)) > limit {
					return "", &FetchError{URL: rawURL, Err: errors.New("response body exceeds size limit")}
				}
				return string(body), nil
			}
			err = readErr
		}
		if !isRetryableEOF(err) || attempt == maxFetchAttempts-1 {
			return "", &FetchError{URL: rawURL, Err: err}
		}
		timer := time.NewTimer(time.Duration(attempt+1) * retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", &FetchError{URL: rawURL, Err: ctx.Err()}
		case <-timer.C:
		}
	}
	return "", &FetchError{URL: rawURL, Err: io.EOF}
}

func isRetryableEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	dialContext := dialer.DialContext
	if !c.AllowPrivateAddresses {
		dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if err := checkPublicAddress(ctx, address); err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, address)
		}
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext: dialContext,
			TLSClientConfig: &tls.Config{
				MinVersion:       tls.VersionTLS12,
				CurvePreferences: classicCurvePreferences,
			},
			ForceAttemptHTTP2:     false,
			MaxIdleConns:          4,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
}

// checkPublicAddress resolves the dial target and refuses non-public IPs.
// The hostname has already been restricted to ChatGPT share domains, so a DNS
// answer pointing at private space indicates SSRF or rebinding.
func checkPublicAddress(ctx context.Context, address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return fmt.Errorf("refusing to dial non-public address %s", ip)
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return fmt.Errorf("refusing to dial %s resolving to non-public address %s", host, ip)
		}
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	// IPv4-mapped IPv6 and other transition ranges that net.IP helpers miss.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 0 || v4[0] == 127 || (v4[0] == 169 && v4[1] == 254) {
			return false
		}
	}
	return true
}
