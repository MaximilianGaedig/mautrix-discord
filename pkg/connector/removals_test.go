package connector

import (
	"slices"
	"testing"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
	"go.mau.fi/mautrix-discord/pkg/router"
)

func removalTestRoute() *router.Route {
	return &router.Route{
		PortalKey:       networkid.PortalKey{ID: discordid.MakeChannelPortalIDWithID("100"), Receiver: "1"},
		PortalChannelID: "100",
		Uncertain:       true,
	}
}

func TestBulkDeleteEvents(t *testing.T) {
	route := removalTestRoute()
	d := &DiscordClient{}
	evts := d.bulkDeleteEvents(&discordgo.MessageDeleteBulk{
		Messages:  []string{"11", "12", "13"},
		ChannelID: "100",
		GuildID:   "9",
	}, route)
	if len(evts) != 3 {
		t.Fatalf("got %d events for 3 deleted messages", len(evts))
	}
	for i, want := range []string{"11", "12", "13"} {
		evt := evts[i]
		if evt.GetType() != bridgev2.RemoteEventMessageRemove {
			t.Errorf("event %d is a %s, want a message removal", i, evt.GetType())
		}
		if got := evt.GetTargetMessage(); got != discordid.MakeMessageID(want) {
			t.Errorf("event %d removes %q, want %q", i, got, want)
		}
		if evt.GetPortalKey() != route.PortalKey || !evt.PortalReceiverIsUncertain() {
			t.Errorf("event %d doesn't go where the channel is routed: %v", i, evt.GetPortalKey())
		}
		if evt.ShouldCreatePortal() {
			t.Errorf("event %d would create a room just to delete from it", i)
		}
	}
	if evts := d.bulkDeleteEvents(&discordgo.MessageDeleteBulk{ChannelID: "100"}, route); len(evts) != 0 {
		t.Errorf("got %d events for no deleted messages", len(evts))
	}
}

func TestReactionClearEvent(t *testing.T) {
	route := removalTestRoute()
	evt := reactionClearEvent(&discordgo.MessageReaction{MessageID: "11", ChannelID: "100"}, route)
	if evt.GetType() != bridgev2.RemoteEventReactionSync {
		t.Errorf("got a %s, want a reaction sync", evt.GetType())
	}
	if evt.GetTargetMessage() != discordid.MakeMessageID("11") {
		t.Errorf("targets %q", evt.GetTargetMessage())
	}
	if evt.GetPortalKey() != route.PortalKey || !evt.PortalReceiverIsUncertain() {
		t.Errorf("doesn't go where the channel is routed: %v", evt.GetPortalKey())
	}
	// Nobody listed, and the list said to be complete: the bridge removes every reaction it knows.
	data := evt.GetReactions()
	if data == nil || !data.HasAllUsers || len(data.Users) != 0 {
		t.Errorf("want a complete, empty set of reactions, got %+v", data)
	}
}

func TestReactionsWithoutEmoji(t *testing.T) {
	unicode := func(sender, emoji string) *database.Reaction {
		return &database.Reaction{SenderID: networkid.UserID(sender), EmojiID: discordid.MakeEmojiID(emoji), Emoji: emoji}
	}
	existing := []*database.Reaction{
		unicode("1", "👍"),
		unicode("1", "🎉"),
		unicode("2", "👍"),
		unicode("3", "🎉"),
		{SenderID: "4", EmojiID: "blob:555", Emoji: ":blob:"},
		{SenderID: "4", EmojiID: "👍", Emoji: "👍"},
	}

	data := reactionsWithoutEmoji(existing, &discordgo.Emoji{Name: "👍"})
	if data.HasAllUsers {
		t.Error("people who didn't use the emoji must be left alone, so the list can't claim to be complete")
	}
	kept := func(user string) []networkid.EmojiID {
		t.Helper()
		u, ok := data.Users[networkid.UserID(user)]
		if !ok {
			t.Fatalf("user %s isn't in the sync", user)
		}
		if !u.HasAllReactions {
			t.Errorf("user %s: the kept reactions must be all of them, or nothing is removed", user)
		}
		var ids []networkid.EmojiID
		for _, r := range u.Reactions {
			if r.Sender.Sender != networkid.UserID(user) {
				t.Errorf("user %s: kept reaction is from %q", user, r.Sender.Sender)
			}
			ids = append(ids, r.EmojiID)
		}
		return ids
	}
	if got := kept("1"); !slices.Equal(got, []networkid.EmojiID{"🎉"}) {
		t.Errorf("user 1 keeps %v, want only the party popper", got)
	}
	if got := kept("2"); len(got) != 0 {
		t.Errorf("user 2 keeps %v, want nothing", got)
	}
	if got := kept("4"); !slices.Equal(got, []networkid.EmojiID{"blob:555"}) {
		t.Errorf("user 4 keeps %v, want only the custom emoji", got)
	}
	if _, ok := data.Users["3"]; ok {
		t.Error("user 3 never used the emoji and must not be in the sync")
	}

	// A custom emoji is the same emoji after the server renames it.
	data = reactionsWithoutEmoji(existing, &discordgo.Emoji{ID: "555", Name: "renamed"})
	if len(data.Users) != 1 {
		t.Fatalf("got %d users for the custom emoji, want only user 4", len(data.Users))
	}
	if got := kept("4"); !slices.Equal(got, []networkid.EmojiID{"👍"}) {
		t.Errorf("user 4 keeps %v, want only the thumbs up", got)
	}

	// An emoji nobody used changes nothing.
	if data = reactionsWithoutEmoji(existing, &discordgo.Emoji{Name: "🦀"}); len(data.Users) != 0 || data.HasAllUsers {
		t.Errorf("an unused emoji must not touch anyone: %+v", data)
	}
}
