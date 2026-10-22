package chat

import (
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// The dashboard writes {"vision":false,"reasoning":false} when an operator
// adds a catalogue model without filling the form in. Those are absent
// answers, not denials — registering them would let a blank form overwrite
// the capabilities the provider published.
func TestBlankCustomCapsDoNotOverwriteLive(t *testing.T) {
	h := &ChatHandler{}
	cm := &db.CustomModel{ID: "claude-sonnet-5", Type: "llm", Caps: map[string]bool{"vision": false, "reasoning": false}}
	h.registerCustomModelCaps("zd", "zd", cm)

	got := providersGetCustom("zd", "claude-sonnet-5")
	if got {
		t.Fatal("a blank capability map was registered and would shadow the live catalogue")
	}
}

// A row that does state something must still win: the operator's own
// declaration for this exact endpoint outranks the catalogue.
func TestStatedCustomCapsStillWin(t *testing.T) {
	h := &ChatHandler{}
	cm := &db.CustomModel{ID: "claude-sonnet-5", Type: "llm", Caps: map[string]bool{"vision": true}}
	h.registerCustomModelCaps("zd", "zd", cm)

	if !providersGetCustom("zd", "claude-sonnet-5") {
		t.Fatal("a stated capability was ignored")
	}
}

func providersGetCustom(provider, model string) bool {
	caps, ok := providers.GetCustomModelCaps(provider, model)
	return ok && caps.Vision
}
