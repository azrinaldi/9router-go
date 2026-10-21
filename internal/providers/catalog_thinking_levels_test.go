package providers

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// The thinking-effort levels a model accepts are a property of the model, and
// the catalog states them exactly (`reasoning_options[].values`). The
// hand-written pattern table guesses from the model name, and a wrong guess
// either hides a level the model supports or offers one it rejects with a 400.
//
// These tests pin the catalog path: the declared set wins, and it reaches
// both the level resolver and the published /v1/models capabilities.
func TestSyncModelCatalog_ReasoningLevels(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", "etag-levels")
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, map[string]any{
			"opencode": map[string]any{
				"models": map[string]any{
					"minimax-m3.1-flash-free": map[string]any{
						"modalities":       map[string]any{"input": []string{"text", "image"}},
						"limit":            map[string]int{"context": 1000000, "output": 512000},
						"reasoning":        true,
						"reasoning_options": []any{map[string]any{"type": "effort", "values": []string{"low", "medium", "high", "xhigh", "max"}}},
					},
					"deepseek-v4.1-flash-free": map[string]any{
						"limit":             map[string]int{"context": 200000, "output": 32000},
						"reasoning":         true,
						"reasoning_options": []any{map[string]any{"type": "effort", "values": []string{"low", "high", "max"}}},
					},
					"mimo-v2.6-flash-free": map[string]any{
						"limit":      map[string]int{"context": 200000, "output": 32000},
						"reasoning":  true,
						// No reasoning_options: the model reasons but the
						// catalog states no effort set, so none is claimed.
					},
					// A reasoning model whose id matches no pattern rule: the
					// catalog's flag is the only thing that marks it.
					"beam-free": map[string]any{
						"limit":             map[string]int{"context": 1000000, "output": 131072},
						"reasoning":         true,
						"reasoning_options": []any{map[string]any{"type": "effort", "values": []string{"low", "medium", "high", "xhigh", "max"}}},
					},
					// A model the catalog calls non-reasoning.
					"plain-free": map[string]any{
						"limit": map[string]int{"context": 128000, "output": 8192},
					},
				},
			},
		})
	}))
	defer mock.Close()

	tmpFile := t.TempDir() + "/catalog.json"
	base, err := url.Parse(mock.URL)
	if err != nil {
		t.Fatalf("parse mock url: %v", err)
	}
	client := &http.Client{Transport: rewriteTransport{base: base}}
	if err := SyncModelCatalog(context.Background(), client, tmpFile); err != nil {
		t.Fatalf("SyncModelCatalog: %v", err)
	}
	InvalidateCapabilitiesCache()

	t.Run("the declared effort set is what the catalog reports", func(t *testing.T) {
		got := GetCatalogThinkingLevels("opencode", "minimax-m3.1-flash-free")
		want := []string{"low", "medium", "high", "xhigh", "max"}
		if len(got) != len(want) {
			t.Fatalf("levels = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("levels = %v, want %v", got, want)
			}
		}
	})

	t.Run("a model with no declared set claims none", func(t *testing.T) {
		if got := GetCatalogThinkingLevels("opencode", "mimo-v2.6-flash-free"); got != nil {
			t.Errorf("levels = %v, want nil", got)
		}
	})

	t.Run("the node's published alias resolves the same row", func(t *testing.T) {
		// The node ships as a user-created openai-compatible node and renders
		// under its prefix, so "zen/<model>" has to reach the same catalog
		// entry the node id does.
		got := GetCatalogThinkingLevels("zen", "minimax-m3.1-flash-free")
		if len(got) != 5 || got[4] != "max" {
			t.Errorf("levels via the alias = %v, want the 5 declared levels", got)
		}
	})

	t.Run("the resolver prefers the catalog over the pattern table", func(t *testing.T) {
		// The pattern table would answer low|high|max for this family by
		// name; the catalog says five levels, and the catalog is right.
		got := GetThinkingLevels("zen", "minimax-m3.1-flash-free")
		if len(got) != 5 {
			t.Fatalf("levels = %v, want the 5 catalog levels", got)
		}
		if got[0] != "low" || got[4] != "max" {
			t.Errorf("levels = %v, want low..max", got)
		}
	})

	t.Run("the published capabilities carry the levels", func(t *testing.T) {
		// A client that reads /v1/models has to learn the levels from there;
		// without this field the client guesses and the effort picker is
		// either empty or wrong.
		detail := GetCapabilitiesDetailForModel("zen", "minimax-m3.1-flash-free")
		if len(detail.ThinkingLevels) != 5 {
			t.Fatalf("published levels = %v, want 5 entries", detail.ThinkingLevels)
		}
		if detail.ThinkingLevels[4] != "max" {
			t.Errorf("published levels = %v, want the catalog set", detail.ThinkingLevels)
		}
	})

	t.Run("the catalog's reasoning flag reaches the capabilities", func(t *testing.T) {
		// A model whose id matches no pattern rule used to be published as
		// text-only, and a client then offered no thinking control for a model
		// that does think.
		caps := GetCapabilitiesForModel("opencode", "beam-free")
		if !caps.Reasoning {
			t.Error("expected the catalog to mark the model as reasoning")
		}
	})

	t.Run("a model the catalog calls non-reasoning stays non-reasoning", func(t *testing.T) {
		caps := GetCapabilitiesForModel("opencode", "plain-free")
		if caps.Reasoning {
			t.Error("expected the model to stay non-reasoning")
		}
	})
}
