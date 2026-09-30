package connector

import (
	"slices"
	"testing"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

func TestPinnedMessageIDs(t *testing.T) {
	// Discord lists the newest pin first; the room keeps them oldest first, as Element shows them.
	got := pinnedMessageIDs(&discordgo.ChannelMessagePinsList{Items: []*discordgo.MessagePin{
		{Message: &discordgo.Message{ID: "3"}},
		{Message: nil},
		{Message: &discordgo.Message{ID: "1"}},
	}})
	want := []networkid.MessageID{discordid.MakeMessageID("1"), discordid.MakeMessageID("3")}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := pinnedMessageIDs(&discordgo.ChannelMessagePinsList{}); got == nil || len(got) != 0 {
		t.Errorf("no pins must be an empty list, not nil (nil leaves the room's pins alone): %v", got)
	}
}
