package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Image upload on Gemini Web.
//
// The site accepts an image as an *attachment*, not as text: the bytes go to
// Google's content-push service first and the path it returns is placed in
// the turn's slot 0. The same path is what the site's own composer sends, so
// there is no alternative shape to try.
//
// The upload is a separate request from the turn, so it is a separate failure:
// an image that cannot be uploaded has to be reported before the turn is sent,
// not as an empty answer afterwards.
//
// Reference: azrinaldi/gemini-stealth-bridge internal/gemini/stream.go
// (uploadFileLocked) — the endpoint, the multipart envelope and the
// resumable fallback are that project's shape, verified live against
// gemini.google.com on 2026-10-20.
//
// Reading an image out of the conversation is not this provider's problem and
// does not live here: see web_image_input.go for the shared parser that
// deepseek-web uses too.

// geminiWebUploadEndpoint is the content-push service the site's own composer
// uploads to. The fallback lives on a different host because Google retires
// the primary path periodically.
const (
	geminiWebUploadEndpoint    = "https://content-push.googleapis.com/upload"
	geminiWebUploadFallback    = "https://push.clients6.google.com/upload/"
	geminiWebUploadTenant      = "bard-storage"
	geminiWebUploadDefaultPush = "feeds/mcudyrk2a4khkz"
)

// geminiWebUploadPath is where the content-push service is reached when the
// origin is not the public one — a mirror, or a test's own server.
const geminiWebUploadPath = "/upload"

// geminiWebUploadFallbackPath is where the resumable upload service is
// reached when the origin is not the public one.
const geminiWebUploadFallbackPath = "/upload/"

// geminiWebMaxImageBytes bounds one image.
//
// The site accepts far larger uploads, but a request that carries one is
// already unusual, and an unbounded read would let a client make the router
// hold an arbitrary amount of memory. The ceiling itself is the shared one:
// the same bound applies to any provider that takes an image, and a second
// number here would only be a second limit to keep in step.
const geminiWebMaxImageBytes = webMaxImageBytes

// geminiWebImagePolicy is what this upload surface tolerates. The label is the
// error prefix, so a refusal names the provider the client called.
var geminiWebImagePolicy = webImagePolicy{
	label:    "gemini-web",
	maxBytes: geminiWebMaxImageBytes,
}

// geminiWebAttachment is one uploaded image, in the shape slot 0 expects.
type geminiWebAttachment struct {
	// Path is what content-push returned: an opaque path such as
	// "/contrib_service/ttl_1d/…". The site resolves it server-side.
	Path string
	// Name is the filename the site shows for the attachment.
	Name string
}

// slot renders the attachment as the tuple the turn's slot 0 carries.
func (a geminiWebAttachment) slot() []any {
	return []any{[]string{a.Path}, a.Name}
}

// geminiWebUploadImage uploads one image and returns the path the site
// resolves it by.
//
// Two paths are tried, in the order the site itself uses them: the multipart
// upload to content-push, and the resumable upload to push.clients6. The
// second exists because the first is retired periodically, and an image the
// router cannot upload is an image the model never sees.
func (s *geminiWebSession) uploadImage(ctx context.Context, image webImage) (geminiWebAttachment, error) {
	attachment, err := s.uploadMultipart(ctx, image)
	if err == nil {
		return attachment, nil
	}
	primaryErr := err
	attachment, fallbackErr := s.uploadResumable(ctx, image)
	if fallbackErr == nil {
		return attachment, nil
	}
	return geminiWebAttachment{}, geminiWebFailure(http.StatusBadGateway, fmt.Sprintf(
		"gemini-web: the image could not be uploaded (%s; the fallback upload failed too: %s)",
		primaryErr.Error(), fallbackErr.Error()))
}

// uploadMultipart is the primary path: a multipart form post to
// content-push whose answer is the path.
func (s *geminiWebSession) uploadMultipart(ctx context.Context, image webImage) (geminiWebAttachment, error) {
	boundary := "----GeminiWebUpload" + webRequestUUID()
	body := geminiWebMultipartBody(boundary, image)
	headers := map[string]string{
		"content-type":   "multipart/form-data; boundary=" + boundary,
		"origin":         s.endpoints.origin,
		"referer":        s.endpoints.origin + "/",
		"sec-fetch-site": "cross-site",
		"x-tenant-id":    geminiWebUploadTenant,
		"push-id":        s.pushID(),
	}
	response, err := s.do(ctx, http.MethodPost, s.endpoints.upload, strings.NewReader(body), headers["content-type"], headers)
	if err != nil {
		return geminiWebAttachment{}, err
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: read the upload answer: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: the upload answered HTTP %d", response.StatusCode)
	}
	path := strings.TrimSpace(string(payload))
	if !strings.HasPrefix(path, "/") {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: the upload answer carried no path")
	}
	return geminiWebAttachment{Path: path, Name: image.Name}, nil
}

