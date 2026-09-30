package connector

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

var (
	_ bridgev2.RoomNameHandlingNetworkAPI   = (*DiscordClient)(nil)
	_ bridgev2.RoomTopicHandlingNetworkAPI  = (*DiscordClient)(nil)
	_ bridgev2.RoomAvatarHandlingNetworkAPI = (*DiscordClient)(nil)
	_ bridgev2.MembershipHandlingNetworkAPI = (*DiscordClient)(nil)
	_ bridgev2.DeleteChatHandlingNetworkAPI = (*DiscordClient)(nil)
)

// What a portal is on Discord decides which of its settings can be changed from Matrix.
type channelKind int

const (
	kindOther channelKind = iota // a server's space, or unknown
	kindDM
	kindGroupDM
	kindServerChannel
)

func portalChannelKind(meta *discordid.PortalMetadata) channelKind {
	if meta.ChannelType == nil {
		return kindOther
	}
	switch *meta.ChannelType {
	case discordgo.ChannelTypeDM:
		return kindDM
	case discordgo.ChannelTypeGroupDM:
		return kindGroupDM
	}
	if meta.GuildID != "" {
		return kindServerChannel
	}
	return kindOther
}

var errNotOnThisChannel = errors.New("this kind of channel doesn't have that on Discord")

// editChannel changes channel fields. A map, not discordgo.ChannelEdit, because clearing a topic or an icon
// has to send an empty value, which ChannelEdit's omitempty fields leave out.
func (d *DiscordClient) editChannel(portal *bridgev2.Portal, fields map[string]any) (*discordgo.Channel, error) {
	meta := portal.Metadata.(*discordid.PortalMetadata)
	channelID := discordid.ParseChannelPortalID(portal.ID)
	endpoint := discordgo.EndpointChannel(channelID)
	body, err := d.Session.RequestWithBucketID("PATCH", endpoint, fields, endpoint, makeDiscordReferer(meta.GuildID, channelID, ""))
	if err != nil {
		return nil, err
	}
	var ch discordgo.Channel
	if err = json.Unmarshal(body, &ch); err != nil {
		return nil, fmt.Errorf("failed to parse edited channel: %w", err)
	}
	return &ch, nil
}

func (d *DiscordClient) HandleMatrixRoomName(ctx context.Context, msg *bridgev2.MatrixRoomName) (bool, error) {
	if !d.IsLoggedIn() {
		return false, bridgev2.ErrNotLoggedIn
	}
	switch portalChannelKind(msg.Portal.Metadata.(*discordid.PortalMetadata)) {
	case kindGroupDM, kindServerChannel:
	default:
		return false, fmt.Errorf("can't rename: %w", errNotOnThisChannel)
	}
	if _, err := d.editChannel(msg.Portal, map[string]any{"name": msg.Content.Name}); err != nil {
		return false, d.tryWrappingError(ctx, err)
	}
	msg.Portal.Name = msg.Content.Name
	msg.Portal.NameSet = true
	return true, nil
}

func (d *DiscordClient) HandleMatrixRoomTopic(ctx context.Context, msg *bridgev2.MatrixRoomTopic) (bool, error) {
	if !d.IsLoggedIn() {
		return false, bridgev2.ErrNotLoggedIn
	}
	if portalChannelKind(msg.Portal.Metadata.(*discordid.PortalMetadata)) != kindServerChannel {
		return false, fmt.Errorf("can't set a topic: %w", errNotOnThisChannel)
	}
	if _, err := d.editChannel(msg.Portal, map[string]any{"topic": msg.Content.Topic}); err != nil {
		return false, d.tryWrappingError(ctx, err)
	}
	msg.Portal.Topic = msg.Content.Topic
	msg.Portal.TopicSet = true
	return true, nil
}

// iconDataURI is how Discord takes an image in a JSON body; nil removes the icon.
func iconDataURI(data []byte) any {
	if data == nil {
		return nil
	}
	return fmt.Sprintf("data:%s;base64,%s", http.DetectContentType(data), base64.StdEncoding.EncodeToString(data))
}

