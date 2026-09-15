package renderer

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	"github.com/jihuayu/chatgpt-share-page/internal/conversation"
)

var inlineScriptRE = regexp.MustCompile("(?s)<script>(.*?)</script>")

func TestCSPHashesRenderedInlineScripts(t *testing.T) {
	t.Parallel()

	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &conversation.ConversationSnapshot{Title: "test"}
	tests := []struct {
		name   string
		render func(*conversation.ConversationSnapshot) ([]byte, error)
		csp    string
	}{
		{name: "page", render: r.RenderPage, csp: r.PageCSP()},
		{name: "embed", render: r.RenderEmbed, csp: r.EmbedCSP()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			htmlBytes, err := tt.render(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			match := inlineScriptRE.FindSubmatch(htmlBytes)
			if len(match) != 2 {
				t.Fatal("rendered HTML has no inline script")
			}
			script := normalizeNewlines(string(match[1]))
			sum := sha256.Sum256([]byte(script))
			want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
			if !strings.Contains(tt.csp, want) {
				t.Fatalf("CSP %q does not contain rendered script hash %q", tt.csp, want)
			}
		})
	}
}
