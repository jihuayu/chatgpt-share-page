package conversation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func researchFixture(t *testing.T, status string, encoded bool) map[string]any {
	t.Helper()
	report := map[string]any{"id": "report", "author": map[string]any{"role": "assistant"}, "channel": "final", "recipient": "all", "content": map[string]any{"content_type": "text", "parts": []any{"# Final research\n\n| Game | Players |\n| --- | --- |\n| Example | 100 |"}}, "metadata": map[string]any{"content_references": []any{map[string]any{"url": "https://example.com/source", "title": "Source"}}}}
	state := map[string]any{"status": status, "report_message": report, "plan": "private process"}
	var value any = state
	if encoded {
		b, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		value = string(b)
	}
	return map[string]any{"id": "widget", "author": map[string]any{"role": "tool"}, "content": map[string]any{"content_type": "code", "text": "{}"}, "metadata": map[string]any{"chatgpt_sdk": map[string]any{"attribution_id": "connector_openai_deep_research", "widget_state": value}}}
}

func TestDeepResearchReports(t *testing.T) {
	for _, encoded := range []bool{true, false} {
		widget := researchFixture(t, "completed", encoded)
		report := researchReport(widget)
		if report == nil || !report.Research || report.Process || len(report.Blocks) != 2 || !strings.Contains(report.Blocks[0].Content, "| Game |") {
			t.Fatalf("missing report: %+v", report)
		}
		raw := map[string]any{"linear_conversation": []any{
			map[string]any{"message": map[string]any{"id": "q", "author": map[string]any{"role": "user"}, "content": map[string]any{"parts": []any{"question"}}}},
			map[string]any{"message": widget},
			map[string]any{"message": map[string]any{"id": "started", "author": map[string]any{"role": "assistant"}, "content": map[string]any{"parts": []any{"started"}}}},
			map[string]any{"message": map[string]any{"id": "q2", "author": map[string]any{"role": "user"}, "content": map[string]any{"parts": []any{"follow up"}}}},
		}}
		normalized, err := Normalize(raw, "https://chatgpt.com/share/test", Options{}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(normalized.Messages) != 4 || normalized.Messages[2].ID != "report" || normalized.Messages[3].ID != "q2" {
			t.Fatalf("wrong order: %+v", normalized.Messages)
		}
		legacy := &ConversationSnapshot{Messages: []ConversationMsg{{ID: "q", Role: "user", Blocks: []ContentBlock{{Type: "markdown", Content: "archived question"}}}, {ID: "started", Role: "assistant"}, {ID: "q2", Role: "user"}}}
		RestoreResearchReports(legacy, raw)
		legacy.Messages[2].Research = false // r4 archives have text but no presentation marker.
		RestoreResearchReports(legacy, raw)
		if len(legacy.Messages) != 4 || !legacy.Messages[2].Research || legacy.Messages[2].ID != "report" || legacy.Messages[0].Blocks[0].Content != "archived question" {
			t.Fatal("restore duplicated report or changed archive")
		}
	}
}

func TestResearchRejectsProcessAndOtherWidgets(t *testing.T) {
	for _, status := range []string{"in_progress", "failed", ""} {
		if researchReport(researchFixture(t, status, true)) != nil {
			t.Fatal("exposed incomplete report")
		}
	}
	widget := researchFixture(t, "completed", false)
	sdk := widget["metadata"].(map[string]any)["chatgpt_sdk"].(map[string]any)
	sdk["attribution_id"] = "other_app"
	if researchReport(widget) != nil {
		t.Fatal("accepted unknown widget")
	}
	sdk["attribution_id"] = "connector_openai_deep_research"
	sdk["widget_state"] = "broken json"
	if researchReport(widget) != nil {
		t.Fatal("accepted malformed state")
	}
	state := researchFixture(t, "completed", false)["metadata"].(map[string]any)["chatgpt_sdk"].(map[string]any)["widget_state"].(map[string]any)
	state["report_message"].(map[string]any)["channel"] = "analysis"
	sdk["widget_state"] = state
	if researchReport(widget) != nil {
		t.Fatal("accepted analysis")
	}
}
