package conversation

import "encoding/json"

// researchReport reads only the final assistant report of the first-party
// research widget. Plans, tool output and other embedded state stay private.
func researchReport(message map[string]any) *ConversationMsg {
	metadata, _ := message["metadata"].(map[string]any)
	sdk, _ := metadata["chatgpt_sdk"].(map[string]any)
	if sdk["attribution_id"] != "connector_openai_deep_research" {
		return nil
	}
	var state map[string]any
	switch value := sdk["widget_state"].(type) {
	case string:
		if json.Unmarshal([]byte(value), &state) != nil {
			return nil
		}
	case map[string]any:
		state = value
	default:
		return nil
	}
	if state["status"] != "completed" {
		return nil
	}
	report, _ := state["report_message"].(map[string]any)
	if report == nil {
		return nil
	}
	result := normalizeMessage(report, false)
	if result == nil || result.Role != "assistant" || result.Process || result.Hidden || result.ID == "" {
		return nil
	}
	result.Research = true
	return result
}

// RestoreResearchReports recovers reports and final image outputs omitted by older normalizers from
// the stored source payload. Existing archived text is never replaced. Reports
// are placed after an archived message from the same turn, before the next user.
func RestoreResearchReports(snapshot *ConversationSnapshot, raw map[string]any) {
	nodes := conversationNodes(raw, snapshot.Metadata.AllNodes)
	existing := map[string]bool{}
	for _, m := range snapshot.Messages {
		existing[m.ID] = true
	}
	for i, node := range nodes {
		message, _ := node["message"].(map[string]any)
		report := researchReport(message)
		if report == nil && finalImageOutput(message) {
			report = normalizeMessage(message, false)
		}
		if report != nil && report.Research && existing[report.ID] {
			for j := range snapshot.Messages {
				if snapshot.Messages[j].ID == report.ID {
					snapshot.Messages[j].Research = true
				}
			}
		}
		if report == nil || existing[report.ID] {
			continue
		}
		// Anchor to an existing message on this branch; do not pull unrelated turns
		// from a newer source payload into an older archived revision.
		anchor := ""
		for j := i - 1; j >= 0; j-- {
			m, _ := nodes[j]["message"].(map[string]any)
			id := stringValue(m["id"])
			if existing[id] {
				anchor = id
				break
			}
		}
		if anchor == "" {
			continue
		}
		pos := -1
		for j, m := range snapshot.Messages {
			if m.ID == anchor {
				pos = j + 1
				break
			}
		}
		if pos < 0 {
			continue
		}
		for pos < len(snapshot.Messages) && snapshot.Messages[pos].Role != "user" {
			pos++
		}
		snapshot.Messages = append(snapshot.Messages, ConversationMsg{})
		copy(snapshot.Messages[pos+1:], snapshot.Messages[pos:])
		snapshot.Messages[pos] = *report
		existing[report.ID] = true
	}
	snapshot.Metadata.MessageCount = len(snapshot.Messages)
}
