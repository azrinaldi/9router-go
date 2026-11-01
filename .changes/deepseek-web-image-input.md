### feat(deepseek-web): take images through the site's own file service

`deepseek-web` answered an image request as if the client had sent no image at
all: the completion endpoint has no inline-image field, `ref_file_ids` was
hard-coded to an empty slice, and `deepSeekMessageText` dropped every non-text
content part. A client that sent a screenshot got a confident answer about
nothing, which is worse than an error.

The web surface does accept images — its composer uploads them and names the
returned file id in the turn — so the executor now does the same thing a
browser does:

- **The upload is its own request with its own proof of work.** The challenge
  is bound to the path it will be presented to, so a challenge minted for the
  completion is refused on the upload path. `requestPow` gained a
  `target_path` parameter and the upload asks for its own.
- **The upload is not usable immediately.** The file service parses
  asynchronously and a turn that names an id still being parsed is answered
  without the picture. The executor polls `/api/v0/file/fetch_files` until the
  service reports `SUCCESS`, and reports a rejection, an audit refusal or a
  timeout as the failure it is — before the turn is sent, not as an answer
  that silently ignored the image.
- **`ref_file_ids` carries the ids, in content order**, and the prompt numbers
  the same attachments (`[Attached image 1]`) so the model can tell which
  picture a sentence is about. The counter is shared across the conversation,
  so a replayed transcript keeps naming the attachments it was given.
- **Vision is advertised, which is what makes a client send the image at all.**
  `providerCapabilities["deepseek-web"]` now states what the web surface
  actually does: images and tools, no audio, video or PDF, and the thinking
  levels the fixture records for this lane (the v4 family can disable
  thinking, the reasoner names cannot, `deepseek-chat` publishes none).

**A latent alias bug surfaced while proving this on a live turn.** The capacity
adapter asks `GetCapabilitiesForModel("ds-web", …)` — the id the client wrote —
but only the canonical `deepseek-web` table was registered, so the lookup fell
through to the generic `*deepseek-v4*` pattern row (the text-only API shape),
reported `vision:false`, and rerouted every image to another provider. The
executor was never reached. `providerCapabilities["ds-web"]` is now a mirror of
the canonical table, exactly as `cx` mirrors `codex`, and `gweb` got the same
mirror because it carried the identical latent bug. A test pins that every
web-provider alias publishes the same table as its canonical id.

Reading an image out of a conversation is not a per-provider problem, so it
lives in one place now: `web_image_input.go` holds the three client spellings
(OpenAI `image_url`, Responses `input_image`, Claude `image`), the data-URL and
remote-URL resolution, the SSRF-guarded fetch and the size bound.
`gemini_web_image.go` keeps only its own upload — the content-push multipart,
the resumable fallback and the slot-0 tuple — and calls the shared parser.
`geminiWebRequestUUID` folded into the shared `webRequestUUID` for the same
reason.

Verified against the live site on 2026-10-27 with a signed-in connection, one
turn each:

| Request | Before | After |
|---|---|---|
| non-streamed `image_url`, solid-blue PNG | answered about nothing | `"Blue"` |
| streamed, two images in one turn | dropped both | `"Blue"`, ids in content order |
| Anthropic `/v1/messages`, `image` block | `"the inline image is not valid base64"` from a quoting bug in the test, then dropped | `"Blue"` |
| remote `https://…/Cat03.jpg` | dropped | `"Orange tabby cat looking sideways."` |
| text-only turn | — | unchanged, no upload, no marker |
| `capacity adapter auto-switch` log lines | 1 per image request | 0 — the lane now serves its own images |

Gates: `go build ./...`, `go vet` clean, `go test ./internal/providers/
./internal/proxy/executor/ -count=1` green. Full `go test ./...` carries four
pre-existing failures in `web`, `internal/handlers` and `cmd/9router-go` that
need a built `web/dist`; they reproduce on a clean checkout of this commit.

No live-site limit is claimed: only one request per shape was run, the upstream
file service's own maximum size and lifetime were not probed, and the bound
here (20 MiB per image) is a router resource policy rather than an asserted
upstream ceiling.
