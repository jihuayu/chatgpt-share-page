package renderer

import (
	"github.com/jihuayu/chatgpt-share-page/internal/conversation"
	"strings"
	"testing"
)

func TestChatPresentation(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	s := &conversation.ConversationSnapshot{Title: "中文对话", Messages: []conversation.ConversationMsg{
		{Role: "user", Blocks: []conversation.ContentBlock{{Type: "markdown", Content: "问题是什么？"}}},
		{Role: "assistant", Process: true, Blocks: []conversation.ContentBlock{{Type: "code", Content: "private command"}}},
		{Role: "assistant", Hidden: true, Blocks: []conversation.ContentBlock{{Type: "markdown", Content: "hidden answer"}}},
		{Role: "tool", Blocks: []conversation.ContentBlock{{Type: "markdown", Content: "tool output"}}},
		{Role: "assistant", Blocks: []conversation.ContentBlock{{Type: "markdown", Content: "这是**回答**。\ue200cite\ue202turn123view0\ue201\n\n```go\nfmt.Println(1)\n```"}, {Type: "citation", URL: "https://example.com", Title: "来源"}}},
	}}
	for _, render := range []func(*conversation.ConversationSnapshot) ([]byte, error){r.RenderPage, r.RenderEmbed} {
		data, err := render(s)
		if err != nil {
			t.Fatal(err)
		}
		page := string(data)
		for _, want := range []string{"msg-user", "msg-assistant", "问题是什么", "<strong>回答</strong>", "2 条消息", "https://example.com", `<details class="sources">`} {
			if !strings.Contains(page, want) {
				t.Errorf("missing %q", want)
			}
		}
		for _, bad := range []string{"private command", "hidden answer", "tool output", "turn123view0", "\ue200"} {
			if strings.Contains(page, bad) {
				t.Errorf("leaked %q", bad)
			}
		}
	}
}
