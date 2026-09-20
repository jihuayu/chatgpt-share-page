package conversation

import "testing"

func TestImageNormalization(t *testing.T) {
	image := map[string]any{"content_type": "image_asset_pointer", "asset_pointer": "sediment://file_test?shared_conversation_id=public"}
	message := map[string]any{"id": "picture", "author": map[string]any{"role": "tool"}, "channel": "final", "recipient": "all", "content": map[string]any{"content_type": "multimodal_text", "parts": []any{image, "private tool output"}}}
	normalized := normalizeMessage(message, false)
	if normalized == nil || normalized.Role != "assistant" || normalized.Process || len(normalized.Blocks) != 1 || normalized.Blocks[0].Type != "image" || normalized.Blocks[0].URL != "" {
		t.Fatalf("bad image: %+v", normalized)
	}
	message["metadata"] = map[string]any{"is_visually_hidden_from_conversation": true}
	if normalizeMessage(message, false) != nil {
		t.Fatal("hidden duplicate exposed")
	}
	for _, raw := range []string{"javascript:alert(1)", "http://example.com/a.png", "https://user:password@example.com/a.png", "data:image/png;base64,abc", "sediment://file_test"} {
		if PublicImageURL(raw) != "" {
			t.Fatalf("unsafe URL %s", raw)
		}
	}
	if PublicImageURL("https://example.com/a.png") == "" {
		t.Fatal("public image rejected")
	}
}

func TestRestoreImageAttachment(t *testing.T) {
	raw := map[string]any{"linear_conversation": []any{map[string]any{"message": map[string]any{"id": "q", "author": map[string]any{"role": "user"}, "content": map[string]any{"parts": []any{map[string]any{"content_type": "image_asset_pointer", "asset_pointer": "sediment://file_test"}, "original"}}}}}}
	s := &ConversationSnapshot{Messages: []ConversationMsg{{ID: "q", Role: "user", Blocks: []ContentBlock{{Type: "attachment", AssetName: "file_test"}, {Type: "markdown", Content: "archived text"}}}}}
	RestoreImageBlocks(s, raw)
	if s.Messages[0].Blocks[0].Type != "image" || s.Messages[0].Blocks[1].Content != "archived text" {
		t.Fatal("bad restore")
	}
}
