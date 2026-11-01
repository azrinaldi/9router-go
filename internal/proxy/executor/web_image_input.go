package executor

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/proxy"
)

// Image input shared by the browser-login providers.
//
// gemini-web and deepseek-web both take an image as an *attachment*: the bytes
// leave the router in an upload request of their own, and the turn names what
// the upload returned. What differs is only the upload: Google's content-push
// answers an opaque path, DeepSeek's file service answers a file id. Reading
// the image out of the conversation is the same problem for both, so it lives
// here rather than in either provider's file — a second copy of this parsing
// would be a second set of spellings to keep in step.

// webMaxImageBytes bounds one image.
//
// The sites accept far larger uploads, but a request that carries one is
// already unusual, and an unbounded read would let a client make the router
// hold an arbitrary amount of memory. 20 MB is the ceiling both reference
// implementations put on one image.
const webMaxImageBytes = 20 << 20

// webImageSource is one image reference, before it is resolved.
type webImageSource struct {
	// URL is the location of the image: a data URL or an http(s) URL.
	URL string
	// Name is the filename to announce, when the client gave one.
	Name string
}

// webImage is one image a turn wants the model to look at.
type webImage struct {
	// Data is the decoded bytes.
	Data []byte
	// MIME is the content type the upload announces.
	MIME string
	// Name is the filename the upload announces.
	Name string
}

// webImagePolicy is what one provider's upload surface tolerates. label is the
// error prefix, so a refusal names the provider the client actually called.
type webImagePolicy struct {
	label    string
	maxBytes int
}

// webCollectImages reads every image out of the conversation.
//
// Three spellings reach an executor, because three clients send this shape:
// OpenAI's `image_url` part (a string or a {url} object), Claude's
// `{type:"image",source:{…}}` block (base64 or a URL), and the Responses API's
// `input_image` part. They are folded into one shape here so the upload path
// has a single input.
//
// A data URL is decoded locally; a remote URL is fetched, because a client
// that sends a link expects the image, not the link. Both are bounded by the
// policy before anything leaves the process.
func webCollectImages(ctx context.Context, policy webImagePolicy, messages []deepSeekMessage) ([]webImage, error) {
	var images []webImage
	for _, message := range messages {
		for _, source := range webImageSources(message.Content) {
			image, err := webResolveImage(ctx, policy, source)
			if err != nil {
				return nil, err
			}
			if image != nil {
				images = append(images, *image)
			}
		}
	}
	return images, nil
}

// webImageSources reads every image reference out of one message's content.
func webImageSources(content jsontext.Value) []webImageSource {
	if len(content) == 0 {
		return nil
	}
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		return nil
	}
	var blocks []map[string]any
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil
	}
	var sources []webImageSource
	for _, block := range blocks {
		if source := webImageSourceOf(block); source != nil {
			sources = append(sources, *source)
		}
	}
	return sources
}

// isWebImagePartType reports whether a content-part type carries an image.
//
// The three spellings are the same three webImageSourceOf reads: OpenAI's
// `image_url`, the Responses API's `input_image`, and Claude's `image` block.
// A caller that only needs to count or mark images asks here rather than
// duplicating the list.
func isWebImagePartType(typ string) bool {
	switch typ {
	case "image_url", "input_image", "image":
		return true
	}
	return false
}

// webImageSourceOf reads one image block in any of the three spellings.
func webImageSourceOf(block map[string]any) *webImageSource {
	typ, _ := block["type"].(string)
	switch typ {
	case "image_url":
		url := webStringOf(block["image_url"])
		if url == "" {
			if object, ok := block["image_url"].(map[string]any); ok {
				url = webStringOf(object["url"])
			}
		}
		if url == "" {
			return nil
		}
		return &webImageSource{URL: url}
	case "input_image":
		url := webStringOf(block["image_url"])
		if url == "" {
			url = webStringOf(block["url"])
			if url == "" {
				url = webStringOf(block["file_url"])
			}
		}
		if url == "" {
			return nil
		}
		return &webImageSource{URL: url}
	case "image":
		source, ok := block["source"].(map[string]any)
		if !ok {
			return nil
		}
		data := webStringOf(source["data"])
		if data != "" {
			mime := webStringOf(source["media_type"])
			if mime == "" {
				mime = "image/png"
			}
			return &webImageSource{URL: "data:" + mime + ";base64," + data}
		}
		url := webStringOf(source["url"])
		if url == "" {
			return nil
		}
		return &webImageSource{URL: url}
	}
	return nil
}

// webStringOf reads a string field, ignoring anything else.
func webStringOf(value any) string {
	text, _ := value.(string)
	return text
}

