package conversation

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	fileIDRE    = regexp.MustCompile(`file_[A-Za-z0-9]+`)
	nonSlugRE   = regexp.MustCompile(`[^a-z0-9]+`)
	multiDashRE = regexp.MustCompile(`-{2,}`)
)

// Options controls normalization.
type Options struct {
	IncludeHidden bool
	AllNodes      bool
	Timezone      string
}

// Normalize converts a decoded ChatGPT conversation payload into the stable
// ConversationSnapshot model.
func Normalize(conversation map[string]any, sourceURL string, opts Options, now time.Time) (*ConversationSnapshot, error) {
	location, err := ResolveLocation(opts.Timezone)
	if err != nil {
		return nil, err
	}
	title := stringValue(conversation["title"])
	if title == "" {
		title = "ChatGPT Shared Chat"
	}
	conversationID := stringValue(conversation["conversation_id"])
	if conversationID == "" {
		conversationID = stringValue(conversation["id"])
	}

	nodes := conversationNodes(conversation, opts.AllNodes)
	messages := make([]ConversationMsg, 0, len(nodes))
	for _, node := range nodes {
		message, ok := node["message"].(map[string]any)
		if !ok || message == nil {
			continue
		}
		msg := normalizeMessage(message, opts.IncludeHidden)
		if msg == nil {
			continue
		}
		messages = append(messages, *msg)
	}

	model := stringValue(conversation["default_model_slug"])
	snapshot := &ConversationSnapshot{
		Version:    Version,
		Title:      title,
		Source:     SnapshotSource{Provider: "chatgpt", URL: sourceURL, ShareID: shareID(sourceURL)},
		ImportedAt: now.UTC(),
		UpdatedAt:  now.UTC(),
		Messages:   messages,
		Metadata: SnapshotMetadata{
			ConversationID: conversationID,
			MessageCount:   len(messages),
			Model:          model,
			IncludeHidden:  opts.IncludeHidden,
			AllNodes:       opts.AllNodes,
			Timezone:       location.String(),
		},
	}
	if snapshot.Metadata.Model == "" {
		snapshot.Metadata.Model = detectModel(messages)
	}
	return snapshot, nil
}

// RestorePresentationMetadata upgrades legacy snapshots without replacing their
// archived text or message order. Only matching message IDs supply metadata.
func RestorePresentationMetadata(snapshot *ConversationSnapshot, raw map[string]any) {
	byID := map[string]*ConversationMsg{}
	for _, node := range conversationNodes(raw, true) {
		message, _ := node["message"].(map[string]any)
		if msg := normalizeMessage(message, true); msg != nil && msg.ID != "" {
			byID[msg.ID] = msg
		}
	}
	for i := range snapshot.Messages {
		msg := &snapshot.Messages[i]
		if original := byID[msg.ID]; original != nil {
			msg.Process = original.Process
			msg.Hidden = msg.Hidden || original.Hidden
			seen := map[string]bool{}
			for _, block := range msg.Blocks {
				seen[block.URL] = true
			}
			for _, block := range original.Blocks {
				if block.Type == "citation" && !seen[block.URL] {
					msg.Blocks = append(msg.Blocks, block)
					seen[block.URL] = true
				}
			}
		}
	}
}

// ResolveLocation validates and loads a timezone name.
func ResolveLocation(name string) (*time.Location, error) {
	if name == "" || strings.EqualFold(name, "local") {
		return time.Local, nil
	}
	if strings.EqualFold(name, "utc") {
		return time.UTC, nil
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, &InvalidOptionError{Message: "unknown timezone: " + name}
	}
	return location, nil
}

// InvalidOptionError indicates an invalid import option.
type InvalidOptionError struct{ Message string }

func (e *InvalidOptionError) Error() string { return e.Message }

// Slugify produces a URL-safe slug base from a title.
func Slugify(title string) string {
	slug := nonSlugRE.ReplaceAllString(strings.ToLower(title), "-")
	slug = strings.Trim(slug, "-")
	slug = multiDashRE.ReplaceAllString(slug, "-")
	if slug == "" {
		slug = "conversation"
	}
	if len(slug) > 60 {
		slug = strings.Trim(slug[:60], "-")
	}
	return slug
}

