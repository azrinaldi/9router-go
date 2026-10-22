package chat

// Helpers that exercise the limit-resolution rule in isolation.
//
// appendConnectionModels reads its live catalogue from a network resolver, so
// a unit test cannot drive it directly. These helpers apply the same rule to
// one entry: a catalogue that states its own limits wins over every static
// table, everything else keeps upstream's table fallback.

import (
	"testing"

	"9router/proxy/internal/providers"
)

// appendModelEntryWithLive resolves one model the way appendConnectionModels
// does and returns the resulting entry.
func appendModelEntryWithLive(t *testing.T, providerID, modelID string, live []LiveModel, caps *LiveCapabilities) ModelInfoObject {
	t.Helper()
	liveByID := make(map[string]LiveModel, len(live))
	for _, entry := range live {
		liveByID[entry.ID] = entry
	}

	fullID := providerID + "/" + modelID
	liveEntry, hasLive := liveByID[modelID]

	var ctxLen, maxOut int
	if hasLive && liveEntry.LimitsAuthoritative {
		ctxLen = liveEntry.ContextLength
		maxOut = liveEntry.MaxOutput
	}
	if ctxLen == 0 || maxOut == 0 {
		detail := providers.GetCapabilitiesDetailForModel(providerID, modelID)
		if ctxLen == 0 && detail.ContextWindow > 0 {
			ctxLen = detail.ContextWindow
		}
		if maxOut == 0 {
			maxOut = detail.MaxOutput
		}
	}
	ctxLen, maxOut = fillTokenLimitGaps(ctxLen, maxOut, modelID, fullID)

	return ModelInfoObject{
		ID:                  fullID,
		Object:              "model",
		OwnedBy:             providerID,
		Capabilities:        caps,
		ContextLength:       optionalTokenLimit(ctxLen),
		MaxCompletionTokens: optionalTokenLimit(maxOut),
	}
}