// webResolveImage turns one reference into bytes.
//
// A remote fetch is bounded in size, restricted to http(s), and dialled
// through the repository's SSRF-guarded transport, so a client cannot make
// the router read a file:// path, reach a private address, or pull an
// unbounded body.
func webResolveImage(ctx context.Context, policy webImagePolicy, source webImageSource) (*webImage, error) {
	raw := strings.TrimSpace(source.URL)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "data:") {
		return webDecodeDataURL(policy, raw, source.Name)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return nil, webImageFailure(policy, http.StatusBadRequest,
			"the image URL could not be read")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, webImageFailure(policy, http.StatusBadRequest,
			"only http and https image URLs are supported")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, webImageFailure(policy, http.StatusBadRequest,
			"the image URL could not be read: "+err.Error())
	}
	request.Header.Set("User-Agent", webRemoteImageUserAgent)
	request.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	response, err := webImageClient().Do(request)
	if err != nil {
		return nil, webImageFailure(policy, http.StatusBadGateway,
			"the image could not be fetched: "+err.Error())
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, webImageFailure(policy, http.StatusBadGateway,
			fmt.Sprintf("the image URL answered HTTP %d", response.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(policy.maxBytes)+1))
	if err != nil {
		return nil, webImageFailure(policy, http.StatusBadGateway,
			"the image could not be read: "+err.Error())
	}
	if len(data) > policy.maxBytes {
		return nil, webImageFailure(policy, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("an image larger than %d bytes is not accepted", policy.maxBytes))
	}
	mime := response.Header.Get("Content-Type")
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	return &webImage{
		Data: data,
		MIME: mime,
		Name: webImageName(source.Name, mime),
	}, nil
}

// webRemoteImageUserAgent is the browser identity the fetch presents.
const webRemoteImageUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"

// webImageClient is the client a remote image is fetched with.
//
// It is a package-level singleton on purpose: the transport pools its
// connections, and one image per turn would otherwise open a connection pool
// per turn.
var webImageClient = sync.OnceValue(func() *http.Client {
	return &http.Client{
		Timeout:   20 * time.Second,
		Transport: proxy.NewSafeTransport(),
	}
})

// webDecodeDataURL decodes a base64 data URL.
func webDecodeDataURL(policy webImagePolicy, raw, name string) (*webImage, error) {
	header, payload, found := strings.Cut(raw, ",")
	if !found {
		return nil, webImageFailure(policy, http.StatusBadRequest,
			"the inline image is not a valid data URL")
	}
	mime := "image/png"
	if strings.HasPrefix(header, "data:") {
		if declared := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64"); declared != "" {
			mime = declared
		}
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, webImageFailure(policy, http.StatusBadRequest,
			"the inline image is not valid base64")
	}
	if len(data) > policy.maxBytes {
		return nil, webImageFailure(policy, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("an image larger than %d bytes is not accepted", policy.maxBytes))
	}
	return &webImage{Data: data, MIME: mime, Name: webImageName(name, mime)}, nil
}

// webImageName names the uploaded file.
//
// The name is what the model sees when it refers to the attachment. A client
// that gave no name gets one derived from the mime type, so the model is never
// told about a file called "image".
func webImageName(name, mime string) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return sanitizedWebFileName(trimmed)
	}
	extension := ".png"
	switch {
	case strings.Contains(mime, "jpeg"), strings.Contains(mime, "jpg"):
		extension = ".jpg"
	case strings.Contains(mime, "webp"):
		extension = ".webp"
	case strings.Contains(mime, "gif"):
		extension = ".gif"
	}
	return "image" + extension
}

// sanitizedWebFileName strips the characters that would break the multipart
// envelope.
func sanitizedWebFileName(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '\r' || r == '\n' {
			return '_'
		}
		return r
	}, name)
}

// webImageFailure builds a refusal that names the provider the client called,
// so an image problem is never reported as a generic gateway failure.
func webImageFailure(policy webImagePolicy, status int, message string) error {
	return proxy.UpstreamFailure(status, policy.label+": "+message)
}

// webRequestUUID mints a random RFC-4122 version-4 uuid.
//
// Two providers echo a per-request uuid inside the request body or a multipart
// boundary, and both need a value that does not repeat across turns. A
// generator that is not crypto-rand is fine here: the value is a request
// identifier, not a secret.
func webRequestUUID() string {
	var raw [16]byte
	value := uint64(time.Now().UnixNano())
	for i := range raw {
		raw[i] = byte(value >> (8 * (i % 8)))
		value = value*6364136223846793005 + 1442695040888963407
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	const hexDigits = "0123456789ABCDEF"
	out := make([]byte, 0, 36)
	for i, b := range raw {
		switch i {
		case 4, 6, 8, 10:
			out = append(out, '-')
		}
		out = append(out, hexDigits[b>>4], hexDigits[b&0x0f])
	}
	return string(out)
}
