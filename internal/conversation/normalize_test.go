package conversation

import (
	"strings"
	"testing"
	"time"
)

const testURL = "https://chatgpt.com/share/abc-123"

func conv() map[string]any {
	return map[string]any{
		"title":              "Demo Chat",
		"conversation_id":    "conv-1",
		"current_node":       "n3",
		"create_time":        1700000000.0,
		"default_model_slug": "gpt-4o",
		"mapping": map[string]any{
			"n1": map[string]any{
				"id": "n1",
				"message": map[string]any{
					"id":          "m1",
					"author":      map[string]any{"role": "user"},
					"create_time": 1700000000.0,
					"content": map[string]any{
						"content_type": "text",
						"parts":        []any{"Explain **markdown** please"},
					},
					"metadata": map[string]any{},
				},
			},
			"n2": map[string]any{
				"id":     "n2",
				"parent": "n1",
				"message": map[string]any{
					"id":          "m2",
					"author":      map[string]any{"role": "assistant"},
					"create_time": 1700000060.0,
					"content": map[string]any{
						"content_type": "text",
						"parts":        []any{"Here is code:\n```go\nfmt.Println()\n```"},
					},
					"metadata": map[string]any{
						"content_references": []any{
							map[string]any{"type": "webpage", "url": "https://example.com", "title": "Example"},
							map[string]any{"type": "webpage", "url": "http://insecure.example", "title": "Bad"},
						},
					},
				},
			},
			"n3": map[string]any{
				"id":     "n3",
				"parent": "n2",
				"message": map[string]any{
					"id":          "m3",
					"author":      map[string]any{"role": "assistant"},
					"create_time": 1700000120.0,
					"content": map[string]any{
						"content_type": "code",
						"text":         "print('hi')",
						"language":     "python",
					},
					"metadata": map[string]any{"is_visually_hidden_from_conversation": true},
				},
			},
			"n4": map[string]any{
				"id":     "n4",
				"parent": "n3",
				"message": map[string]any{
					"id":     "m4",
					"author": map[string]any{"role": "system"},
					"content": map[string]any{
						"content_type": "model_editable_context",
						"text":         "secret context",
					},
					"metadata": map[string]any{},
				},
			},
		},
	}
}

func TestNormalizeLinearPath(t *testing.T) {
	snap, err := Normalize(conv(), testURL, Options{Timezone: "UTC"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Title != "Demo Chat" {
		t.Errorf("title = %q", snap.Title)
	}
	if snap.Source.ShareID != "abc-123" || snap.Source.Provider != "chatgpt" {
		t.Errorf("bad source %+v", snap.Source)
	}
	// linear path from current_node n3 walks n1->n2->n3 (hidden filtered).
	if len(snap.Messages) != 2 {
		t.Fatalf("expected 2 visible messages, got %d", len(snap.Messages))
	}
	if snap.Messages[0].Role != "user" || snap.Messages[1].Role != "assistant" {
		t.Errorf("roles wrong: %+v", snap.Messages)
	}
	// assistant message: markdown block + https citation; http citation dropped.
	assistant := snap.Messages[1]
	var hasCitation bool
	for _, b := range assistant.Blocks {
		if b.Type == "citation" {
			hasCitation = true
			if b.URL != "https://example.com" {
				t.Errorf("bad citation url %q", b.URL)
			}
		}
	}
	if !hasCitation {
		t.Error("expected citation block")
	}
	if snap.Metadata.Model != "gpt-4o" {
		t.Errorf("model = %q", snap.Metadata.Model)
	}
	if snap.Metadata.MessageCount != 2 {
		t.Errorf("message_count = %d", snap.Metadata.MessageCount)
	}
}

func TestNormalizeIncludeHidden(t *testing.T) {
	snap, err := Normalize(conv(), testURL, Options{IncludeHidden: true, AllNodes: true, Timezone: "UTC"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// AllNodes + includeHidden: m1, m2, m3 (hidden code msg) — m4 has no content blocks.
	if len(snap.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(snap.Messages))
	}
	var hidden *ConversationMsg
	for i := range snap.Messages {
		if snap.Messages[i].Hidden {
			hidden = &snap.Messages[i]
		}
	}
	if hidden == nil {
		t.Fatal("hidden message missing")
	}
	if hidden.Blocks[0].Type != "code" || hidden.Blocks[0].Language != "python" {
		t.Errorf("hidden code block wrong: %+v", hidden.Blocks[0])
	}
}

func TestNormalizeBadTimezone(t *testing.T) {
	if _, err := Normalize(conv(), testURL, Options{Timezone: "Mars/Olympus"}, time.Now()); err == nil {
		t.Error("expected timezone error")
	}
}

func TestContentHashStable(t *testing.T) {
	a, _ := Normalize(conv(), testURL, Options{Timezone: "UTC"}, time.Now())
	b, _ := Normalize(conv(), testURL, Options{Timezone: "UTC"}, time.Now().Add(time.Hour))
	if ContentHash(a) != ContentHash(b) {
		t.Error("hash must not depend on import time")
	}
	// Different content -> different hash.
	c := conv()
	c["title"] = "Other"
	d, _ := Normalize(c, testURL, Options{Timezone: "UTC"}, time.Now())
	if ContentHash(a) == ContentHash(d) {
		t.Error("different content must hash differently")
	}
	// Rendering metadata is part of the immutable output and must therefore
	// produce a distinct revision when it changes.
	model := *a
	model.Metadata.Model = "gpt-5"
	if ContentHash(a) == ContentHash(&model) {
		t.Error("different model metadata must hash differently")
	}
	zone := *a
	zone.Metadata.Timezone = "Asia/Shanghai"
	if ContentHash(a) == ContentHash(&zone) {
		t.Error("different timezone metadata must hash differently")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Hello World!":           "hello-world",
		"  spaces---everywhere ": "spaces-everywhere",
		"中文标题":                   "conversation",
		"":                       "conversation",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if len(Slugify(strings.Repeat("a b ", 100))) > 60 {
		t.Error("slug too long")
	}
}