// geminiWebUploadAttachments uploads every image the conversation names.
//
// The upload is per image and per session: each one is a separate request to
// content-push, and each one produces the path the turn's slot 0 carries.
func geminiWebUploadAttachments(ctx context.Context, session *geminiWebSession, messages []deepSeekMessage) ([]geminiWebAttachment, error) {
	images, err := webCollectImages(ctx, geminiWebImagePolicy, messages)
	if err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, nil
	}
	attachments := make([]geminiWebAttachment, 0, len(images))
	for _, image := range images {
		attachment, err := session.uploadImage(ctx, image)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

// uploadResumable is the fallback path: a two-step Scotrup resumable upload.
func (s *geminiWebSession) uploadResumable(ctx context.Context, image webImage) (geminiWebAttachment, error) {
	initHeaders := map[string]string{
		"content-type":                        "application/x-www-form-urlencoded;charset=UTF-8",
		"origin":                              s.endpoints.origin,
		"referer":                             s.endpoints.origin + "/",
		"sec-fetch-site":                      "same-site",
		"x-tenant-id":                         geminiWebUploadTenant,
		"push-id":                             s.pushID(),
		"x-goog-upload-command":               "start",
		"x-goog-upload-protocol":              "resumable",
		"x-goog-upload-header-content-length": fmt.Sprintf("%d", len(image.Data)),
	}
	response, err := s.do(ctx, http.MethodPost, s.endpoints.uploadFallback,
		strings.NewReader("File name: "+image.Name), initHeaders["content-type"], initHeaders)
	if err != nil {
		return geminiWebAttachment{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: the resumable upload answered HTTP %d", response.StatusCode)
	}
	uploadURL := response.Header.Get("x-goog-upload-url")
	if uploadURL == "" {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: the resumable upload returned no target URL")
	}
	putHeaders := map[string]string{
		"content-type":          "application/x-www-form-urlencoded;charset=UTF-8",
		"origin":                s.endpoints.origin,
		"referer":               s.endpoints.origin + "/",
		"sec-fetch-site":        "same-site",
		"x-tenant-id":           geminiWebUploadTenant,
		"push-id":               s.pushID(),
		"x-goog-upload-command": "upload, finalize",
		"x-goog-upload-offset":  "0",
	}
	putResponse, err := s.do(ctx, http.MethodPost, uploadURL,
		strings.NewReader(string(image.Data)), putHeaders["content-type"], putHeaders)
	if err != nil {
		return geminiWebAttachment{}, err
	}
	defer func() { _ = putResponse.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(putResponse.Body, 1<<20))
	if err != nil {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: read the upload answer: %w", err)
	}
	if putResponse.StatusCode != http.StatusOK {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: the resumable upload answered HTTP %d", putResponse.StatusCode)
	}
	path := strings.TrimSpace(string(payload))
	if !strings.HasPrefix(path, "/") {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: the upload answer carried no path")
	}
	return geminiWebAttachment{Path: path, Name: image.Name}, nil
}

// geminiWebMultipartBody renders the multipart envelope the primary upload
// posts.
func geminiWebMultipartBody(boundary string, image webImage) string {
	var b strings.Builder
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Disposition: form-data; name=\"file\"; filename=\"" + image.Name + "\"\r\n")
	b.WriteString("Content-Type: " + image.MIME + "\r\n\r\n")
	b.Write(image.Data)
	b.WriteString("\r\n--" + boundary + "--\r\n")
	return b.String()
}

// pushID is the feed the upload is announced under.
//
// The page carries one, and the site uses it to tie the upload to the
// session. A page that does not carry one gets the site's own default, which
// is what the reference bridge falls back to as well.
func (s *geminiWebSession) pushID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.page.pushID != "" {
		return s.page.pushID
	}
	return geminiWebUploadDefaultPush
}