func (d *DiscordClient) HandleMatrixRoomAvatar(ctx context.Context, msg *bridgev2.MatrixRoomAvatar) (bool, error) {
	if !d.IsLoggedIn() {
		return false, bridgev2.ErrNotLoggedIn
	}
	// Only group DMs have an icon of their own; a server channel has none.
	if portalChannelKind(msg.Portal.Metadata.(*discordid.PortalMetadata)) != kindGroupDM {
		return false, fmt.Errorf("can't set a picture: %w", errNotOnThisChannel)
	}
	var data []byte
	if msg.Content.URL != "" {
		var err error
		data, err = msg.Portal.Bridge.Bot.DownloadMedia(ctx, msg.Content.URL, nil)
		if err != nil {
			return false, fmt.Errorf("failed to download avatar: %w", err)
		}
	}
	ch, err := d.editChannel(msg.Portal, map[string]any{"icon": iconDataURI(data)})
	if err != nil {
		return false, d.tryWrappingError(ctx, err)
	}
	// The same avatar ID the chat info gives it, so the next sync doesn't set it again.
	msg.Portal.AvatarID = discordid.MakeAvatarID(ch.Icon)
	msg.Portal.AvatarMXC = msg.Content.URL
	msg.Portal.AvatarHash = [32]byte{}
	if data != nil {
		msg.Portal.AvatarHash = sha256.Sum256(data)
	}
	msg.Portal.AvatarSet = true
	return true, nil
}

// recipientRequest is the request that adds (Invite) or removes (Kick) someone from a group DM, or false
// when the change isn't one Discord has.
func recipientRequest(changeType bridgev2.MembershipChangeType, channelID, userID string) (method, endpoint string, ok bool) {
	endpoint = discordgo.EndpointChannel(channelID) + "/recipients/" + userID
	switch changeType {
	case bridgev2.Invite:
		return "PUT", endpoint, true
	case bridgev2.Kick:
		return "DELETE", endpoint, true
	}
	return "", "", false
}

func (d *DiscordClient) HandleMatrixMembership(ctx context.Context, msg *bridgev2.MatrixMembershipChange) (*bridgev2.MatrixMembershipResult, error) {
	if msg.Type.IsSelf {
		// Leaving the Matrix room doesn't leave the Discord chat; deleting the chat does.
		return nil, nil
	}
	if portalChannelKind(msg.Portal.Metadata.(*discordid.PortalMetadata)) != kindGroupDM {
		if msg.Type == bridgev2.Invite {
			return nil, fmt.Errorf("can't add people: %w", errNotOnThisChannel)
		}
		return nil, nil
	}
	if !d.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}
	var userID string
	switch target := msg.Target.(type) {
	case *bridgev2.Ghost:
		userID = discordid.ParseUserID(target.ID)
	case *bridgev2.UserLogin:
		userID = discordid.ParseUserLoginID(target.ID)
	default:
		return nil, fmt.Errorf("unknown membership target %T", msg.Target)
	}
	channelID := discordid.ParseChannelPortalID(msg.Portal.ID)
	method, endpoint, ok := recipientRequest(msg.Type, channelID, userID)
	if !ok {
		return nil, nil
	}
	_, err := d.Session.RequestWithBucketID(method, endpoint, nil, discordgo.EndpointChannel(channelID)+"/recipients/",
		makeDiscordReferer("", channelID, ""))
	if err != nil {
		return nil, d.tryWrappingError(ctx, err)
	}
	return nil, nil
}

func (d *DiscordClient) HandleMatrixDeleteChat(ctx context.Context, msg *bridgev2.MatrixDeleteChat) error {
	switch portalChannelKind(msg.Portal.Metadata.(*discordid.PortalMetadata)) {
	case kindDM, kindGroupDM:
	default:
		// A server channel is the server's, not the user's chat: never delete it from Matrix.
		return fmt.Errorf("can't delete a server channel from Matrix")
	}
	if !d.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	// For a DM this closes it, for a group DM it leaves the group, as in Discord's own client.
	channelID := discordid.ParseChannelPortalID(msg.Portal.ID)
	_, err := d.Session.ChannelDelete(channelID, makeDiscordReferer("", channelID, ""))
	if err != nil {
		return d.tryWrappingError(ctx, err)
	}
	return nil
}
