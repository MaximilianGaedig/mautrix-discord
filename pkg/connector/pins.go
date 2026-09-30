package connector

import (
	"context"
	"fmt"
	"slices"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-discord/pkg/discordid"
	"go.mau.fi/mautrix-discord/pkg/router"
)

var _ bridgev2.PinHandlingNetworkAPI = (*DiscordClient)(nil)

// Discord allows 50 pins per channel, and one page of the pins list holds them all.
const maxPins = 50

// pinnedMessageIDs turns Discord's pin list, newest pin first, into the room's pin list, oldest first.
func pinnedMessageIDs(list *discordgo.ChannelMessagePinsList) []networkid.MessageID {
	ids := make([]networkid.MessageID, 0, len(list.Items))
	for _, item := range list.Items {
		if item.Message != nil {
			ids = append(ids, discordid.MakeMessageID(item.Message.ID))
		}
	}
	slices.Reverse(ids)
	return ids
}

// handleDiscordPinsUpdate re-reads the channel's pins: the event only says that they changed.
func (d *DiscordClient) handleDiscordPinsUpdate(ctx context.Context, evt *discordgo.ChannelPinsUpdate, route *router.Route) {
	log := zerolog.Ctx(ctx)
	if evt.ChannelID != route.PortalChannelID {
		// A thread's pins would replace its parent room's; the room only has one pin list.
		log.Debug().Str("channel_id", evt.ChannelID).Msg("Ignoring pins update in a thread")
		return
	}
	list, err := d.Session.ChannelMessagesPinned(evt.ChannelID, nil, maxPins)
	if err != nil {
		log.Err(err).Str("channel_id", evt.ChannelID).Msg("Failed to fetch pinned messages")
		return
	}
	pinned := pinnedMessageIDs(list)
	d.UserLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: route.PortalKey,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("channel_id", evt.ChannelID).Int("pinned_count", len(pinned))
			},
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			ChatInfo: &bridgev2.ChatInfo{PinnedMessages: &pinned},
		},
	})
}

func (d *DiscordClient) HandleMatrixPin(ctx context.Context, msg *bridgev2.MatrixPin) error {
	if !d.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	meta := msg.Portal.Metadata.(*discordid.PortalMetadata)
	parentChannelID := discordid.ParseChannelPortalID(msg.Portal.ID)
	channelID := parentChannelID
	threadChannelID := ""
	if msg.TargetMessage.ThreadRoot != "" {
		thread, err := d.getThreadByRootMessageID(ctx, discordid.ParseMessageID(msg.TargetMessage.ThreadRoot))
		if err != nil {
			return err
		} else if thread != nil {
			threadChannelID = thread.ThreadChannelID
			channelID = threadChannelID
		}
	}
	messageID := discordid.ParseMessageID(msg.TargetMessage.ID)
	referer := makeDiscordReferer(meta.GuildID, parentChannelID, threadChannelID)
	var err error
	if msg.Pinned {
		err = d.Session.ChannelMessagePin(channelID, messageID, referer)
	} else {
		err = d.Session.ChannelMessageUnpin(channelID, messageID, referer)
	}
	if err != nil {
		return fmt.Errorf("failed to update pin: %w", d.tryWrappingError(ctx, err))
	}
	return nil
}
