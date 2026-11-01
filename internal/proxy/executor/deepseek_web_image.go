package executor

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/proxy"
)

// Image input on DeepSeek Web.
//
// The completion endpoint has no inline-image field. An image travels as an
// *attachment*: the bytes go to the file service first, the answer is a file
// id, and the turn names that id in `ref_file_ids`. The upload is therefore a
// separate request with its own failure, reported before the turn is sent
// rather than as an answer that silently ignored the picture.
//
// Two upstream details decide whether this works at all:
//
//   - The upload carries its own proof of work. The challenge names the path
//     it is good for (`target_path`), and a challenge minted for the
//     completion is rejected on the upload path. So the upload asks for a
//     challenge of its own, and the completion asks for another.
//   - The file is not usable the moment it is uploaded. The service processes
//     it asynchronously and a turn that names an id still being parsed answers
//     without the image, so the upload polls until the service reports
//     SUCCESS.
//
// Reference: snake-aabb-wtf/deepseek-web2api-free deepseek_files.py and
// OmniRoute open-sse/executors/deepseek-web/image-upload.ts — same endpoint,
// same headers, same poll. Both verify the shape against the live site.

const (
	// deepSeekFileUploadPath is where an attachment is posted. It is also the
	// `target_path` its proof of work must name.
	deepSeekFileUploadPath = "/api/v0/file/upload_file"
	// deepSeekFileFetchPath reports whether an uploaded file finished
	// processing.
	deepSeekFileFetchPath = "/api/v0/file/fetch_files"
	// deepSeekFilePollInterval is how long the upload waits between status
	// reads. The service answers in about a second for a normal image.
	deepSeekFilePollInterval = 700 * time.Millisecond
	// deepSeekFileProcessingTimeout bounds the wait for one file to become
	// usable. The site's own composer gives up far earlier; a gateway holds
	// the request open only as long as a real answer could still arrive.
	deepSeekFileProcessingTimeout = 60 * time.Second
)

// deepSeekImagePolicy is what this upload surface tolerates.
var deepSeekImagePolicy = webImagePolicy{
	label:    "deepseek-web",
	maxBytes: webMaxImageBytes,
}

// deepSeekFile is one entry of the file service's status answer.
type deepSeekFile struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	ErrorCode   int    `json:"error_code"`
	AuditResult string `json:"audit_result"`
}

// deepSeekUploadImages uploads every image in the conversation and returns the
// file ids the turn names, in the order the images appear.
//
// The ids are what `ref_file_ids` carries. The order is the conversation's
// order, because that is the order the prompt's markers name.
func deepSeekUploadImages(ctx context.Context, turn deepSeekTurn, accessToken, sessionID string, messages []deepSeekMessage) ([]string, error) {
	images, err := webCollectImages(ctx, deepSeekImagePolicy, messages)
	if err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(images))
	for _, image := range images {
		id, err := turn.uploadFile(ctx, accessToken, sessionID, image)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// uploadFile posts one image and waits until the service reports it usable.
func (t deepSeekTurn) uploadFile(ctx context.Context, accessToken, sessionID string, image webImage) (string, error) {
	powAnswer, err := t.requestPowFor(ctx, accessToken, sessionID, deepSeekFileUploadPath)
	if err != nil {
		return "", err
	}
	boundary := "----DeepSeekWebUpload" + webRequestUUID()
	body := deepSeekMultipartBody(boundary, image)
	headers := t.webHeaders(map[string]string{
		"content-type":      "multipart/form-data; boundary=" + boundary,
		"x-ds-pow-response": powAnswer,
		// The upload announces the size it is sending, the way the site's own
		// composer does before the body is written.
		"x-file-size": fmt.Sprintf("%d", len(image.Data)),
		// The upload is issued from the conversation's page, so the Referer
		// names that page rather than the site root.
		"referer": deepSeekChatPageURL(t.origin, sessionID),
	})
	headers["authorization"] = "Bearer " + accessToken
	headers["origin"] = t.origin
	response, err := t.send(ctx, http.MethodPost, t.endpoints.fileUpload, body, headers)
	if err != nil {
		return "", deepSeekUploadFailure(err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, deepSeekMaxBodyBytes))
	if err != nil {
		return "", fmt.Errorf("deepseek-web: read the upload answer: %w", err)
	}
	file, err := deepSeekFileFromEnvelope(raw)
	if err != nil {
		return "", err
	}
	return deepSeekAwaitFile(ctx, t, accessToken, file)
}

// deepSeekFileFromEnvelope unwraps the upload answer's envelope.
//
// The file service nests its payload under `data.biz_data` exactly as the
// chat endpoints do, and an error inside that envelope arrives with HTTP 200 —
// so a reader that trusted the status alone would treat a refusal as a file
// with no id.
func deepSeekFileFromEnvelope(raw []byte) (deepSeekFile, error) {
	var envelope deepSeekEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return deepSeekFile{}, fmt.Errorf("deepseek-web: the upload answer was not a JSON envelope: %w", err)
	}
	if envelope.Code != 0 {
		return deepSeekFile{}, &deepSeekAPIError{Code: envelope.Code, Message: envelope.Msg}
	}
	var holder struct {
		BizCode int           `json:"biz_code"`
		BizData *deepSeekFile `json:"biz_data"`
	}
	if err := json.Unmarshal(envelope.Data, &holder); err != nil {
		return deepSeekFile{}, fmt.Errorf("deepseek-web: the upload answer carried no file: %w", err)
	}
	if holder.BizCode != 0 {
		return deepSeekFile{}, &deepSeekAPIError{Code: holder.BizCode, Message: "the file service rejected the image"}
	}
	if holder.BizData == nil || strings.TrimSpace(holder.BizData.ID) == "" {
		return deepSeekFile{}, fmt.Errorf("deepseek-web: the upload answer carried no file id")
	}
	file := *holder.BizData
	if file.Status == "" {
		file.Status = "PENDING"
	}
	return file, nil
}

