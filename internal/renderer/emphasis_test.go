package renderer

import (
	"strings"
	"testing"
)

func TestChatStrongBoundaries(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ source, want string }{
		{"**Tokio：**丢弃 `JoinHandle`", "<strong>Tokio：</strong>丢弃"},
		{"**Smol： **丢弃 `Task`", "<strong>Smol： </strong>丢弃"},
		{"这是**不同的语义。**后续", "<strong>不同的语义。</strong>后续"},
		{"**normal** and *emphasis*", "<strong>normal</strong> and <em>emphasis</em>"},
		{"`**Tokio：**丢弃`", "<code>**Tokio：**丢弃</code>"},
		{"\\*\\*Tokio：\\*\\*丢弃", "**Tokio：**丢弃"},
	}
	for _, tc := range cases {
		got, err := r.renderMarkdown(tc.source)
		if err != nil || !strings.Contains(got, tc.want) {
			t.Errorf("%q: %s %v; want %s", tc.source, got, err, tc.want)
		}
	}
	got, err := r.renderMarkdown("```text\n**Tokio：**丢弃\n```")
	if err != nil || strings.Contains(got, "<strong>") || !strings.Contains(got, "**Tokio：**") {
		t.Fatalf("fenced code changed: %s %v", got, err)
	}
}
