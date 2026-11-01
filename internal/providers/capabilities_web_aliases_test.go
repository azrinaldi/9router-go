package providers

import "testing"

// A provider's alias must resolve to the same capability table as its
// canonical id.
//
// GetCapabilitiesForModel looks the table up by the exact string the request
// named, so a client that wrote `ds-web/…` and a client that wrote
// `deepseek-web/…` get different answers unless the alias is registered too.
// The failure this pins is silent and expensive: a vision-capable provider
// whose alias row is missing falls through to the generic name patterns,
// which describe the text-only API surface, and the capacity adapter then
// reroutes an image to a different provider instead of serving it.
func TestWebProviderAliasesShareTheCapabilityTable(t *testing.T) {
	tests := []struct {
		alias     string
		canonical string
	}{
		{alias: "ds-web", canonical: "deepseek-web"},
		{alias: "gweb", canonical: "gemini-web"},
	}
	for _, tt := range tests {
		t.Run(tt.canonical, func(t *testing.T) {
			if got := ResolveAlias(tt.alias); got != tt.canonical {
				t.Fatalf("ResolveAlias(%q) = %q, want %q", tt.alias, got, tt.canonical)
			}
			canonicalTable, ok := providerCapabilities[tt.canonical]
			if !ok {
				t.Fatalf("no capability table for %q", tt.canonical)
			}
			aliasTable, ok := providerCapabilities[tt.alias]
			if !ok {
				t.Fatalf("no capability table for alias %q — a request naming the alias falls through to the generic patterns", tt.alias)
			}
			if len(aliasTable) != len(canonicalTable) {
				t.Fatalf("%q publishes %d models, canonical %q publishes %d",
					tt.alias, len(aliasTable), tt.canonical, len(canonicalTable))
			}
			for model, want := range canonicalTable {
				got, ok := aliasTable[model]
				if !ok {
					t.Errorf("%q is missing model %q", tt.alias, model)
					continue
				}
				if got.Vision != want.Vision || got.Tools != want.Tools || got.Reasoning != want.Reasoning {
					t.Errorf("%s/%s = vision:%t tools:%t reasoning:%t, canonical %s/%s = vision:%t tools:%t reasoning:%t",
						tt.alias, model, got.Vision, got.Tools, got.Reasoning,
						tt.canonical, model, want.Vision, want.Tools, want.Reasoning)
				}
			}
			// And the lookup a request actually performs must agree, which is
			// the form the capacity adapter asks in.
			for model := range canonicalTable {
				if byAlias, byCanonical := GetCapabilitiesForModel(tt.alias, model), GetCapabilitiesForModel(tt.canonical, model); byAlias.Vision != byCanonical.Vision {
					t.Errorf("GetCapabilitiesForModel(%q, %q).Vision = %t, canonical = %t",
						tt.alias, model, byAlias.Vision, byCanonical.Vision)
				}
			}
		})
	}
}

// TestDeepSeekWebAdvertisesImageInput pins what the web surface can actually
// do, because the executor now uploads an image and a client only sends one
// when the model advertises it.
func TestDeepSeekWebAdvertisesImageInput(t *testing.T) {
	for _, provider := range []string{"deepseek-web", "ds-web"} {
		for _, model := range GetProviderModels(provider) {
			caps := GetCapabilitiesForModel(provider, model)
			if !caps.Vision {
				t.Errorf("%s/%s: the site's file service takes an image, so vision must be advertised", provider, model)
			}
			// The web surface has no attachment slot for these, and a flag
			// that is set without a wire for it makes a client send a request
			// the provider cannot serve.
			if caps.AudioInput || caps.VideoInput || caps.PDF {
				t.Errorf("%s/%s: audio, video and PDF have no slot on the web surface; got %+v", provider, model, caps)
			}
			if !caps.Tools {
				t.Errorf("%s/%s: tool calling works through the prompt and must be advertised", provider, model)
			}
		}
	}
}
