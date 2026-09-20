package conversation

import "testing"

func TestProcessMessageClassification(t *testing.T) {
	for _, tc := range []struct {
		channel, recipient, kind string
		process                  bool
	}{
		{"final", "all", "text", false}, {"", "", "text", false}, {"commentary", "all", "text", true},
		{"analysis", "all", "text", true}, {"", "python", "code", true}, {"", "", "thoughts", true},
		{"", "", "code", false},
	} {
		m := map[string]any{"id": "a", "author": map[string]any{"role": "assistant"}, "channel": tc.channel, "recipient": tc.recipient, "content": map[string]any{"content_type": tc.kind, "text": "text", "parts": []any{"text"}}}
		got := normalizeMessage(m, true)
		if got == nil || got.Process != tc.process {
			t.Fatalf("%+v => %+v", tc, got)
		}
		raw := map[string]any{"linear_conversation": []any{map[string]any{"message": m}}}
		s := &ConversationSnapshot{Messages: []ConversationMsg{{ID: "a", Role: "assistant", Blocks: []ContentBlock{{Type: "markdown", Content: "archived text"}}}}}
		RestorePresentationMetadata(s, raw)
		if s.Messages[0].Process != tc.process || s.Messages[0].Blocks[0].Content != "archived text" {
			t.Fatal("legacy metadata changed text or lost process status")
		}
	}
}
