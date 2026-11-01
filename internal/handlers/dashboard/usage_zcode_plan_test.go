package dashboard

import (
	"testing"
)

// parseZCodeQuotaResponse must read the rows the native fetcher emits:
// period from the entitlement, units as plain numbers, resetAt from
// period_end.
func TestParseZCodePlanRows(t *testing.T) {
	res := parseZCodeQuotaResponse(usageJSON([]byte(`{"rows":[
		{"model":"GLM-5.3","plan_name":"ZCode Start Plan","period":"daily","total_units":3000000,"used_units":1200,"remaining_units":2998800,"period_end":1793548799},
		{"model":"GLM-5.3-Flash","plan_name":"ZCODE WEEKEND BUILD","period":"one_time","total_units":500000000,"used_units":431810558,"remaining_units":68189442,"period_end":1793581200}
	]}`)))
	if res.plan != "ZCode Start Plan" {
		t.Fatalf("plan = %q", res.plan)
	}
	if len(res.quotas) != 2 {
		t.Fatalf("want 2 cards, got %d", len(res.quotas))
	}
	card := res.quotas["GLM-5.3 (daily)"].(map[string]any)
	if card["used"] != 1200.0 || card["remaining"] != 2998800.0 {
		t.Errorf("daily card units wrong: %v", card)
	}
	if card["resetAt"] == nil {
		t.Errorf("resetAt not derived from period_end: %v", card["resetAt"])
	}
	weekend := res.quotas["GLM-5.3-Flash (one_time)"].(map[string]any)
	if weekend["remainingPercentage"].(float64) < 13 || weekend["remainingPercentage"].(float64) > 14 {
		t.Errorf("percentage wrong: %v", weekend["remainingPercentage"])
	}
}

// The fetcher maps ReadQuota rows into the bridge's row shape; period comes
// from the entitlement, units pass through, a missing number is nil (never a
// NaN literal — encoding/json/v2 refuses to marshal it).
func TestZCodePlanRowMappingNilNotNaN(t *testing.T) {
	if numberOrNil(float64(7)) != float64(7) {
		t.Fatal("number must pass through")
	}
	if numberOrNil("x") != nil || numberOrNil(nil) != nil {
		t.Fatal("non-number must become nil")
	}
}
