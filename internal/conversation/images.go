package conversation

import "net/url"

// PublicImageURL accepts browser-loadable HTTPS image locations, never internal
// sediment pointers, credentials, script URLs or local data URLs.
func PublicImageURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return u.String()
}

func finalImageOutput(message map[string]any) bool {
	author, _ := message["author"].(map[string]any)
	metadata, _ := message["metadata"].(map[string]any)
	content, _ := message["content"].(map[string]any)
	if author["role"] != "tool" || message["channel"] != "final" || boolValue(metadata["is_visually_hidden_from_conversation"]) || content["content_type"] != "multimodal_text" {
		return false
	}
	parts, _ := content["parts"].([]any)
	for _, part := range parts {
		p, _ := part.(map[string]any)
		if p["content_type"] == "image_asset_pointer" {
			return true
		}
	}
	return false
}

// RestoreImageBlocks upgrades only matching image attachments. Archived prose
// and unrelated attachments remain unchanged.
func RestoreImageBlocks(snapshot *ConversationSnapshot, raw map[string]any) {
	byID := map[string]*ConversationMsg{}
	for _, node := range conversationNodes(raw, snapshot.Metadata.AllNodes) {
		m, _ := node["message"].(map[string]any)
		if normalized := normalizeMessage(m, false); normalized != nil {
			byID[normalized.ID] = normalized
		}
	}
	for i := range snapshot.Messages {
		m := &snapshot.Messages[i]
		original := byID[m.ID]
		if original == nil {
			continue
		}
		for _, image := range original.Blocks {
			if image.Type != "image" {
				continue
			}
			for j, block := range m.Blocks {
				if (block.Type == "attachment" || block.Type == "image") && block.AssetName == image.AssetName {
					m.Blocks[j] = image
					break
				}
			}
		}
	}
}
