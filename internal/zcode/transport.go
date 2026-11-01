package zcode

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PlanAnthropicPath locates the plan lane.
var PlanAnthropicPath = "/api/v1/zcode-plan/anthropic/v1/messages"

// UpstreamError carries the gateway's status/code to the caller.
type UpstreamError struct {
	Msg    string
	Status int
	Body   any
	Code   any // gateway envelope code (3012/1113/3008/3009/3001)
}

func (e *UpstreamError) Error() string { return e.Msg }

// errorFromResponse maps the gateway's {code,msg} envelope onto an
// UpstreamError with a human message per known code.
func errorFromResponse(status int, body []byte) *UpstreamError {
	var env struct {
		Code  any    `json:"code"`
		Msg   string `json:"msg"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil {
		t := string(body)
		if len(t) > 200 {
			t = t[:200]
		}
		return &UpstreamError{Msg: fmt.Sprintf("upstream %d: %s", status, t), Status: status}
	}
	message := ""
	switch env.Code {
	case float64(3012):
		message = "zcode gateway blocked this request (3012): the system prompt must carry the exact ZCode identity blocks."
	case float64(1113):
		message = "zcode paid lane answered \"insufficient balance\" (1113): the request was not recognized as start-plan traffic."
	case float64(3009):
		message = fmt.Sprintf("model concurrency limit (3009)%s - retry shortly", msgSuffix(env.Msg))
	case float64(3008):
		message = fmt.Sprintf("account concurrency limit (3008)%s - retry shortly", msgSuffix(env.Msg))
	case float64(3001):
		message = fmt.Sprintf("gateway parameter error (3001)%s", msgSuffix(env.Msg))
	case float64(1005):
		message = fmt.Sprintf("zcode plan quota exhausted (1005)%s", msgSuffix(env.Msg))
	default:
		if env.Error != nil && env.Error.Message != "" {
			message = env.Error.Message
		} else {
			message = fmt.Sprintf("upstream %d", status)
		}
	}
	return &UpstreamError{Msg: message, Status: status, Body: env, Code: env.Code}
}

func msgSuffix(msg string) string {
	if strings.TrimSpace(msg) == "" {
		return ""
	}
	return ": " + msg
}

// IsTransient reports gateway responses worth retrying in place: model
// concurrency (3009), 429, 5xx.
func IsTransient(err *UpstreamError) bool {
	if err == nil {
		return false
	}
	if codeIs(err.Code, 3009) {
		return true
	}
	return err.Status == http.StatusTooManyRequests || err.Status >= 500
}

func codeIs(code any, want float64) bool {
	f, ok := code.(float64)
	return ok && f == want
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// LegacyUserID extracts the account uuid from a JWT's user_id claim.
func LegacyUserID(jwt string) string {
	claims, ok := DecodeJWTClaims(jwt)
	if !ok {
		return ""
	}
	if v, _ := claims["user_id"].(string); v != "" {
		return v
	}
	v, _ := claims["sub"].(string)
	return v
}

// DecodeJWTClaims decodes a JWT payload without verifying the signature.
func DecodeJWTClaims(token string) (map[string]any, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	raw, err := json.Marshal(base64URLDecode(parts[1]))
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if json.Unmarshal(raw, &claims) != nil {
		return nil, false
	}
	return claims, true
}

func base64URLDecode(s string) []byte {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	pad := len(s) % 4
	if pad == 2 {
		s += "=="
	} else if pad == 3 {
		s += "="
	} else if pad == 1 {
		return nil
	}
	var buf []byte
	for i := 0; i+4 <= len(s); i += 4 {
		var vals [4]int
		for j := 0; j < 4; j++ {
			c := s[i+j]
			if c == '=' {
				vals[j] = 0
				continue
			}
			idx := strings.IndexByte(alphabet, c)
			if idx < 0 {
				return nil
			}
			vals[j] = idx
		}
		buf = append(buf,
			byte(vals[0]<<2|vals[1]>>4),
			byte(vals[1]<<4|vals[2]>>2),
			byte(vals[2]<<6|vals[3]),
		)
	}
	// Trim the padding bytes the '=' counts implied.
	if len(s) > 0 {
		switch s[len(s)-1] {
		case '=':
			buf = buf[:len(buf)-1]
			if len(s) > 1 && s[len(s)-2] == '=' {
				buf = buf[:len(buf)-1]
			}
		}
	}
	return buf
}

// ShapePlanBody shapes one Anthropic-format body for the plan gateway:
// canonical identity system blocks with the client's own system prompt
// riding block 3, session metadata, and sane sampling defaults.
func ShapePlanBody(body map[string]any, sessionID, deviceID, accountUUID string) (map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	out["system"] = BuildSystem(body["system"])
	meta, _ := out["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["user_id"] = MetadataUserID(deviceID, sessionID, accountUUID)
	out["metadata"] = meta
	if out["max_tokens"] == nil {
		out["max_tokens"] = 8192
	}
	return out, nil
}

// SendOpts carries one upstream call's parameters. JWT is the connection's
// plan token (the zcode OAuth row's apiKey).
type SendOpts struct {
	Body            map[string]any // Anthropic-format request
	JWT             string         // required: the plan JWT
	ClientSessionID string         // conversation affinity key
	Context         context.Context
	HTTP            *http.Client
	UpstreamTimeout time.Duration // hard deadline (0: 5m)
}

// SendAnthropic sends one Anthropic-shaped request to the plan endpoint.
// Transient failures (3009/429/5xx) retry with exponential backoff. The
// response body is NOT consumed here; the caller must close it.
func SendAnthropic(o SendOpts) (*http.Response, error) {
	ctx := o.Context
	if ctx == nil {
		ctx = context.Background()
	}
	jwt := strings.TrimSpace(o.JWT)
	if jwt == "" {
		return nil, &UpstreamError{Msg: "zcode connection has no plan JWT", Status: 500}
	}
	timeout := o.UpstreamTimeout
	if timeout <= 0 {
		if v := osGetenv("ZCODE_UPSTREAM_TIMEOUT_MS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				timeout = time.Duration(n) * time.Millisecond
			}
		}
		if timeout <= 0 {
			timeout = 5 * time.Minute
		}
	}
	client := o.HTTP
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	maxAttempts := 3
	if v := osGetenv("ZCODE_MAX_ATTEMPTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxAttempts = n
		}
	}
	baseMs := 1500
	if v := osGetenv("ZCODE_RETRY_BASE_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			baseMs = n
		}
	}

	var lastErr *UpstreamError
	for n := range maxAttempts {
		res, attemptErr := attemptOnce(ctx, client, jwt, o, timeout)
		if res != nil && res.StatusCode >= 200 && res.StatusCode < 300 {
			return res, nil
		}
		if attemptErr != nil {
			if ctx.Err() != nil {
				return nil, attemptErr // client hung up or deadline
			}
			if ue, ok := attemptErr.(*UpstreamError); ok {
				lastErr = ue
			} else {
				lastErr = &UpstreamError{Msg: attemptErr.Error(), Status: 502}
			}
		} else {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			lastErr = errorFromResponse(res.StatusCode, body)
		}
		if !IsTransient(lastErr) || n >= maxAttempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, &UpstreamError{Msg: "client went away", Status: 499}
		case <-time.After(time.Duration(baseMs*(1<<n)) * time.Millisecond):
		}
	}
	if lastErr == nil {
		lastErr = &UpstreamError{Msg: "zcode upstream request failed", Status: 502}
	}
	return nil, lastErr
}

func osGetenv(key string) string { return os.Getenv(key) }

// attemptOnce performs one upstream POST.
func attemptOnce(ctx context.Context, client *http.Client, bearer string, o SendOpts, timeout time.Duration) (*http.Response, error) {
	deviceID := ResolveDeviceMid()
	if deviceID == "" {
		deviceID = newUUID()
	}
	sessionID := SessionIDFor(o.ClientSessionID, "")
	requestID := newUUID()
	traceID := newUUID()
	queryID := "01a1" + requestID[4:]

	headers := BuildLLMHeaders(Captured().AnthropicVersion, Captured().AnthropicBeta, LLMHeaderOpts{
		SessionID: sessionID, RequestID: requestID, TraceID: traceID, QueryID: queryID,
	})
	headers["authorization"] = "Bearer " + bearer
	headers["content-type"] = "application/json"
	if streamTrue(o.Body) {
		headers["accept"] = "text/event-stream"
	} else {
		headers["accept"] = "application/json"
	}
	payload, err := ShapePlanBody(o.Body, sessionID, deviceID, LegacyUserID(bearer))
	if err != nil {
		return nil, &UpstreamError{Msg: "shape body: " + err.Error(), Status: 500}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, &UpstreamError{Msg: "marshal body: " + err.Error(), Status: 500}
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, Origin()+PlanAnthropicPath, bytes.NewReader(raw))
	if err != nil {
		cancel()
		return nil, &UpstreamError{Msg: "build request: " + err.Error(), Status: 500}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		if callCtx.Err() != nil && ctx.Err() == nil {
			return nil, &UpstreamError{Msg: "zcode upstream timeout", Status: 504}
		}
		return nil, &UpstreamError{Msg: "zcode upstream fetch failed: " + err.Error(), Status: 502}
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

func streamTrue(body map[string]any) bool {
	if body == nil {
		return false
	}
	v, _ := body["stream"].(bool)
	return v
}

// cancelBody runs the request cancel when the consumer closes the body.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelBody) Close() error {
	c.once.Do(c.cancel)
	return c.ReadCloser.Close()
}
