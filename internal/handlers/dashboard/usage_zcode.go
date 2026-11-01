package dashboard

// ZCode start-plan quota — GET <bridge baseUrl>/quota
//
// The zcode-bridge (Bridge/zcode-bridge) exposes the ZCode desktop panel's
// billing data as {plan, rows[]} rows: one per plan entitlement, carrying
// token counts and the daily window. The panel sums grants per model, so a
// model entitled by two plans (weekend grant + daily allowance) shows two
// rows; keys stay unique by appending the period.

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

// isCompatibleZCodeProvider matches the zcode bridge node id: composed as
// `anthropic-compatible-<suffix>` with suffix "zcode" (register the node's
// prefix as "zcode" in the dashboard).
func isCompatibleZCodeProvider(provider string) bool {
	return provider == "anthropic-compatible-zcode"
}

// zcodeQuotaURLFromBase maps a connection baseUrl to the bridge's quota
// route. The dashboard stores anthropic-compatible baseUrls without a
// trailing /messages (provider_nodes.go strips it), so ".../v1" becomes
// ".../v1/quota"; a full route or bare host is completed the same way the
// chat path does.
func zcodeQuotaURLFromBase(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return ""
	}
	for _, suffix := range []string{"/chat/completions", "/messages"} {
		base = strings.TrimSuffix(base, suffix)
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/quota"
	}
	return base + "/v1/quota"
}

// fetchZCodeUsage reads the bridge quota endpoint. apiKey is optional: the
// bridge requires a token only when ZCODE_BRIDGE_TOKEN is set, and an empty
// one sends no Authorization header.
func fetchZCodeUsage(ctx context.Context, apiKey, baseURL string) usageResult {
	const plan = "ZCode Bridge"
	usageURL := zcodeQuotaURLFromBase(baseURL)
	if usageURL == "" {
		return usageResult{plan: plan, message: "ZCode bridge baseUrl not set on this connection."}
	}
	headers := map[string]string{"Accept": "application/json", "User-Agent": usageUserAgent}
	if token := strings.TrimSpace(apiKey); token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	status, _, body, err := usageGet(ctx, usageURL, headers)
	if err != nil {
		return usageResult{plan: plan, message: fmt.Sprintf("ZCode error: %v", err)}
	}
	if status == http.StatusUnauthorized {
		return usageResult{plan: plan, message: "ZCode bridge rejected the token. Set the connection key to ZCODE_BRIDGE_TOKEN."}
	}
	if status < 200 || status >= 300 {
		return usageResult{plan: plan, message: fmt.Sprintf("ZCode bridge error (%d).", status)}
	}
	res := parseZCodeQuotaResponse(usageJSON(body))
	if res.plan == "" {
		res.plan = plan
	}
	return res
}

// parseZCodeQuotaResponse normalizes {rows:[{model,plan_name,period,
// total_units,used_units,remaining_units,period_end,...}]} into quota cards.
func parseZCodeQuotaResponse(json map[string]any) usageResult {
	if json == nil {
		return usageResult{message: "ZCode bridge returned no data."}
	}
	rows, _ := json["rows"].([]any)
	if len(rows) == 0 {
		if msg := usageStr(json["error"]); msg != "" {
			return usageResult{message: "ZCode: " + msg}
		}
		return usageResult{message: "ZCode bridge reported no entitlements."}
	}
	quotas := make(map[string]any)
	planName := ""
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if row == nil {
			continue
		}
		model := usageStr(row["model"])
		if model == "" {
			continue
		}
		if planName == "" {
			planName = usageStr(row["plan_name"])
		}
		period := usageStr(row["period"])
		key := model
		if period != "" {
			key = fmt.Sprintf("%s (%s)", model, period)
		}
		total := usageNum(row["total_units"], 0)
		used := usageNum(row["used_units"], 0)
		remaining := usageNum(row["remaining_units"], math.NaN())
		if math.IsNaN(remaining) {
			remaining = total - used
			if remaining < 0 {
				remaining = 0
			}
		}
		pct := 0.0
		if total > 0 {
			pct = remaining / total * 100
			if pct < 0 {
				pct = 0
			}
		}
		var resetAt any
		if end := usageNum(row["period_end"], 0); end > 0 {
			resetAt = time.Unix(int64(end), 0).UTC().Format(time.RFC3339Nano)
		}
		quotas[key] = map[string]any{
			"used":                used,
			"total":               total,
			"remaining":           remaining,
			"remainingPercentage": pct,
			"resetAt":             resetAt,
			"unlimited":           false,
		}
	}
	if planName == "" {
		planName = "ZCode"
	}
	return usageResult{plan: planName, quotas: quotas}
}
