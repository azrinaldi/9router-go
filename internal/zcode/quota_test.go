package zcode

import "testing"

// The balance rows carry period:null; the entitlement definitions (plans)
// carry the real period ("daily"/"one_time"). The merged row must surface the
// entitlement's period, or the dashboard card keys collapse to the bare
// model id and the daily/one_time split disappears.
func TestMergeRowsUsesEntitlementPeriod(t *testing.T) {
	plans := []any{map[string]any{
		"name": "ZCODE WEEKEND BUILD", "plan_id": "p1", "status": "active",
		"entitlements": []any{map[string]any{
			"entitlement_id": "e1", "show_name": "GLM-5.3-Flash",
			"period": "one_time", "grant_units": 500000000.0,
		}},
	}}
	balances := []any{map[string]any{
		"entitlement_id": "e1", "plan_id": "p1", "show_name": "GLM-5.3-Flash",
		"total_units": 500000000.0, "used_units": 431810558.0, "remaining_units": 68189442.0,
		"period": nil, "period_end": 1793581200.0,
	}}
	rows := mergeRows(plans, balances)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.Period != "one_time" {
		t.Errorf("period = %v, want one_time", r.Period)
	}
	if r.PlanName != "ZCODE WEEKEND BUILD" {
		t.Errorf("plan_name = %q", r.PlanName)
	}
	if r.TotalUnits != 500000000.0 || r.UsedUnits != 431810558.0 {
		t.Errorf("units not merged: total=%v used=%v", r.TotalUnits, r.UsedUnits)
	}
}

// An entitlement defined but absent from the usage list still shows its grant.
func TestMergeRowsGrantOnlyEntitlement(t *testing.T) {
	plans := []any{map[string]any{
		"name": "P", "plan_id": "p1",
		"entitlements": []any{map[string]any{
			"entitlement_id": "e2", "show_name": "GLM-5.3", "period": "daily", "grant_units": 3000000.0,
		}},
	}}
	rows := mergeRows(plans, nil)
	if len(rows) != 1 || rows[0].TotalUnits != 3000000.0 {
		t.Fatalf("grant-only row missing: %+v", rows)
	}
}