func shareID(rawURL string) string {
	parts := strings.Split(strings.Trim(rawURL, "/"), "/")
	for i, part := range parts {
		if part == "share" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func detectModel(messages []ConversationMsg) string {
	for _, msg := range messages {
		if msg.Role == "assistant" {
			for _, block := range msg.Blocks {
				if block.Type == "attachment" && block.Title == "model" {
					return block.Content
				}
			}
		}
	}
	return ""
}

func conversationNodes(conversation map[string]any, allNodes bool) []map[string]any {
	mapping, _ := conversation["mapping"].(map[string]any)
	if allNodes && mapping != nil {
		nodes := make([]map[string]any, 0, len(mapping))
		for _, value := range mapping {
			if node, ok := value.(map[string]any); ok {
				nodes = append(nodes, node)
			}
		}
		sort.SliceStable(nodes, func(i, j int) bool { return nodeSortKey(nodes[i]) < nodeSortKey(nodes[j]) })
		return nodes
	}
	if linear, ok := conversation["linear_conversation"].([]any); ok {
		nodes := make([]map[string]any, 0, len(linear))
		for _, value := range linear {
			if node, ok := value.(map[string]any); ok {
				nodes = append(nodes, node)
			}
		}
		return nodes
	}
	if mapping == nil {
		return nil
	}
	current := stringValue(conversation["current_node"])
	if current == "" {
		return mapValues(mapping)
	}
	path := make([]map[string]any, 0)
	visited := map[string]bool{}
	for current != "" && !visited[current] {
		visited[current] = true
		node, ok := mapping[current].(map[string]any)
		if !ok {
			break
		}
		path = append(path, node)
		current = stringValue(node["parent"])
	}
	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	return path
}

func mapValues(mapping map[string]any) []map[string]any {
	values := make([]map[string]any, 0, len(mapping))
	for _, value := range mapping {
		if node, ok := value.(map[string]any); ok {
			values = append(values, node)
		}
	}
	return values
}

func nodeSortKey(node map[string]any) string {
	message, _ := node["message"].(map[string]any)
	created := math.Inf(1)
	if value := numericValue(message["create_time"]); value != nil {
		created = *value
	}
	return fmt.Sprintf("%020.6f:%s", created, stringValue(node["id"]))
}

// normalizeMessage returns nil when the message must be skipped.
func normalizeMessage(message map[string]any, includeHidden bool) *ConversationMsg {
	author, _ := message["author"].(map[string]any)
	role := strings.ToLower(stringValue(author["role"]))
	switch role {
	case "user", "assistant", "system", "tool":
	default:
		if role == "" {
			role = "unknown"
		}
	}
	metadata, _ := message["metadata"].(map[string]any)
	hidden := boolValue(metadata["is_visually_hidden_from_conversation"])
	content, _ := message["content"].(map[string]any)
	contentType := stringValue(content["content_type"])
	channel := strings.ToLower(firstString(message["channel"], metadata["channel"]))
	recipient := strings.ToLower(stringValue(message["recipient"]))
	process := role == "tool" || role == "system" ||
		(role == "assistant" && ((channel != "" && channel != "final") ||
			(recipient != "" && recipient != "all") || contentType == "thoughts" ||
			contentType == "reasoning_recap" || contentType == "execution_output"))

	if !includeHidden {
		if role != "user" && role != "assistant" {
			return nil
		}
		if hidden || contentType == "model_editable_context" {
			return nil
		}
	}

	blocks := contentBlocks(message, content, metadata)
	if len(blocks) == 0 {
		return nil
	}
	for _, ref := range citationBlocks(metadata) {
		blocks = append(blocks, ref)
	}

	msg := &ConversationMsg{
		ID:      stringValue(message["id"]),
		Role:    role,
		Blocks:  blocks,
		Hidden:  hidden,
		Process: process,
	}
	if seconds := numericValue(message["create_time"]); seconds != nil {
		created := time.Unix(0, int64(*seconds*float64(time.Second))).UTC()
		msg.CreatedAt = &created
	}
	return msg
}

// contentBlocks turns the ChatGPT content object into typed blocks.
func contentBlocks(message, content, metadata map[string]any) []ContentBlock {
	contentType := stringValue(content["content_type"])
	switch contentType {
	case "code":
		text := stringValue(content["text"])
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []ContentBlock{{
			Type:     "code",
			Content:  strings.TrimRight(text, "\n"),
			Language: firstString(content["language"], content["language_slug"]),
		}}
	case "execution_output":
		text := stringValue(content["text"])
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []ContentBlock{{Type: "code", Content: strings.TrimRight(text, "\n"), Language: "text"}}
	case "thoughts", "reasoning_recap":
		text := stringValue(content["text"])
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []ContentBlock{{Type: "quote", Content: strings.TrimSpace(text), Title: "Reasoning"}}
	case "model_editable_context", "tether_browsing_display":
		return nil
	}

	parts, _ := content["parts"].([]any)
	var blocks []ContentBlock
	var imageParts []map[string]any
	for _, part := range parts {
		switch typed := part.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				blocks = append(blocks, ContentBlock{Type: "markdown", Content: strings.TrimRight(typed, "\n")})
			}
		case map[string]any:
			if stringValue(typed["content_type"]) == "image_asset_pointer" {
				imageParts = append(imageParts, typed)
			} else {
				blocks = append(blocks, attachmentBlockFromPart(typed))
			}
		case nil:
		default:
			if text := strings.TrimSpace(stringValue(typed)); text != "" {
				blocks = append(blocks, ContentBlock{Type: "markdown", Content: text})
			}
		}
	}
	blocks = append(attachmentBlocks(message, metadata, imageParts), blocks...)
	if len(blocks) == 0 {
		fallback := firstString(content["text"], content["result"])
		if strings.TrimSpace(fallback) != "" {
			blocks = append(blocks, ContentBlock{Type: "markdown", Content: strings.TrimRight(fallback, "\n")})
		}
	}
	return blocks
}

