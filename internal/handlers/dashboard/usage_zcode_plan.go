package dashboard

// ZCode native plan quota — the provider "zcode" connections carry the plan
// JWT in data.apiKey (OAuth flow), and the quota readout comes from the
// start-plan billing endpoints directly (internal/zcode port of the bridge's
// quota reader):
//
//	GET https://zcode.z.ai/api/v1/zcode-plan/billing/balance  (control face:
//	    identity headers + UUID X-Device-Mid — without the mid it answers
//	    3001 parameter error)
//	GET https://zcode.z.ai/api/v1/zcode-plan/billing/current  (bare auth —
//	    the control face empties its payload; definitions-only fallback)
//
// Rows merge per entitlement the same way the bridge's own readout did, so
// parseZCodeQuotaResponse renders the cards unchanged.

import (
	"context"
	"encoding/json"

	"9router/proxy/internal/zcode"
)

// fetchZCodePlanUsage reads the billing endpoints for one native zcode
// connection. apiKey is the connection's plan JWT.
func fetchZCodePlanUsage(ctx context.Context, apiKey string) usageResult {
	const plan = "ZCode"
	jwt := trimJWT(apiKey)
	if jwt == "" {
		return usageResult{plan: plan, message: "ZCode connection has no plan token."}
	}
	res, err := zcode.ReadQuota(ctx, jwt, zcode.ResolveDeviceMid())
	if err != nil {
		return usageResult{plan: plan, message: "ZCode error: " + err.Error()}
	}
	rows := make([]map[string]any, 0, len(res.Rows))
	for _, r := range res.Rows {
		row := map[string]any{
			"model":           r.Model,
			"plan_name":       r.PlanName,
			"period":          stringOfAny(r.Period),
			"total_units":     numberOrNil(r.TotalUnits),
			"used_units":      numberOrNil(r.UsedUnits),
			"remaining_units": numberOrNil(r.RemainingUnits),
			"period_end":      numberOrNil(r.PeriodEnd),
		}
		rows = append(rows, row)
	}
	payload := map[string]any{"rows": toAnySlice(rows)}
	raw, err := json.Marshal(payload)
	if err != nil {
		return usageResult{plan: plan, message: "ZCode: " + err.Error()}
	}
	out := parseZCodeQuotaResponse(usageJSON(raw))
	if out.plan == "" {
		out.plan = plan
	}
	if len(rows) == 0 && len(res.Errors) > 0 {
		out.message = "ZCode: " + res.Errors[0]
	}
	return out
}

// trimJWT trims a bearer token the way the chat path normalizes it.
func trimJWT(v string) string {
	if len(v) > 7 {
		if v[:7] == "Bearer " {
			return trimSpaceStr(v[7:])
		}
	}
	return trimSpaceStr(v)
}

func trimSpaceStr(v string) string {
	start, end := 0, len(v)
	for start < end && (v[start] == ' ' || v[start] == '\t' || v[start] == '\n' || v[start] == '\r') {
		start++
	}
	for end > start && (v[end-1] == ' ' || v[end-1] == '\t' || v[end-1] == '\n' || v[end-1] == '\r') {
		end--
	}
	return v[start:end]
}

func stringOfAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// numberOrNil passes JSON numbers through, nil for everything else —
// usageNum treats a missing value the same way it treats the bridge's absent
// fields (compute remaining from total-used). A NaN literal cannot ride the
// JSON response: encoding/json/v2 refuses to marshal it.
func numberOrNil(v any) any {
	if f, ok := v.(float64); ok {
		return f
	}
	return nil
}

func toAnySlice(rows []map[string]any) []any {
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	return out
}
