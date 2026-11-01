package executor

import (
	"context"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
)

// Image input on the DeepSeek Web lane.
//
// The completion endpoint has no inline-image field, so an image only reaches
// the model through a separate upload whose id the turn names. Every test here
// pins one fact a client depends on: the image is actually uploaded, the upload
// carries its own proof of work, the id reaches `ref_file_ids`, and the prompt
// says where the picture belongs.

// deepSeekTestImageDataURL is a one-pixel PNG, small enough to inline and real
// enough that the decode path is exercised rather than bypassed.
const deepSeekTestImageDataURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// deepSeekImageMessage builds a user message carrying text and the inline PNG.
func deepSeekImageMessage(text string) map[string]any {
	return map[string]any{
		"role": "user",
		"content": []map[string]any{
			{"type": "text", "text": text},
			{"type": "image_url", "image_url": map[string]any{"url": deepSeekTestImageDataURL}},
		},
	}
}

// deepSeekRunTurn drives one non-streaming turn against the fake upstream.
func deepSeekRunTurn(t *testing.T, upstream *deepSeekFakeUpstream, body []byte) string {
	t.Helper()
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:    context.Background(),
		Client: server.Client(),
		Config: deepSeekTestConfig(server.URL),
		APIKey: "user-token-image",
		Body:   body,
	}
	if err := ForwardDeepSeekWeb(rec, req); err != nil {
		t.Fatalf("ForwardDeepSeekWeb: %v", err)
	}
	return rec.Body.String()
}

// TestForwardDeepSeekWebUploadsImage proves an image reaches the model as an
// attachment rather than as a dropped content part.
//
// The three facts a client depends on, in one turn: the bytes were posted to
// the file service, the id it returned travelled in `ref_file_ids`, and the
// prompt names the attachment so the model knows the picture belongs to the
// text.
func TestForwardDeepSeekWebUploadsImage(t *testing.T) {
	upstream := newDeepSeekFakeUpstream(t,
		`data: {"p":"response","v":{"response":{"fragments":[{"type":"ANSWER","content":"an orange pixel"}]}}}`+"\n\n",
		`data: {"p":"response/status","v":"FINISHED"}`+"\n\n",
	)
	body := deepSeekTestBody(t, "deepseek-v4-flash",
		map[string]any{"role": "user", "content": []map[string]any{
			{"type": "text", "text": "what colour is this?"},
			{"type": "image_url", "image_url": map[string]any{"url": deepSeekTestImageDataURL}},
		}},
	)
	answer := deepSeekRunTurn(t, upstream, body)

	if got := upstream.countCalls(deepSeekFileUploadPath); got != 1 {
		t.Fatalf("file uploads = %d, want 1 (calls: %v)", got, upstream.callPaths())
	}
	// The upload's own challenge must name the upload path: a challenge minted
	// for the completion is refused there, and that refusal is a 4xx the client
	// never sees as "your image did not arrive".
	if upstream.uploadPowTarget != deepSeekFileUploadPath {
		t.Errorf("upload pow target = %q, want %q", upstream.uploadPowTarget, deepSeekFileUploadPath)
	}
	if got := upstream.countCalls(deepSeekFileFetchPath); got == 0 {
		t.Error("upload never polled the file service; a turn sent while the file is still parsing is answered without the image")
	}
	completion := upstream.body(t, deepSeekCompletionPath)
	refs, ok := completion["ref_file_ids"].([]any)
	if !ok || len(refs) != 1 {
		t.Fatalf("ref_file_ids = %v, want one uploaded id", completion["ref_file_ids"])
	}
	if refs[0] != "file-1" {
		t.Errorf("ref_file_ids[0] = %v, want the id the upload returned", refs[0])
	}
	prompt, _ := completion["prompt"].(string)
	if !strings.Contains(prompt, "[Attached image 1]") {
		t.Errorf("prompt does not name the attachment, so the model cannot tie it to the text; prompt: %q", prompt)
	}
	if !strings.Contains(answer, "an orange pixel") {
		t.Errorf("answer missing; body: %s", answer)
	}
}

