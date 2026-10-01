package connector

import (
	"encoding/json"
	"testing"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-discord/pkg/discordid"
	"go.mau.fi/mautrix-discord/pkg/router"
)

func TestMarkedUnreadIsHandled(t *testing.T) {
	// bridgev2 drops a room's marked-unread change unless the client says it can handle one.
	if _, ok := any(&DiscordClient{}).(bridgev2.MarkedUnreadHandlingNetworkAPI); !ok {
		t.Error("marking a room unread in Matrix must reach Discord")
	}
	if !discordCaps.MarkAsUnread {
		t.Error("the room features must say that marking unread is bridged")
	}
}

func TestManualAckBody(t *testing.T) {
	for _, tc := range []struct {
		private bool
		want    string
	}{
		{true, `{"manual":true,"mention_count":1}`},
		// The count must be sent as 0 rather than left out: it replaces the channel's badge.
		{false, `{"manual":true,"mention_count":0}`},
	} {
		got, err := json.Marshal(manualAckFor(tc.private))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("private=%v: got %s, want %s", tc.private, got, tc.want)
		}
	}
}

func unreadTestPart(id string, threadRoot string) *database.Message {
	msg := &database.Message{ID: discordid.MakeMessageID(id)}
	if threadRoot != "" {
		msg.ThreadRoot = discordid.MakeMessageID(threadRoot)
	}
	return msg
}

func TestUnreadAckTarget(t *testing.T) {
	for _, tc := range []struct {
		name          string
		parts         []*database.Message
		ackID, newest string
		ok            bool
	}{
		{
			name:  "the message before the newest one",
			parts: []*database.Message{unreadTestPart("300", ""), unreadTestPart("200", ""), unreadTestPart("100", "")},
			ackID: "200", newest: "300", ok: true,
		},
		{
			name:  "several parts of the newest message are one message",
			parts: []*database.Message{unreadTestPart("300", ""), unreadTestPart("300", ""), unreadTestPart("200", "")},
			ackID: "200", newest: "300", ok: true,
		},
		{
			name:  "messages in a thread belong to another channel",
			parts: []*database.Message{unreadTestPart("400", "150"), unreadTestPart("300", ""), unreadTestPart("250", "150"), unreadTestPart("200", "")},
			ackID: "200", newest: "300", ok: true,
		},
		{
			name:  "the only message: just below it",
			parts: []*database.Message{unreadTestPart("300", "")},
			ackID: "299", newest: "300", ok: true,
		},
		{
			name:  "IDs that aren't Discord's are skipped",
			parts: []*database.Message{unreadTestPart("call-log", ""), unreadTestPart("300", ""), unreadTestPart("notice", ""), unreadTestPart("200", "")},
			ackID: "200", newest: "300", ok: true,
		},
		{name: "nothing to mark from", parts: nil},
		{name: "only thread messages", parts: []*database.Message{unreadTestPart("400", "150")}},
	} {
		ackID, newest, ok := unreadAckTarget(tc.parts)
		if ackID != tc.ackID || newest != tc.newest || ok != tc.ok {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q, %v)", tc.name, ackID, newest, ok, tc.ackID, tc.newest, tc.ok)
		}
	}
}

func TestAckMovesBack(t *testing.T) {
	at := func(id string) *discordgo.ReadState {
		return &discordgo.ReadState{ID: "1", LastMessageID: discordgo.StringOrInt(id)}
	}
	for _, tc := range []struct {
		name string
		prev *discordgo.ReadState
		ack  string
		want bool
	}{
		{"back", at("300"), "200", true},
		{"forward", at("200"), "300", false},
		{"the same message again", at("300"), "300", false},
		{"no position known", nil, "200", false},
		// Compared as numbers: as text "1000" would sort before "999".
		{"a longer ID is newer", at("999"), "1000", false},
		{"a shorter ID is older", at("1000"), "999", true},
		{"a position that was never set", at(""), "200", false},
	} {
		if got := ackMovesBack(tc.prev, &discordgo.MessageAck{ChannelID: "1", MessageID: tc.ack}); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAckEvent(t *testing.T) {
	// The events are sent as the logged-in user, whose ID comes from the session.
	state := discordgo.NewState()
	state.User = &discordgo.User{ID: "9"}
	d := &DiscordClient{Session: &discordgo.Session{State: state}}
	route := &router.Route{PortalKey: networkid.PortalKey{ID: discordid.MakeChannelPortalIDWithID("1")}, PortalChannelID: "1"}
	prev := &discordgo.ReadState{ID: "1", LastMessageID: "300"}

	unread, ok := d.ackEvent(prev, &discordgo.MessageAck{ChannelID: "1", MessageID: "200"}, route).(*simplevent.MarkUnread)
	if !ok || !unread.Unread || unread.Type != bridgev2.RemoteEventMarkUnread || unread.PortalKey != route.PortalKey {
		t.Errorf("an ack that goes back must mark the room unread: %+v", unread)
	}

	receipt, ok := d.ackEvent(prev, &discordgo.MessageAck{ChannelID: "1", MessageID: "400"}, route).(*simplevent.Receipt)
	if !ok || receipt.Type != bridgev2.RemoteEventReadReceipt || receipt.LastTarget != discordid.MakeMessageID("400") {
		t.Errorf("an ack that goes forward is a read receipt: %+v", receipt)
	}

	// A thread shares its parent's room, and going back in it must not mark the whole room unread.
	if _, ok = d.ackEvent(prev, &discordgo.MessageAck{ChannelID: "7", MessageID: "200"}, route).(*simplevent.Receipt); !ok {
		t.Error("an ack that goes back in a thread must stay a read receipt")
	}
}

func TestNoteOwnAck(t *testing.T) {
	d := &DiscordClient{readStates: map[string]*discordgo.ReadState{"1": {ID: "1", LastMessageID: "300"}}}

	// The bridge acks an older message; Discord's echo of that ack must not look like a step back.
	restore := d.noteOwnAck("1", "200")
	if ackMovesBack(d.readStateForID("1"), &discordgo.MessageAck{ChannelID: "1", MessageID: "200"}) {
		t.Error("the echo of the bridge's own ack was taken for a mark-unread")
	}
	restore()
	if got := d.readStateForID("1"); got == nil || got.LastMessageID != "300" {
		t.Errorf("a failed ack must leave the read position where it was: %+v", got)
	}

	restore = d.noteOwnAck("2", "200")
	restore()
	if got := d.readStateForID("2"); got != nil {
		t.Errorf("a failed ack must not leave a read position behind: %+v", got)
	}
}
