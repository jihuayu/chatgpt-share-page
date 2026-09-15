// Package security provides share URL validation and admin token helpers.
package security

import (
	"net/url"
	"regexp"
	"strings"
)

var sharePathRE = regexp.MustCompile(`^/share/([^/?#]+)/?$`)

// InvalidURLError indicates that a URL is not a supported public share URL.
type InvalidURLError struct {
	URL    string
	Reason string
}

func (e *InvalidURLError) Error() string {
	if e.Reason == "" {
		return "invalid ChatGPT share URL"
	}
	return "invalid ChatGPT share URL: " + e.Reason
}

// ValidateShareURL only permits the public ChatGPT share hosts and path.
func ValidateShareURL(rawURL string) error {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return &InvalidURLError{URL: rawURL, Reason: "use an absolute https://chatgpt.com/share/... URL"}
	}
	if strings.ToLower(parsed.Scheme) != "https" {
		return &InvalidURLError{URL: rawURL, Reason: "only HTTPS share URLs are supported"}
	}
	if parsed.User != nil {
		return &InvalidURLError{URL: rawURL, Reason: "credentials in URL are not allowed"}
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "chatgpt.com", "www.chatgpt.com", "chat.openai.com", "www.chat.openai.com":
	default:
		return &InvalidURLError{URL: rawURL, Reason: "host must be chatgpt.com or chat.openai.com"}
	}
	if parsed.Port() != "" {
		return &InvalidURLError{URL: rawURL, Reason: "custom ports are not allowed"}
	}
	match := sharePathRE.FindStringSubmatch(parsed.Path)
	if len(match) != 2 || match[1] == "" {
		return &InvalidURLError{URL: rawURL, Reason: "path must match /share/<share-id>"}
	}
	return nil
}

// ExtractShareID returns the share identifier from a validated or raw URL.
func ExtractShareID(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err == nil {
		if match := sharePathRE.FindStringSubmatch(parsed.Path); len(match) == 2 {
			return match[1]
		}
	}
	return ""
}