// TestForwardDeepSeekWebUploadsImageInContentOrder proves the ids follow the
// order the images appear in, which is the order the prompt's markers name.
//
// A gateway that uploaded in a different order than it numbered the markers
// would attach the wrong picture to the wrong sentence — silently, because the
// model simply answers about whatever it was given.
func TestForwardDeepSeekWebUploadsImageInContentOrder(t *testing.T) {
	upstream := newDeepSeekFakeUpstream(t,
		`data: {"p":"response","v":{"response":{"fragments":[{"type":"ANSWER","content":"ok"}]}}}`+"\n\n",
		`data: {"p":"response/status","v":"FINISHED"}`+"\n\n",
	)
	body := deepSeekTestBody(t, "deepseek-v4-flash",
		map[string]any{"role": "user", "content": []map[string]any{
			{"type": "image_url", "image_url": map[string]any{"url": deepSeekTestImageDataURL}},
			{"type": "text", "text": "compare these two"},
			{"type": "image_url", "image_url": map[string]any{"url": deepSeekTestImageDataURL}},
		}},
	)
	deepSeekRunTurn(t, upstream, body)
	if got := upstream.countCalls(deepSeekFileUploadPath); got != 2 {
		t.Fatalf("file uploads = %d, want 2 (calls: %v)", got, upstream.callPaths())
	}
	refs, _ := upstream.body(t, deepSeekCompletionPath)["ref_file_ids"].([]any)
	if len(refs) != 2 {
		t.Fatalf("ref_file_ids = %v, want two ids", refs)
	}
	prompt, _ := upstream.body(t, deepSeekCompletionPath)["prompt"].(string)
	if !strings.Contains(prompt, "[Attached image 1]") || !strings.Contains(prompt, "[Attached image 2]") {
		t.Errorf("prompt does not number both attachments; prompt: %q", prompt)
	}
}

// TestForwardDeepSeekWebTextOnlyTurnIsUnchanged proves an ordinary turn pays
// nothing for image support.
//
// This is the regression that matters most: the upload path must not run, and
// the prompt must stay byte-for-byte what it was before images existed.
func TestForwardDeepSeekWebTextOnlyTurnIsUnchanged(t *testing.T) {
	upstream := newDeepSeekFakeUpstream(t,
		`data: {"p":"response","v":{"response":{"fragments":[{"type":"ANSWER","content":"hi"}]}}}`+"\n\n",
		`data: {"p":"response/status","v":"FINISHED"}`+"\n\n",
	)
	deepSeekRunTurn(t, upstream, deepSeekTestBody(t, "deepseek-v4-flash",
		map[string]any{"role": "user", "content": "hello"}))
	for _, path := range []string{deepSeekFileUploadPath, deepSeekFileFetchPath} {
		if got := upstream.countCalls(path); got != 0 {
			t.Errorf("%s called %d times on a text-only turn, want 0", path, got)
		}
	}
	completion := upstream.body(t, deepSeekCompletionPath)
	refs, _ := completion["ref_file_ids"].([]any)
	if len(refs) != 0 {
		t.Errorf("ref_file_ids = %v, want empty on a text-only turn", completion["ref_file_ids"])
	}
	if prompt, _ := completion["prompt"].(string); strings.Contains(prompt, "[Attached image") {
		t.Errorf("text-only prompt carries an attachment marker; prompt: %q", prompt)
	}
}

// TestForwardDeepSeekWebWaitsForFileProcessing proves the turn does not name an
// id the service has not finished parsing.
//
// The site accepts the id immediately and answers without the picture, which a
// client cannot tell from a model that ignored it. The wait is what turns that
// silent failure into either an image or an error.
func TestForwardDeepSeekWebWaitsForFileProcessing(t *testing.T) {
	upstream := newDeepSeekFakeUpstream(t,
		`data: {"p":"response","v":{"response":{"fragments":[{"type":"ANSWER","content":"seen"}]}}}`+"\n\n",
		`data: {"p":"response/status","v":"FINISHED"}`+"\n\n",
	)
	// PENDING first, then SUCCESS on the second read: the upload must keep
	// polling rather than send the turn on the first answer.
	upstream.fileStatus = "PENDING"
	body := deepSeekTestBody(t, "deepseek-v4-flash", deepSeekImageMessage("look"))
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)

	// The poll interval is production timing; this test flips the status after
	// the first read so the wait is exercised without sitting out the interval
	// repeatedly.
	go func() {
		for upstream.countCalls(deepSeekFileFetchPath) == 0 {
			// Wait for the first poll to land before changing the answer.
		}
		upstream.mu.Lock()
		upstream.fileStatus = "SUCCESS"
		upstream.mu.Unlock()
	}()

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:    context.Background(),
		Client: server.Client(),
		Config: deepSeekTestConfig(server.URL),
		APIKey: "user-token-wait",
		Body:   body,
	}
	if err := ForwardDeepSeekWeb(rec, req); err != nil {
		t.Fatalf("ForwardDeepSeekWeb: %v", err)
	}
	if got := upstream.filePollCount; got < 2 {
		t.Errorf("file status polls = %d, want at least 2 (the first PENDING read must not end the wait)", got)
	}
	refs, _ := upstream.body(t, deepSeekCompletionPath)["ref_file_ids"].([]any)
	if len(refs) != 1 || refs[0] != "file-1" {
		t.Errorf("ref_file_ids = %v, want the id once the file reported SUCCESS", refs)
	}
}

