package connector

import (
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2/database"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

func TestPollVoteIDRoundTrip(t *testing.T) {
	id := makePollVoteID("1234567890", "42", time.Unix(100, 5))
	pollID, ok := parsePollVoteID(id)
	if !ok || pollID != "1234567890" {
		t.Errorf("got %q %v", pollID, ok)
	}
	if _, ok = parsePollVoteID("1234567890"); ok {
		t.Error("a Discord message ID is not a vote")
	}
	if _, ok = parsePollVoteID(makePollEndID("1234567890")); ok {
		t.Error("a poll end is not a vote")
	}
	a := makePollVoteID("1", "2", time.Unix(1, 1))
	b := makePollVoteID("1", "2", time.Unix(1, 2))
	if a == b {
		t.Error("two votes by the same user must get different IDs")
	}
}

func TestPollMetadataOf(t *testing.T) {
	if pollMetadataOf(nil) != nil {
		t.Error("nil message")
	}
	if pollMetadataOf(&database.Message{Metadata: &discordid.MessageMetadata{}}) != nil {
		t.Error("message without poll")
	}
	pm := &discordid.PollMetadata{MaxSelections: 1}
	if pollMetadataOf(&database.Message{Metadata: &discordid.MessageMetadata{Poll: pm}}) != pm {
		t.Error("message with poll")
	}
}
