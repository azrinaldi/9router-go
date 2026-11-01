package executor

import (
	"encoding/json"
	"fmt"
	"net/http"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/translator"
	"9router/proxy/internal/zcode"
)

// ZCode is the ZCode desktop client's start-plan lane: the connection's token
// is a plan JWT, the endpoint is the desktop client's own
// /api/v1/zcode-plan/anthropic/v1/messages, and the gateway content-inspects
// the request — the exact 3-block identity system and the client's header
// face (internal/zcode). Any other shape answers 405 3012 or lands on the
// paid lane (429 1113), so the generic forwarder must never touch this
// provider; this executor is the only caller.
//
// Upstream speaks Claude Messages, so the body arrives here already
// Claude-shaped (fallback.go converts a Chat Completions caller before
// dispatch because the registry entry declares FormatClaude) and the reply is
// relayed/translated by the shared Claude upstream handlers.
func ForwardZcode(w http.ResponseWriter, req *Request) error {
	body := req.Body
	if !req.ClaudeClient {
		// A Chat Completions caller whose model lands here without having
		// gone through the upstreamClaude conversion (direct executor
		// dispatch) is converted to a spec-compliant Messages payload, the
		// same call fallback.go makes pre-dispatch.
		body = EnsureClaudeMessagesFor(body, req.Provider, req.ModelName)
	} else {
		body = translator.SanitizeClaudePassthrough(body, translator.ClaudeIntentionalPrefill(req.Body))
	}

	var bodyMap map[string]any
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		return fmt.Errorf("ForwardZcode: parse request body: %w", err)
	}
	// The plan gateway meters per session and content-inspects the system
	// blocks: ShapePlanBody swaps in the canonical 3-block identity system
	// (the client's own prompt rides block 3), sets metadata.user_id and the
	// max_tokens default.
	// req.SessionID is the handler session id; a stable per-conversation
	// session_<32hex> is derived from it so retries reuse the id.
	resp, err := zcode.SendAnthropic(zcode.SendOpts{
		Body:            bodyMap,
		JWT:             req.APIKey,
		ClientSessionID: req.SessionID + "|" + handlerutil.GetSessionID(req.Ctx),
		Context:         reqCtx(req),
		HTTP:            req.Client,
	})
	if err != nil {
		return fmt.Errorf("ForwardZcode: %w", err)
	}
	defer resp.Body.Close()

	forwardUpstreamResponseHeaders(w, resp.Header)

	if resp.StatusCode != http.StatusOK {
		return upstreamStatusError(resp)
	}

	// The reply is Claude Messages (JSON or SSE). UpstreamClaude is the flag
	// that means exactly "upstream answers Claude while the client sent an
	// OpenAI body" (fallback.go sets it as upstreamClaude && !claudeNative);
	// TranslateResp is not, it is false on this lane for both client kinds. A
	// Claude client (ClaudeClient) is relayed its own wire format untouched.
	if req.UpstreamClaude {
		if req.IsStream {
			return handleClaudeMessagesStream(w, req, resp.Body)
		}
		return handleClaudeMessagesNonStream(w, req, resp.Body)
	}
	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, false, req.ResponseBuf)
}