// TestForwardDeepSeekWebRejectsUnreadableImage proves a bad image is refused
// before the turn is sent, with a status the client can act on.
func TestForwardDeepSeekWebRejectsUnreadableImage(t *testing.T) {
	upstream := newDeepSeekFakeUpstream(t,
		`data: {"p":"response/status","v":"FINISHED"}`+"\n\n",
	)
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	body := deepSeekTestBody(t, "deepseek-v4-flash",
		map[string]any{"role": "user", "content": []map[string]any{
			{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,not-base64!!"}},
		}},
	)
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:    context.Background(),
		Client: server.Client(),
		Config: deepSeekTestConfig(server.URL),
		APIKey: "user-token-bad-image",
		Body:   body,
	}
	err := ForwardDeepSeekWeb(rec, req)
	if err == nil {
		t.Fatal("a malformed inline image was accepted")
	}
	if got := upstream.countCalls(deepSeekCompletionPath); got != 0 {
		t.Errorf("completion called %d times, want 0 — an unreadable image is refused before the turn", got)
	}
	if !strings.Contains(err.Error(), "not valid base64") {
		t.Errorf("error = %q, want it to name the malformed image", err)
	}
}

// TestDeepSeekMessageTextWithImageMarkers pins the marker rendering on its own,
// because the prompt is what the model actually reads.
func TestDeepSeekMessageTextWithImageMarkers(t *testing.T) {
	image := func() map[string]any {
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}}
	}
	tests := []struct {
		name    string
		content any
		want    string
	}{
		{
			name:    "plain string is untouched",
			content: "just text",
			want:    "just text",
		},
		{
			name: "marker leads the text",
			content: []map[string]any{
				{"type": "text", "text": "what is this"},
				image(),
			},
			want: "[Attached image 1]\n\nwhat is this",
		},
		{
			name: "an image with no text still gets a marker",
			content: []map[string]any{
				image(),
			},
			want: "[Attached image 1]",
		},
		{
			name: "text-only parts carry no marker",
			content: []map[string]any{
				{"type": "text", "text": "no picture here"},
			},
			want: "no picture here",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			counter := 0
			encoded := mustJSON(t, tt.content)
			if got := deepSeekMessageTextWithImageMarkers(encoded, &counter); got != tt.want {
				t.Errorf("text = %q, want %q", got, tt.want)
			}
		})
	}
	// The counter is shared across a conversation, so two messages number
	// their attachments consecutively rather than restarting at one.
	counter := 0
	if got := deepSeekMessageTextWithImageMarkers(mustJSON(t, []map[string]any{image()}), &counter); got != "[Attached image 1]" {
		t.Errorf("first marker = %q", got)
	}
	if got := deepSeekMessageTextWithImageMarkers(mustJSON(t, []map[string]any{image()}), &counter); got != "[Attached image 2]" {
		t.Errorf("second marker = %q, want the counter to continue", got)
	}
}

// TestWebImageSourcesReadsEverySpelling proves the three client shapes the
// parser accepts are still accepted, because a client that sends one of them
// and gets no image has no way to tell why.
func TestWebImageSourcesReadsEverySpelling(t *testing.T) {
	inline := base64.StdEncoding.EncodeToString([]byte("png"))
	tests := []struct {
		name    string
		content any
		want    string
	}{
		{
			name:    "openai image_url object",
			content: []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/a.png"}}},
			want:    "https://example.com/a.png",
		},
		{
			name:    "openai image_url string",
			content: []map[string]any{{"type": "image_url", "image_url": "https://example.com/b.png"}},
			want:    "https://example.com/b.png",
		},
		{
			name:    "responses input_image",
			content: []map[string]any{{"type": "input_image", "image_url": "https://example.com/c.png"}},
			want:    "https://example.com/c.png",
		},
		{
			name: "claude image block",
			content: []map[string]any{{
				"type":   "image",
				"source": map[string]any{"type": "base64", "media_type": "image/png", "data": inline},
			}},
			want: "data:image/png;base64," + inline,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sources := webImageSources(mustJSON(t, tt.content))
			if len(sources) != 1 {
				t.Fatalf("sources = %v, want one", sources)
			}
			if sources[0].URL != tt.want {
				t.Errorf("source URL = %q, want %q", sources[0].URL, tt.want)
			}
		})
	}
}

