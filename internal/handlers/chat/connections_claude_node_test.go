// (test file continues below)
package chat

import "testing"

func TestClaudeMessagesURL(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:20131/v1":             "http://127.0.0.1:20131/v1/messages",
		"http://127.0.0.1:20131/v1/":            "http://127.0.0.1:20131/v1/messages",
		"http://127.0.0.1:20131":                "http://127.0.0.1:20131/v1/messages",
		"http://127.0.0.1:20131/v1/messages":    "http://127.0.0.1:20131/v1/messages",
		"https://gw.example.com/anthropic/v1":   "https://gw.example.com/anthropic/v1/messages",
		"https://gw.example.com/some/deep/path": "https://gw.example.com/some/deep/path/v1/messages",
	}
	for in, want := range cases {
		if got := claudeMessagesURL(in); got != want {
			t.Errorf("claudeMessagesURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsAnthropicCompatibleProvider(t *testing.T) {
	for _, id := range []string{"anthropic-compatible", "anthropic-compatible-zcode", "anthropic-compatible-x.y_1"} {
		if !isAnthropicCompatibleProvider(id) {
			t.Errorf("%q must be recognized", id)
		}
	}
	for _, id := range []string{"", "claude", "openai-compatible-chat-gsb", "anthropic-compatiblezz", "my-anthropic-compatible"} {
		if isAnthropicCompatibleProvider(id) {
			t.Errorf("%q must not be recognized", id)
		}
	}
}

func TestIsLoopbackBase(t *testing.T) {
	yes := []string{"http://127.0.0.1:20131/v1", "http://localhost:20131/v1", "http://[::1]:20131/v1"}
	no := []string{"https://api.anthropic.com/v1", "http://10.0.0.5:8080/v1", "http://169.254.169.254/latest", "not a url"}
	for _, in := range yes {
		if !isLoopbackBase(in) {
			t.Errorf("%q should be loopback", in)
		}
	}
	for _, in := range no {
		if isLoopbackBase(in) {
			t.Errorf("%q should not be loopback", in)
		}
	}
}
