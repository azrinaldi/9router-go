package zcode

import (
	"encoding/json"
	"strings"
	"testing"
)

// The plan gateway content-inspects the system field: block 1 must be exactly
// cliPrefix, block 2 must open with the captured stable text (leading \n
// included), block 3 carries the client prompt. Wrong shape = 405 3012.
func TestBuildSystemThreeBlocks(t *testing.T) {
	asset := Captured()
	if len(asset.CliPrefix) == 0 || len(asset.StablePrefix) < 1500 {
		t.Fatalf("identity asset incomplete: cliPrefix=%d stablePrefix=%d", len(asset.CliPrefix), len(asset.StablePrefix))
	}
	blocks := BuildSystem([]any{
		map[string]any{"type": "text", "text": "Be terse."},
	})
	if len(blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d", len(blocks))
	}
	if blocks[0].Text != asset.CliPrefix {
		t.Error("block 1 is not cliPrefix verbatim")
	}
	if !strings.HasPrefix(blocks[1].Text, "\n") || len(blocks[1].Text) != len(asset.StablePrefix) {
		t.Error("block 2 does not carry the stable text byte-exact (leading \\n included)")
	}
	if !strings.Contains(blocks[2].Text, "Be terse.") {
		t.Error("block 3 lost the client prompt")
	}
	if blocks[1].CacheControl == nil || blocks[1].CacheControl.Type != "ephemeral" ||
		blocks[2].CacheControl == nil || blocks[2].CacheControl.Type != "ephemeral" {
		t.Error("cache_control ephemeral missing on blocks 2/3")
	}
	if blocks[0].CacheControl != nil {
		t.Error("block 1 must not carry cache_control")
	}
}

// ShapePlanBody must round-trip the caller's body and stamp identity fields.
func TestShapePlanBodyStampsIdentity(t *testing.T) {
	in := map[string]any{
		"model":    "glm-5.3-flash",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"system":   "custom prompt",
	}
	out, err := ShapePlanBody(in, "sess-key", "device-uuid", "acct-uuid")
	if err != nil {
		t.Fatal(err)
	}
	if out["model"] != "glm-5.3-flash" || out["max_tokens"] != 8192 {
		t.Errorf("body fields not preserved/ defaulted: model=%v max_tokens=%v", out["model"], out["max_tokens"])
	}
	sys, _ := out["system"].([]Block)
	if len(sys) != 3 {
		t.Fatalf("shaped system must be 3 blocks, got %d", len(sys))
	}
	meta, _ := out["metadata"].(map[string]any)
	uid, _ := meta["user_id"].(string)
	var parsed map[string]string
	if json.Unmarshal([]byte(uid), &parsed) != nil ||
		parsed["device_id"] != "device-uuid" || parsed["account_uuid"] != "acct-uuid" {
		t.Errorf("metadata.user_id malformed: %s", uid)
	}
}

// Session ids are stable per conversation key and hex-shaped.
func TestSessionIDStable(t *testing.T) {
	a := SessionIDFor("conv-1", "")
	b := SessionIDFor("conv-1", "")
	c := SessionIDFor("conv-2", "")
	if a != b || a == c {
		t.Fatalf("session derivation not stable/unique: %s %s %s", a, b, c)
	}
	if !strings.HasPrefix(a, "session_") || len(a) != len("session_")+32 {
		t.Errorf("session id shape wrong: %s", a)
	}
}
