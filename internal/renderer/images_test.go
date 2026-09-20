package renderer

import (
	"github.com/jihuayu/chatgpt-share-page/internal/conversation"
	"strings"
	"testing"
)

func TestImageRendering(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	s := &conversation.ConversationSnapshot{Messages: []conversation.ConversationMsg{
		{Role: "user", Blocks: []conversation.ContentBlock{{Type: "image", Title: "file_private"}, {Type: "attachment", Title: "notes.pdf"}, {Type: "markdown", Content: "Question"}}},
		{Role: "assistant", Blocks: []conversation.ContentBlock{{Type: "image", URL: "https://example.com/image.png", Title: "a\" onerror=\"alert(1)"}}},
	}}
	for _, render := range []func(*conversation.ConversationSnapshot) ([]byte, error){r.RenderPage, r.RenderEmbed} {
		b, err := render(s)
		if err != nil {
			t.Fatal(err)
		}
		html := string(b)
		for _, want := range []string{"已上传图片", "已上传文件", "notes.pdf", `src="https://example.com/image.png"`, `referrerpolicy="no-referrer"`} {
			if !strings.Contains(html, want) {
				t.Errorf("missing %s", want)
			}
		}
		for _, bad := range []string{"file_private", ` onerror="alert(1)"`, "图片暂不可用"} {
			if strings.Contains(html, bad) {
				t.Errorf("unexpected %s", bad)
			}
		}
		if strings.Index(html, `class="msg-media"`) > strings.Index(html, `class="msg-body"`) {
			t.Fatal("media should precede bubble")
		}
	}
}
