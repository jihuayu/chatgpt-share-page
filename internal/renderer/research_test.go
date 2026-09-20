package renderer

import (
	"github.com/jihuayu/chatgpt-share-page/internal/conversation"
	"strings"
	"testing"
)

func TestResearchCollapsedAndImageProxy(t *testing.T) {
	r, _ := New()
	s := &conversation.ConversationSnapshot{ID: "snapshot-test", Messages: []conversation.ConversationMsg{
		{Role: "assistant", Research: true, Blocks: []conversation.ContentBlock{{Type: "markdown", Content: "# Research title\n\nComplete report body"}}},
		{Role: "assistant", Blocks: []conversation.ContentBlock{{Type: "image", AssetName: "file_test", URL: "https://cdn.oaiusercontent.com/expired"}}},
	}}
	for _, render := range []func(*conversation.ConversationSnapshot) ([]byte, error){r.RenderPage, r.RenderEmbed} {
		b, err := render(s)
		if err != nil {
			t.Fatal(err)
		}
		h := string(b)
		for _, want := range []string{`<details class="research-report">`, `<strong>Research title</strong>`, "Complete report body", `src="/media/snapshot-test/file_test"`} {
			if !strings.Contains(h, want) {
				t.Errorf("missing %s", want)
			}
		}
		if strings.Contains(h, `class="research-report" open`) || strings.Contains(h, "https://cdn.oaiusercontent.com/expired") {
			t.Fatal("report expanded or upstream URL leaked")
		}
	}
}
