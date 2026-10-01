package msgconv

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

type fakeGhostMatrix struct {
	bridgev2.MatrixConnector
}

func (fakeGhostMatrix) ParseGhostMXID(userID id.UserID) (networkid.UserID, bool) {
	localpart, found := strings.CutPrefix(userID.Localpart(), "discord_")
	return networkid.UserID(localpart), found
}

func testReply(mentions *event.Mentions, senderMXID id.UserID) *bridgev2.MatrixMessage {
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: "hello", Mentions: mentions}
	return &bridgev2.MatrixMessage{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Event:   &event.Event{},
			Content: content,
			Portal:  &bridgev2.Portal{},
		},
		ReplyTo: &database.Message{
			ID:         discordid.MakeMessageID("700"),
			Room:       networkid.PortalKey{ID: discordid.MakeChannelPortalIDWithID("100")},
			SenderID:   discordid.MakeUserID("42"),
			SenderMXID: senderMXID,
		},
	}
}

func TestReplyPing(t *testing.T) {
	mc := &MessageConverter{Bridge: &bridgev2.Bridge{Matrix: fakeGhostMatrix{}}}
	const ghost = id.UserID("@discord_42:example.test")
	const puppet = id.UserID("@real:example.test")
	const other = id.UserID("@discord_43:example.test")

	tests := []struct {
		name       string
		mentions   *event.Mentions
		senderMXID id.UserID
		silent     bool
	}{
		// A client from before intentional mentions says nothing about whom to ping, and then a
		// reply pings as it does on Discord.
		{"no m.mentions", nil, ghost, false},
		{"mentions the author", &event.Mentions{UserIDs: []id.UserID{ghost}}, ghost, false},
		{"mentions the author among others", &event.Mentions{UserIDs: []id.UserID{other, ghost}}, ghost, false},
		// The message was bridged through the author's own Matrix account, so that is who the
		// client mentions.
		{"mentions the author's Matrix account", &event.Mentions{UserIDs: []id.UserID{puppet}}, puppet, false},
		// The author's ghost is the author too, whichever account the message was sent with.
		{"mentions the author's ghost", &event.Mentions{UserIDs: []id.UserID{ghost}}, puppet, false},
		{"mentions nobody", &event.Mentions{}, ghost, true},
		{"mentions someone else", &event.Mentions{UserIDs: []id.UserID{other}}, ghost, true},
		{"mentions only the room", &event.Mentions{Room: true}, ghost, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, err := mc.ToDiscord(context.Background(), nil, testReply(test.mentions, test.senderMXID), "100", nil)
			if err != nil {
				t.Fatal(err)
			}
			if req.Reference == nil || req.Reference.MessageID != "700" {
				t.Fatalf("not sent as a reply: %+v", req.Reference)
			}
			if !test.silent {
				// Discord's own client sends no allowed_mentions for an ordinary reply.
				if req.AllowedMentions != nil {
					t.Errorf("an ordinary reply must not restrict mentions: %+v", req.AllowedMentions)
				}
				return
			}
			if req.AllowedMentions == nil {
				t.Fatal("the reply pings its author, who wasn't mentioned")
			}
			if req.AllowedMentions.RepliedUser {
				t.Error("replied_user is still set")
			}
			// Turning off the reply's ping must leave every other mention working.
			for _, typ := range []discordgo.AllowedMentionType{
				discordgo.AllowedMentionTypeUsers, discordgo.AllowedMentionTypeRoles, discordgo.AllowedMentionTypeEveryone,
			} {
				if !slices.Contains(req.AllowedMentions.Parse, typ) {
					t.Errorf("%s mentions are no longer parsed", typ)
				}
			}
		})
	}

	t.Run("not a reply", func(t *testing.T) {
		msg := testReply(&event.Mentions{}, ghost)
		msg.ReplyTo = nil
		req, err := mc.ToDiscord(context.Background(), nil, msg, "100", nil)
		if err != nil {
			t.Fatal(err)
		}
		if req.AllowedMentions != nil {
			t.Errorf("a message that isn't a reply must not restrict mentions: %+v", req.AllowedMentions)
		}
	})
}
