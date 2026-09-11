package extractor

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// streamPage builds a share page carrying the React Router reference table.
func streamPage(t *testing.T, table []any) string {
	t.Helper()
	tableJSON, err := json.Marshal(table)
	if err != nil {
		t.Fatal(err)
	}
	return `<!doctype html><html><body><script>streamController.enqueue(` +
		strconv.Quote(string(tableJSON)) + `)</script></body></html>`
}

// decodedConversation is a plain (already resolved) conversation map used to
// construct fixtures.
func decodedConversation() map[string]any {
	return map[string]any{
		"conversation_id": "conv-1",
		"current_node":    "n2",
		"create_time":     1700000000.0,
		"mapping": map[string]any{
			"n1": map[string]any{
				"id":     "n1",
				"parent": nil,
				"message": map[string]any{
					"id":          "m1",
					"author":      map[string]any{"role": "user"},
					"create_time": 1700000000.0,
					"content":     map[string]any{"content_type": "text", "parts": []any{"hello"}},
				},
			},
		},
	}
}

func TestExtractStreamPayload(t *testing.T) {
	// Ref table: element 0 is the root object pointing at the conversation.
	table := []any{
		map[string]any{"pageProps": map[string]any{"conversation": 2.0}},
		"unused",
		decodedConversation(),
	}
	page := streamPage(t, table)
	conv, err := ExtractConversation(page)
	if err != nil {
		t.Fatal(err)
	}
	if conv["conversation_id"] != "conv-1" {
		t.Errorf("unexpected conversation: %v", conv["conversation_id"])
	}
	mapping, ok := conv["mapping"].(map[string]any)
	if !ok || mapping["n1"] == nil {
		t.Error("mapping not decoded")
	}
}

func TestExtractStreamPayloadTitleRef(t *testing.T) {
	conv := decodedConversation()
	conv["title"] = 1.0 // reference into the table
	table := []any{conv, "Referenced Title"}
	page := streamPage(t, table)
	got, err := ExtractConversation(page)
	if err != nil {
		t.Fatal(err)
	}
	if got["title"] != "Referenced Title" {
		t.Errorf("title ref not resolved, got %v", got["title"])
	}
}

func TestExtractNextDataFallback(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"props": map[string]any{"pageProps": map[string]any{"conversation": decodedConversation()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := `<html><head><script id="__NEXT_DATA__" type="application/json">` +
		string(payload) + `</script></head></html>`
	conv, err := ExtractConversation(page)
	if err != nil {
		t.Fatal(err)
	}
	if conv["conversation_id"] != "conv-1" {
		t.Errorf("unexpected conversation %v", conv["conversation_id"])
	}
}

func TestExtractChallenge(t *testing.T) {
	page := `<html><body><div class="challenge-error-text">Enable JavaScript and cookies to continue</div></body></html>`
	_, err := ExtractConversation(page)
	if err == nil || !strings.Contains(err.Error(), "challenge") {
		t.Errorf("expected challenge parse error, got %v", err)
	}
}

func TestExtractMissing(t *testing.T) {
	_, err := ExtractConversation(`<html><body>nothing here</body></html>`)
	if err == nil {
		t.Error("expected parse error")
	}
}

func TestExtractCycleSafe(t *testing.T) {
	// table[0] refers to itself; decoding must terminate.
	table := []any{
		map[string]any{"self": 0.0},
	}
	page := streamPage(t, table)
	if _, err := ExtractConversation(page); err == nil {
		t.Error("expected parse error for cyclic table without conversation")
	}
}
