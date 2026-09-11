// Package extractor finds and decodes the conversation payload embedded in a
// ChatGPT share page, supporting both the React Router streaming reference
// table and the legacy __NEXT_DATA__ JSON blob.
package extractor

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseError indicates that the response did not contain a conversation.
type ParseError struct{ Message string }

func (e *ParseError) Error() string { return e.Message }

// decodeRefTable resolves the indexed reference table used by the React
// Router streaming payload. Element 0 is the root value.
func decodeRefTable(table []any) any {
	return decodeRef(0, table, map[int]bool{})
}

func decodeRef(value any, table []any, seen map[int]bool) any {
	index, ok := refIndex(value)
	if !ok {
		return decodeValue(value, table, seen)
	}
	if index < 0 || index >= len(table) {
		return value
	}
	if seen[index] {
		return fmt.Sprintf("<cycle:%d>", index)
	}
	nextSeen := cloneSeen(seen)
	nextSeen[index] = true
	return decodeValue(table[index], table, nextSeen)
}

func decodeValue(value any, table []any, seen map[int]bool) any {
	switch typed := value.(type) {
	case map[string]any:
		decoded := make(map[string]any, len(typed))
		for key, item := range typed {
			decodedKey := key
			if strings.HasPrefix(key, "_") {
				if index, err := strconv.Atoi(key[1:]); err == nil {
					decodedKey = stringValue(decodeRef(float64(index), table, seen))
				}
			}
			decoded[decodedKey] = decodeRef(item, table, seen)
		}
		return decoded
	case []any:
		decoded := make([]any, len(typed))
		for index, item := range typed {
			decoded[index] = decodeRef(item, table, seen)
		}
		return decoded
	default:
		return value
	}
}

func cloneSeen(seen map[int]bool) map[int]bool {
	clone := make(map[int]bool, len(seen)+1)
	for key, value := range seen {
		clone[key] = value
	}
	return clone
}

func refIndex(value any) (int, bool) {
	switch number := value.(type) {
	case float64:
		if number >= 0 && number <= math.MaxInt && math.Trunc(number) == number {
			return int(number), true
		}
	case json.Number:
		parsed, err := strconv.Atoi(string(number))
		if err == nil && parsed >= 0 {
			return parsed, true
		}
	case int:
		if number >= 0 {
			return number, true
		}
	}
	return 0, false
}

// findConversation walks a decoded payload looking for the object that
// carries the chat mapping.
func findConversation(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		if looksLikeConversation(typed) {
			return typed
		}
		for _, child := range typed {
			if conversation := findConversation(child); conversation != nil {
				return conversation
			}
		}
	case []any:
		for _, child := range typed {
			if conversation := findConversation(child); conversation != nil {
				return conversation
			}
		}
	}
	return nil
}

func looksLikeConversation(value map[string]any) bool {
	if _, ok := value["mapping"].(map[string]any); !ok {
		return false
	}
	_, linear := value["linear_conversation"].([]any)
	_, current := value["current_node"]
	_, conversationID := value["conversation_id"]
	return linear || current || conversationID
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