// TestForwardDeepSeekWebImageFailureIsNotAGatewayFault proves an upload refused
// for a credential reason keeps its status, so the router rotates the account
// instead of reporting a transport hiccup.
func TestForwardDeepSeekWebImageFailureIsNotAGatewayFault(t *testing.T) {
	upstream := newDeepSeekFakeUpstream(t,
		`data: {"p":"response/status","v":"FINISHED"}`+"\n\n",
	)
	upstream.fileStatus = "FAIL"
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:    context.Background(),
		Client: server.Client(),
		Config: deepSeekTestConfig(server.URL),
		APIKey: "user-token-file-fail",
		Body:   deepSeekTestBody(t, "deepseek-v4-flash", deepSeekImageMessage("look")),
	}
	err := ForwardDeepSeekWeb(rec, req)
	if err == nil {
		t.Fatal("a file the service could not process was accepted")
	}
	if !strings.Contains(err.Error(), "could not be processed") {
		t.Errorf("error = %q, want it to name the processing failure", err)
	}
	if got := upstream.countCalls(deepSeekCompletionPath); got != 0 {
		t.Errorf("completion called %d times, want 0 — a failed upload must not send the turn", got)
	}
}

// TestDeepSeekFileFromEnvelope pins the envelope unwrapping, including the
// refusal that arrives with HTTP 200.
func TestDeepSeekFileFromEnvelope(t *testing.T) {
	file, err := deepSeekFileFromEnvelope([]byte(`{"code":0,"data":{"biz_code":0,"biz_data":{"id":"abc","status":"PENDING"}}}`))
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if file.ID != "abc" || file.Status != "PENDING" {
		t.Errorf("file = %+v, want the id and status the envelope carried", file)
	}
	// A missing id is the failure mode that produces a turn with no image.
	if _, err := deepSeekFileFromEnvelope([]byte(`{"code":0,"data":{"biz_code":0,"biz_data":{}}}`)); err == nil {
		t.Error("an answer with no file id was accepted")
	}
	// The site refuses inside the envelope with HTTP 200.
	var apiErr *deepSeekAPIError
	_, err = deepSeekFileFromEnvelope([]byte(`{"code":40003,"msg":"Authorization Failed","data":null}`))
	if err == nil {
		t.Fatal("an envelope error was accepted as a file")
	}
	if !asDeepSeekAPIError(err, &apiErr) || apiErr.Code != deepSeekCodeAuthFailed {
		t.Errorf("error = %v, want the envelope's own code", err)
	}
}

// asDeepSeekAPIError is errors.As specialised to this package's error, kept
// here so the test reads as the assertion it is.
func asDeepSeekAPIError(err error, target **deepSeekAPIError) bool {
	typed, ok := err.(*deepSeekAPIError)
	if ok {
		*target = typed
	}
	return ok
}

// TestForwardDeepSeekWebImageTurnKeepsSessionAffinity proves an image turn is
// still one session: the upload happened before the completion, not as a
// second conversation.
func TestForwardDeepSeekWebImageTurnKeepsSessionAffinity(t *testing.T) {
	upstream := newDeepSeekFakeUpstream(t,
		`data: {"p":"response","v":{"response":{"fragments":[{"type":"ANSWER","content":"ok"}]}}}`+"\n\n",
		`data: {"p":"response/status","v":"FINISHED"}`+"\n\n",
	)
	deepSeekRunTurn(t, upstream, deepSeekTestBody(t, "deepseek-v4-flash", deepSeekImageMessage("what is this")))
	if got := upstream.countCalls("/api/v0/chat_session/create"); got != 1 {
		t.Errorf("sessions created = %d, want 1 — the upload must not open its own conversation", got)
	}
	sessionID, _ := upstream.body(t, deepSeekCompletionPath)["chat_session_id"].(string)
	if sessionID != "session-1" {
		t.Errorf("completion session = %q, want the session the upload used", sessionID)
	}
	// The upload's Referer names the conversation's page, the way the browser
	// issues it from that page.
	if got := upstream.headers[deepSeekFileUploadPath].Get("Referer"); !strings.HasSuffix(got, "/a/chat/s/session-1") {
		t.Errorf("upload Referer = %q, want the conversation's page", got)
	}
	if got := upstream.headers[deepSeekFileUploadPath].Get("X-File-Size"); got == "" {
		t.Error("upload carried no X-File-Size header")
	}
}
