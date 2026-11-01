package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestZCodeUsage_ParseAndFetch(t *testing.T) {
	sampleJSON := `{
		"ok": true,
		"source": "balance",
		"server_time": 1793380000,
		"rows": [
			{"model":"GLM-5.3","models":["glm-5.3"],"entitlement_id":"ent_53","plan":"zcode-v3-start-plan-0817","plan_name":"ZCode Start Plan","period":"daily","period_start":1793376000,"period_end":1793462399,"total_units":3000000,"used_units":22,"remaining_units":2999978,"available_units":2999978},
			{"model":"GLM-5.3-Flash","models":["glm-5.3-flash"],"entitlement_id":"ent_wk","plan":"wk","plan_name":"ZCODE WEEKEND BUILD","period":"one_time","period_end":null,"total_units":500000000,"used_units":0,"remaining_units":500000000,"available_units":500000000},
			{"model":"GLM-5.3-Flash","models":["glm-5.3-flash"],"entitlement_id":"ent_flash","plan":"sp","plan_name":"ZCode Start Plan","period":"daily","period_start":1793376000,"period_end":1793462399,"total_units":5000000,"used_units":27927,"remaining_units":4972073,"available_units":4972073}
		],
		"errors": []
	}`
	t.Run("ParseRows", func(t *testing.T) {
		var m map[string]any
		if err := json.Unmarshal([]byte(sampleJSON), &m); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		res := parseZCodeQuotaResponse(m)
		if len(res.quotas) != 3 {
			t.Fatalf("expected 3 quota keys, got %d: %v", len(res.quotas), res.quotas)
		}
		daily, ok := res.quotas["GLM-5.3 (daily)"].(map[string]any)
		if !ok {
			t.Fatalf("missing daily row: %v", res.quotas)
		}
		if daily["used"].(float64) != 22 || daily["total"].(float64) != 3000000 {
			t.Errorf("daily counters wrong: %v", daily)
		}
		if daily["resetAt"] == nil {
			t.Error("daily row must carry a resetAt from period_end")
		}
		weekend, ok := res.quotas["GLM-5.3-Flash (one_time)"].(map[string]any)
		if !ok {
			t.Fatalf("missing weekend row: %v", res.quotas)
		}
		if weekend["remaining"].(float64) != 500000000 {
			t.Errorf("weekend remaining wrong: %v", weekend)
		}
		if weekend["resetAt"] != nil {
			t.Errorf("one_time row must not carry a resetAt: %v", weekend["resetAt"])
		}
		if res.plan != "ZCode Start Plan" {
			t.Errorf("plan should come from the first row, got %q", res.plan)
		}
	})
	t.Run("ParseEmptyAndBroken", func(t *testing.T) {
		if res := parseZCodeQuotaResponse(nil); res.message == "" {
			t.Error("nil payload must yield a message")
		}
		if res := parseZCodeQuotaResponse(map[string]any{"rows": []any{}}); res.message == "" {
			t.Error("empty rows must yield a message")
		}
		res := parseZCodeQuotaResponse(map[string]any{"rows": []any{
			map[string]any{"model": "X", "total_units": 100.0, "used_units": 40.0},
		}})
		q := res.quotas["X"].(map[string]any)
		if q["remaining"].(float64) != 60 {
			t.Errorf("missing remaining_units should fall back to total-used: %v", q)
		}
	})
	t.Run("URLFromBase", func(t *testing.T) {
		cases := map[string]string{
			"http://127.0.0.1:20131/v1":                  "http://127.0.0.1:20131/v1/quota",
			"http://127.0.0.1:20131/v1/":                 "http://127.0.0.1:20131/v1/quota",
			"http://127.0.0.1:20131/v1/messages":         "http://127.0.0.1:20131/v1/quota",
			"http://127.0.0.1:20131/v1/chat/completions": "http://127.0.0.1:20131/v1/quota",
			"http://127.0.0.1:20131":                     "http://127.0.0.1:20131/v1/quota",
			"":                                           "",
		}
		for in, want := range cases {
			if got := zcodeQuotaURLFromBase(in); got != want {
				t.Errorf("zcodeQuotaURLFromBase(%q) = %q, want %q", in, got, want)
			}
		}
	})
	t.Run("FetchAgainstBridgeStub", func(t *testing.T) {
		var gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/quota" {
				http.NotFound(w, r)
				return
			}
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(sampleJSON))
		}))
		defer srv.Close()
		res := fetchZCodeUsage(context.Background(), "tok-123", srv.URL+"/v1")
		if gotAuth != "Bearer tok-123" {
			t.Errorf("bridge token must be sent as bearer, got %q", gotAuth)
		}
		if len(res.quotas) != 3 || res.plan != "ZCode Start Plan" {
			t.Errorf("fetch result wrong: %+v", res)
		}
		// No token configured: request still goes out without an Authorization.
		gotAuth = "unset"
		res = fetchZCodeUsage(context.Background(), "", srv.URL+"/v1")
		if gotAuth != "" {
			t.Errorf("empty key must send no Authorization, got %q", gotAuth)
		}
		if len(res.quotas) != 3 {
			t.Errorf("empty key fetch failed: %+v", res)
		}
	})
	t.Run("Dispatch", func(t *testing.T) {
		data := map[string]any{"baseUrl": "http://127.0.0.1:20131/v1", "apiKey": "k"}
		if _, ok := fetchProviderUsage(context.Background(), "anthropic-compatible-zcode", data); !ok {
			t.Error("zcode node id must dispatch to the zcode fetcher")
		}
		if _, ok := fetchProviderUsage(context.Background(), "anthropic-compatible-other", data); ok {
			t.Error("other custom nodes must stay unhandled")
		}
	})
}
