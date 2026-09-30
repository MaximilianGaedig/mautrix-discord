package connector

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

func TestPortalChannelKind(t *testing.T) {
	typ := func(ct discordgo.ChannelType) *discordgo.ChannelType { return &ct }
	for _, tc := range []struct {
		meta discordid.PortalMetadata
		want channelKind
	}{
		{discordid.PortalMetadata{ChannelType: typ(discordgo.ChannelTypeDM)}, kindDM},
		{discordid.PortalMetadata{ChannelType: typ(discordgo.ChannelTypeGroupDM)}, kindGroupDM},
		{discordid.PortalMetadata{GuildID: "1", ChannelType: typ(discordgo.ChannelTypeGuildText)}, kindServerChannel},
		{discordid.PortalMetadata{GuildID: "1"}, kindOther}, // the server's space
	} {
		if got := portalChannelKind(&tc.meta); got != tc.want {
			t.Errorf("%+v: got %v, want %v", tc.meta, got, tc.want)
		}
	}
}

func TestRecipientRequest(t *testing.T) {
	method, endpoint, ok := recipientRequest(bridgev2.Invite, "10", "20")
	if !ok || method != "PUT" || !strings.HasSuffix(endpoint, "/channels/10/recipients/20") {
		t.Errorf("invite = %s %s %v", method, endpoint, ok)
	}
	if method, _, ok = recipientRequest(bridgev2.Kick, "10", "20"); !ok || method != "DELETE" {
		t.Errorf("kick = %s %v", method, ok)
	}
	if _, _, ok = recipientRequest(bridgev2.BanJoined, "10", "20"); ok {
		t.Error("group DMs have no bans")
	}
}

func TestIconDataURI(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n0000")
	if got := iconDataURI(png); got != "data:image/png;base64,iVBORw0KGgowMDAw" {
		t.Errorf("icon = %v", got)
	}
	if iconDataURI(nil) != nil {
		t.Error("no picture must send null, which removes the icon")
	}
}

func TestRoomManagementCaps(t *testing.T) {
	server := discordCaps.Clone()
	roomManagementCaps(server, kindServerChannel)
	if server.DeleteChat {
		t.Error("a server channel must never be deletable from Matrix")
	}
	if server.State[event.StateTopic.Type] == nil || server.State[event.StateRoomAvatar.Type] != nil {
		t.Errorf("server channel state = %v", server.State)
	}
	group := discordCaps.Clone()
	roomManagementCaps(group, kindGroupDM)
	if !group.DeleteChat || group.MemberActions[event.MemberActionInvite] != event.CapLevelFullySupported {
		t.Errorf("group DM caps = %+v", group)
	}
	if group.ID == server.ID {
		t.Error("different capabilities need different IDs")
	}
}
