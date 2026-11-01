package zcode

import (
	"regexp"
	"testing"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// billing/balance needs a UUID X-Device-Mid or it answers 3001 and the read
// degrades to the usage-less `current` fallback. With no env mid the reader
// must still send a UUID, stable for the process.
func TestDeviceMidFallbackIsUUID(t *testing.T) {
	t.Setenv("ZCODE_IDENTITY_DEVICE_MID", "")
	mid := deviceMidOrDefault("")
	if !uuidRe.MatchString(mid) {
		t.Fatalf("device mid %q is not a v4 UUID", mid)
	}
	if again := deviceMidOrDefault(""); again != mid {
		t.Fatalf("device mid not stable: %q then %q", mid, again)
	}
	if explicit := deviceMidOrDefault("11111111-1111-4111-8111-111111111111"); explicit != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("explicit mid ignored: %q", explicit)
	}
}
