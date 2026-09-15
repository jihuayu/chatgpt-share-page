package renderer

import (
	"strings"
	"testing"
)

func TestRenderMarkdownSanitizesActiveContent(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	payload := `[safe](https://example.com)
[bad](javascript:alert(1))
<script>alert("xss")</script>
<img src=x onerror=alert(1)>
<iframe src="https://evil.example"></iframe>`
	out, err := r.renderMarkdown(payload)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(out)
	for _, forbidden := range []string{"alert(\"xss\")", "javascript:", "onerror", "<iframe", "<img"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("sanitized HTML contains %q: %s", forbidden, out)
		}
	}
	if !strings.Contains(out, `href="https://example.com"`) {
		t.Errorf("safe HTTPS link was removed: %s", out)
	}
}
