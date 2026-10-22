package chat

// A live catalogue that states its own limits must outrank the static
// substring table. Zed's claude-sonnet-5 is 1M context / 128k output, while
// the table matches the "sonnet" family and reports 200k / 8k — publishing
// the table's numbers makes a client believe it has a quarter of the window
// it actually has.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

func TestZedLiveLimitsOutrankStaticTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/client/llm_tokens":
			_, _ = w.Write([]byte(`{"token":"h.p.s"}`))
		case "/models":
			_, _ = w.Write([]byte(`{"models":[
				{"id":"claude-sonnet-5","provider":"anthropic","max_token_count":1000000,"max_output_tokens":128000,"supports_thinking":true},
				{"id":"gpt-6.1-sol","provider":"open_ai","max_token_count":400000,"max_output_tokens":128000,"supports_thinking":true}
			]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	previous := providers.ZedCloudBaseURL()
	providers.SetZedCloudBaseURL(srv.URL)
	defer providers.SetZedCloudBaseURL(previous)

	live := []LiveModel{
		{
			ID: "claude-sonnet-5", ContextLength: 1000000, MaxOutput: 128000,
			Capabilities:        &LiveCapabilities{Thinking: true, Agentic: true},
			LimitsAuthoritative: true,
		},
	}
	caps := &LiveCapabilities{Thinking: true, Agentic: true}
	got := appendModelEntryWithLive(t, "zd", "claude-sonnet-5", live, caps)

	if got.ContextLength == nil || *got.ContextLength != 1000000 {
		t.Errorf("context_length = %v, want 1000000 (live, not the table's 200000)", derefInt(got.ContextLength))
	}
	if got.MaxCompletionTokens == nil || *got.MaxCompletionTokens != 128000 {
		t.Errorf("max_completion_tokens = %v, want 128000 (live, not the table's 8192)", derefInt(got.MaxCompletionTokens))
	}
}

func TestNonAuthoritativeLiveKeepsTableFallback(t *testing.T) {
	// Kiro publishes a capability block but no limits; upstream reports the
	// table default for those, and that behaviour must survive.
	live := []LiveModel{{
		ID: "claude-sonnet-4-5", ContextLength: 200000,
		Capabilities:        &LiveCapabilities{Thinking: true},
		LimitsAuthoritative: false,
	}}
	caps := &LiveCapabilities{Thinking: true}
	got := appendModelEntryWithLive(t, "kiro", "claude-sonnet-4-5", live, caps)

	if got.ContextLength == nil || *got.ContextLength == 0 {
		t.Errorf("context_length = %v, want the table fallback to fill it", derefInt(got.ContextLength))
	}
	if got.MaxCompletionTokens == nil || *got.MaxCompletionTokens == 0 {
		t.Errorf("max_completion_tokens = %v, want the table fallback to fill it", derefInt(got.MaxCompletionTokens))
	}
}

func derefInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
