package security

import "testing"

func TestValidateShareURL(t *testing.T) {
	valid := []string{
		"https://chatgpt.com/share/abc-123",
		"https://www.chatgpt.com/share/abc",
		"https://chat.openai.com/share/abc",
		"https://www.chat.openai.com/share/abc-123_extra",
		"https://chatgpt.com/share/abc?foo=bar",
	}
	for _, u := range valid {
		if err := ValidateShareURL(u); err != nil {
			t.Errorf("expected %q valid, got %v", u, err)
		}
	}

	invalid := []string{
		"",
		"chatgpt.com/share/abc",                 // missing scheme
		"http://chatgpt.com/share/abc",          // non-https
		"https://evil.com/share/abc",            // wrong host
		"https://chatgpt.com.evil.com/share/x",  // suffix host
		"https://chatgpt.com/c/abc",             // wrong path
		"https://chatgpt.com/share/",            // empty id
		"https://chatgpt.com:4443/share/abc",    // custom port
		"https://user:pass@chatgpt.com/share/x", // credentials
	}
	for _, u := range invalid {
		if err := ValidateShareURL(u); err == nil {
			t.Errorf("expected %q invalid", u)
		}
	}
}

func TestExtractShareID(t *testing.T) {
	if got := ExtractShareID("https://chatgpt.com/share/6aa3c3d2-1d54-83ec-8cd5-9883f007da29"); got != "6aa3c3d2-1d54-83ec-8cd5-9883f007da29" {
		t.Errorf("unexpected share id %q", got)
	}
	if got := ExtractShareID("https://evil.com/"); got != "" {
		t.Errorf("expected empty share id, got %q", got)
	}
}

func TestTokenRoundTrip(t *testing.T) {
	token, err := NewAdminToken()
	if err != nil {
		t.Fatal(err)
	}
	hash := HashToken(token)
	if !TokenMatches(token, hash) {
		t.Error("token should match its hash")
	}
	if TokenMatches("csp_wrong", hash) {
		t.Error("wrong token must not match")
	}
	if TokenMatches(token, "") {
		t.Error("empty hash must not match")
	}
	other, _ := NewAdminToken()
	if other == token {
		t.Error("tokens must be random")
	}
}
