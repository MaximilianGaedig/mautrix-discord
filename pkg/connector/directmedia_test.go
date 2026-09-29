package connector

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"
)

/** The parameter Discord puts on an attachment URL: a big-endian unix second, hex. */
func expiryParam(at time.Time) string {
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], uint32(at.Unix()))
	return hex.EncodeToString(raw[:])
}

func TestParseAttachmentExpiryParam(t *testing.T) {
	// Relative to now, not a fixed date. The original pinned 2026-03-21, which was in the future
	// when it was written and is in the past now - and the parser rejects a past expiry, so the
	// test began failing on that date and would have failed for good.
	soon := time.Now().Add(24 * time.Hour).Truncate(time.Second)

	got := parseAttachmentExpiryParam(expiryParam(soon))
	if !got.Equal(soon) {
		t.Errorf("a valid expiry parsed to %s, want %s", got, soon)
	}
}

func TestParseAttachmentExpiryParamRejects(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name  string
		param string
	}{
		// The window is the point of the function: a URL whose expiry has passed, or is absurdly
		// far off, is not one to trust a timestamp from.
		{"an expiry that has passed", expiryParam(now.Add(-time.Hour))},
		{"an expiry more than a year off", expiryParam(now.Add(400 * 24 * time.Hour))},
		{"not hex", "nothex!!"},
		{"too few bytes", "69be62"},
		{"too many bytes", "69be621400"},
		{"empty", ""},
	} {
		if got := parseAttachmentExpiryParam(tc.param); !got.IsZero() {
			t.Errorf("%s parsed to %s, want the zero time", tc.name, got)
		}
	}
}
