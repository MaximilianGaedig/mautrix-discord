package msgconv

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

type fakeMatrix struct {
	bridgev2.MatrixConnector
}

func (fakeMatrix) GenerateContentURI(_ context.Context, mediaID networkid.MediaID) (id.ContentURIString, error) {
	return id.ContentURIString("mxc://example.test/" + string(rune('a'+len(mediaID)%20))), nil
}

func testForward() *discordgo.Message {
	return &discordgo.Message{
		ID:        "900000000000000001",
		ChannelID: "900000000000000002",
		Author:    &discordgo.User{ID: "900000000000000003", Username: "fwd"},
		MessageReference: &discordgo.MessageReference{
			Type:      discordgo.MessageReferenceTypeForward,
			MessageID: "900000000000000004",
			ChannelID: "900000000000000005",
		},
		MessageSnapshots: []discordgo.MessageSnapshot{{Message: &discordgo.Message{
			Content:   "original text",
			Timestamp: time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC),
			Attachments: []*discordgo.MessageAttachment{{
				ID: "900000000000000010", Filename: "pic.png", URL: "https://cdn.example.test/pic.png",
				ContentType: "image/png", Size: 10, Width: 4, Height: 4,
			}},
			Embeds: []*discordgo.MessageEmbed{{
				Type: discordgo.EmbedTypeRich, Title: "Embed title", Description: "embed body text",
			}},
		}}},
	}
}

func TestForwardedSnapshotIsConverted(t *testing.T) {
	mc := &MessageConverter{
		Bridge:      &bridgev2.Bridge{Matrix: fakeMatrix{}},
		DirectMedia: true,
	}
	mc.CacheDirectMediaAttachment = func(*discordid.MediaInfo, string) {}
	source := &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "900000000000000099"}}
	msg := testForward()

	conv := mc.ToMatrix(context.Background(), &bridgev2.Portal{}, nil, source, nil, msg, nil)

	var text, media *bridgev2.ConvertedMessagePart
	for _, p := range conv.Parts {
		switch {
		case p.Content.MsgType == "m.image":
			media = p
		case strings.Contains(p.Content.Body, "Forwarded"):
			text = p
		}
	}
	if text == nil {
		t.Fatalf("no forwarded text part in %d parts", len(conv.Parts))
	}
	for _, want := range []string{"original text", "Embed title", "embed body text"} {
		if !strings.Contains(text.Content.FormattedBody, want) {
			t.Errorf("forwarded text lacks %q: %s", want, text.Content.FormattedBody)
		}
	}
	if media == nil {
		t.Fatal("forwarded attachment was dropped")
	}
	if media.ID != "fwd_900000000000000010" {
		t.Errorf("attachment part ID = %q", media.ID)
	}
}

func TestForwardedSnapshotUsesForwardIdentity(t *testing.T) {
	msg := testForward()
	snap := forwardedSnapshot(msg)
	if snap == nil || snap.ID != msg.ID || snap.ChannelID != msg.ChannelID || len(snap.Attachments) != 1 {
		t.Fatalf("unexpected snapshot %+v", snap)
	}
	msg.MessageReference.Type = discordgo.MessageReferenceTypeDefault
	if forwardedSnapshot(msg) != nil {
		t.Error("a non-forward must not yield a snapshot")
	}
}
