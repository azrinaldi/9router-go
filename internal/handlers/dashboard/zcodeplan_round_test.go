package dashboard

import (
	"encoding/json"
	"testing"

	"9router/proxy/internal/zcode"
)

// mergeRows re-exported for the dashboard round-trip test.
func zcodeMergeRows(plans, balances []any) []zcode.Row { return zcode.MergeRows(plans, balances) }

// End-to-end: mergeRows -> fetcher mapping -> parse. The used/remaining numbers
// must survive every hop.
func TestZCodePlanEndToEndNumbers(t *testing.T) {
	raw := `{"plans":[{"name":"ZCODE WEEKEND BUILD","plan_id":"p1","status":"active","entitlements":[{"entitlement_id":"e1","show_name":"GLM-5.3-Flash","period":"one_time","grant_units":500000000}]}],"balances":[{"entitlement_id":"e1","plan_id":"p1","show_name":"GLM-5.3-Flash","total_units":500000000,"used_units":446042160,"remaining_units":53957840,"period":null,"period_end":1793581200}]}`
	var payload struct {
		Plans    []any `json:"plans"`
		Balances []any `json:"balances"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	rows := zcodeMergeRows(payload.Plans, payload.Balances)
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	if rows[0].UsedUnits != 446042160.0 {
		t.Fatalf("row used=%v", rows[0].UsedUnits)
	}
	row := map[string]any{
		"model":           rows[0].Model,
		"plan_name":       rows[0].PlanName,
		"period":          stringOfAny(rows[0].Period),
		"total_units":     numberOrNil(rows[0].TotalUnits),
		"used_units":      numberOrNil(rows[0].UsedUnits),
		"remaining_units": numberOrNil(rows[0].RemainingUnits),
		"period_end":      numberOrNil(rows[0].PeriodEnd),
	}
	out := parseZCodeQuotaResponse(usageJSON(toAnyJSON(t, map[string]any{"rows": []any{row}})))
	card := out.quotas["GLM-5.3-Flash (one_time)"].(map[string]any)
	if card["used"] != 446042160.0 {
		t.Fatalf("card used=%v", card["used"])
	}
	if card["remaining"] != 53957840.0 {
		t.Fatalf("card remaining=%v", card["remaining"])
	}
}

func toAnyJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