// deepSeekAwaitFile polls the file service until the image is usable.
//
// A turn sent while the file is still being parsed is answered without the
// image, and the client has no way to tell that from a model that ignored it.
// So the wait happens here, where the failure can still be reported.
func deepSeekAwaitFile(ctx context.Context, turn deepSeekTurn, accessToken string, file deepSeekFile) (string, error) {
	deadline := time.Now().Add(deepSeekFileProcessingTimeout)
	for {
		switch {
		case file.Status == "SUCCESS":
			return file.ID, nil
		case file.ErrorCode != 0:
			return "", deepSeekFailure(http.StatusBadGateway,
				"deepseek-web: the file service rejected the image")
		case strings.Contains(file.Status, "FAIL"),
			strings.Contains(file.Status, "ERROR"),
			strings.Contains(file.Status, "REJECT"),
			strings.Contains(file.Status, "INVALID"),
			strings.Contains(file.Status, "UNSUPPORT"):
			return "", deepSeekFailure(http.StatusBadGateway,
				"deepseek-web: the image could not be processed")
		case file.AuditResult == "fail", file.AuditResult == "reject", file.AuditResult == "block":
			return "", deepSeekFailure(http.StatusBadRequest,
				"deepseek-web: the image was rejected by the site's audit")
		}
		if time.Now().After(deadline) {
			return "", deepSeekFailure(http.StatusGatewayTimeout,
				"deepseek-web: the image was still being processed after "+deepSeekFileProcessingTimeout.String())
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(deepSeekFilePollInterval):
		}
		next, err := turn.fetchFile(ctx, accessToken, file.ID)
		if err != nil {
			return "", err
		}
		file = next
	}
}

// fetchFile reads one file's processing status.
//
// The status answer nests its payload one level deeper than the chat
// endpoints, under `data.biz_data.files`, so it is unwrapped here rather than
// by the shared envelope reader — which returns `data` and nothing more.
func (t deepSeekTurn) fetchFile(ctx context.Context, accessToken, fileID string) (deepSeekFile, error) {
	url := t.endpoints.fileFetch + "?file_ids=" + url.QueryEscape(fileID)
	data, err := t.do(ctx, http.MethodGet, url, accessToken, nil, nil)
	if err != nil {
		return deepSeekFile{}, err
	}
	var holder struct {
		BizCode int `json:"biz_code"`
		BizData struct {
			Files []deepSeekFile `json:"files"`
		} `json:"biz_data"`
	}
	if err := json.Unmarshal(data, &holder); err != nil {
		return deepSeekFile{}, fmt.Errorf("deepseek-web: the file status answer was unreadable: %w", err)
	}
	if holder.BizCode != 0 {
		return deepSeekFile{}, &deepSeekAPIError{Code: holder.BizCode, Message: "the file service refused the status query"}
	}
	for _, file := range holder.BizData.Files {
		if file.ID == fileID {
			return file, nil
		}
	}
	// An answer that omits the file it was asked about is not a status of
	// PENDING; nothing will ever report on it.
	return deepSeekFile{}, fmt.Errorf("deepseek-web: the file status answer omitted the uploaded image")
}

// deepSeekMultipartBody renders the multipart envelope the upload posts.
//
// It is written by hand rather than with mime/multipart so the boundary and
// the body are produced from one call: a transport that receives a boundary
// it did not write into the body is a request no browser made.
func deepSeekMultipartBody(boundary string, image webImage) []byte {
	var b bytes.Buffer
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Disposition: form-data; name=\"file\"; filename=\"" + image.Name + "\"\r\n")
	b.WriteString("Content-Type: " + image.MIME + "\r\n\r\n")
	b.Write(image.Data)
	b.WriteString("\r\n--" + boundary + "--\r\n")
	return b.Bytes()
}

// deepSeekUploadFailure turns a transport refusal into the status the router
// acts on, so an upload rejected by a dead credential is not retried as a
// gateway fault.
func deepSeekUploadFailure(err error) error {
	var upstream *proxy.UpstreamError
	if errors.As(err, &upstream) && upstream != nil {
		return deepSeekFailure(upstream.StatusCode, "deepseek-web: the image could not be uploaded: "+err.Error())
	}
	return err
}