// attachmentBlocks converts image asset pointers and metadata attachments into
// metadata-only attachment blocks. Binary assets are never downloaded.
func attachmentBlocks(message, metadata map[string]any, imageParts []map[string]any) []ContentBlock {
	attachments, _ := metadata["attachments"].([]any)
	byID := make(map[string]map[string]any, len(attachments))
	for _, item := range attachments {
		if attachment, ok := item.(map[string]any); ok {
			if id := stringValue(attachment["id"]); id != "" {
				byID[id] = attachment
			}
		}
	}
	var blocks []ContentBlock
	seen := map[string]bool{}
	for _, part := range imageParts {
		pointer := stringValue(part["asset_pointer"])
		fileID := fileIDRE.FindString(pointer)
		attachment := byID[fileID]
		if fileID != "" {
			seen[fileID] = true
		}
		name := firstString(attachment["name"])
		if name == "" {
			name = fileID
		}
		if name == "" {
			name = "image"
		}
		mime := firstString(attachment["mime_type"], part["mime_type"])
		if mime == "" {
			mime = "image"
		}
		blocks = append(blocks, ContentBlock{
			Type:      "attachment",
			Title:     name,
			Content:   describeAttachment(mime, firstNonNil(part["size_bytes"], attachment["size"]), part),
			AssetName: fileID,
		})
	}
	for _, item := range attachments {
		attachment, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := stringValue(attachment["id"])
		if id != "" && seen[id] {
			continue
		}
		name := firstString(attachment["name"])
		if name == "" {
			name = id
		}
		if name == "" {
			name = "attachment"
		}
		blocks = append(blocks, ContentBlock{
			Type:      "attachment",
			Title:     name,
			Content:   describeAttachment(stringValue(attachment["mime_type"]), attachment["size"], nil),
			AssetName: id,
		})
	}
	return blocks
}

func describeAttachment(mime string, size any, part map[string]any) string {
	var parts []string
	if mime != "" {
		parts = append(parts, mime)
	}
	if text := humanSize(size); text != "" {
		parts = append(parts, text)
	}
	if part != nil {
		width, height := stringValue(part["width"]), stringValue(part["height"])
		if width != "" && height != "" {
			parts = append(parts, width+"x"+height)
		}
	}
	return strings.Join(parts, " · ")
}

func attachmentBlockFromPart(part map[string]any) ContentBlock {
	kind := stringValue(part["content_type"])
	if kind == "" {
		kind = "part"
	}
	encoded, _ := json.Marshal(part)
	return ContentBlock{Type: "attachment", Title: kind, Content: string(encoded)}
}

// citationBlocks extracts web citation references from message metadata.
func citationBlocks(metadata map[string]any) []ContentBlock {
	raw := metadata["content_references"]
	var items []any
	switch typed := raw.(type) {
	case []any:
		items = typed
	case map[string]any:
		items, _ = typed["items"].([]any)
	}
	seen := map[string]bool{}
	var blocks []ContentBlock
	for len(items) > 0 {
		item := items[0]
		items = items[1:]
		ref, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"items", "refs"} {
			if nested, ok := ref[key].([]any); ok {
				items = append(items, nested...)
			}
		}
		url := firstString(ref["url"], ref["attribution"])
		if url == "" || !strings.HasPrefix(url, "https://") || seen[url] {
			continue
		}
		seen[url] = true
		title := firstString(ref["title"])
		if title == "" {
			title = url
		}
		blocks = append(blocks, ContentBlock{Type: "citation", Title: title, URL: url})
	}
	return blocks
}

func humanSize(value any) string {
	size := numericValue(value)
	if size == nil {
		return ""
	}
	units := []string{"B", "KB", "MB", "GB"}
	unit := 0
	for *size >= 1024 && unit < len(units)-1 {
		*size /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", int64(*size), units[unit])
	}
	return fmt.Sprintf("%.1f %s", *size, units[unit])
}

func numericValue(value any) *float64 {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return nil
		}
		number = parsed
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err != nil {
			return nil
		}
		number = parsed
	default:
		return nil
	}
	return &number
}

func boolValue(value any) bool {
	result, ok := value.(bool)
	return ok && result
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func firstString(values ...any) string {
	for _, value := range values {
		if text := strings.TrimSpace(stringValue(value)); text != "" && text != "<nil>" {
			return text
		}
	}
	return ""
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
