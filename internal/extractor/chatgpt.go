package extractor

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
)

var (
	streamEnqueueRE = regexp.MustCompile(`streamController\s*\.\s*enqueue\s*\(\s*("(\\.|[^"\\])*")\s*\)`)
	nextDataRE      = regexp.MustCompile(`(?is)<script[^>]+id=["']__NEXT_DATA__["'][^>]*>(.*?)</script>`)
)

// ExtractConversation finds the current conversation in a share page payload.
// It tries React Router streaming payloads first, then falls back to the
// legacy __NEXT_DATA__ script tag.
func ExtractConversation(page string) (map[string]any, error) {
	for _, match := range streamEnqueueRE.FindAllStringSubmatch(page, -1) {
		if len(match) < 2 {
			continue
		}
		var serialized string
		if json.Unmarshal([]byte(match[1]), &serialized) != nil {
			continue
		}
		serialized = strings.TrimSpace(serialized)
		if !strings.HasPrefix(serialized, "[") {
			continue
		}
		var table []any
		if json.Unmarshal([]byte(serialized), &table) != nil || len(table) == 0 {
			continue
		}
		decoded := decodeRefTable(table)
		if conversation := findConversation(decoded); conversation != nil {
			return conversation, nil
		}
	}

	if match := nextDataRE.FindStringSubmatch(page); len(match) == 2 {
		var payload any
		if json.Unmarshal([]byte(html.UnescapeString(match[1])), &payload) == nil {
			if conversation := findConversation(payload); conversation != nil {
				return conversation, nil
			}
		}
	}

	if strings.Contains(page, "challenge-error-text") || strings.Contains(page, "Enable JavaScript and cookies") {
		return nil, &ParseError{Message: "ChatGPT returned a browser challenge instead of shared chat data"}
	}
	return nil, &ParseError{Message: "could not find a shared ChatGPT conversation payload; the link may be private, expired, or changed format"}
}
