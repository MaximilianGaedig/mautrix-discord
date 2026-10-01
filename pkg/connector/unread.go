package connector

import (
	"context"
	"fmt"
	"strconv"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-discord/pkg/discordid"
	"go.mau.fi/mautrix-discord/pkg/router"
)

var _ bridgev2.MarkedUnreadHandlingNetworkAPI = (*DiscordClient)(nil)

// Discord has no unread flag of its own. Its "Mark Unread" moves the read position back to the
// message before the chosen one, with an ack that says it was done by hand; without that the
// clients take the ack for an ordinary one and may move the position forward again.
type manualAck struct {
	Manual       bool `json:"manual"`
	MentionCount int  `json:"mention_count"`
}

// manualAckFor is the ack that marks a channel unread. The mention count is the red badge: every
// message in a DM or group DM counts as one, and in a server channel only real mentions do, which
// marking the room unread in Matrix says nothing about.
func manualAckFor(private bool) manualAck {
	if private {
		return manualAck{Manual: true, MentionCount: 1}
	}
	return manualAck{Manual: true}
}

// How many of the room's newest message parts are looked at to find the two newest messages of the
// channel itself: parts of one message and messages in threads sit between them.
const unreadLookback = 30

// unreadAckTarget picks the message to move the read position back to, so that the channel's newest
// message is unread, and that newest message. The parts are the room's newest, newest first.
// Messages in threads are left out, since a thread is a channel of its own with its own read
// position. When the newest message is the only one known, the position goes to the ID just below
// it: Discord compares IDs and doesn't need the message to exist.
func unreadAckTarget(newestFirst []*database.Message) (ackID, newestID string, ok bool) {
	for _, part := range newestFirst {
		if part.ThreadRoot != "" {
			continue
		}
		id := discordid.ParseMessageID(part.ID)
		if _, err := strconv.ParseUint(id, 10, 64); err != nil {
			// Not a Discord message (a notice the bridge made up, for example).
			continue
		}
		if newestID == "" {
			newestID = id
		} else if id != newestID {
			return id, newestID, true
		}
	}
	if newestID == "" {
		return "", "", false
	}
	newest, _ := strconv.ParseUint(newestID, 10, 64)
	if newest == 0 {
		return "", "", false
	}
	return strconv.FormatUint(newest-1, 10), newestID, true
}

// ackMovesBack says whether an ack puts the read position before where it was. Reading only moves
// it forward, so that is the chat being marked unread in another Discord client. discordgo doesn't
// parse the ack's "manual" field, which would say the same.
func ackMovesBack(prev *discordgo.ReadState, ack *discordgo.MessageAck) bool {
	if prev == nil {
		return false
	}
	before, err := strconv.ParseUint(string(prev.LastMessageID), 10, 64)
	if err != nil {
		return false
	}
	after, err := strconv.ParseUint(ack.MessageID, 10, 64)
	if err != nil {
		return false
	}
	return after < before
}

// ackEvent is what an ack from another Discord client means for the room: marked unread when the
// read position of the room's own channel went back, and read up to the acked message otherwise.
// A thread going back doesn't mark the room: the room would look unread with nothing new in it.
func (d *DiscordClient) ackEvent(prev *discordgo.ReadState, ack *discordgo.MessageAck, route *router.Route) bridgev2.RemoteEvent {
	meta := simplevent.EventMeta{
		PortalKey:         route.PortalKey,
		Sender:            d.selfEventSender(),
		UncertainReceiver: route.Uncertain,
	}
	if ack.ChannelID == route.PortalChannelID && ackMovesBack(prev, ack) {
		meta.Type = bridgev2.RemoteEventMarkUnread
		return &simplevent.MarkUnread{EventMeta: meta, Unread: true}
	}
	meta.Type = bridgev2.RemoteEventReadReceipt
	return &simplevent.Receipt{EventMeta: meta, LastTarget: discordid.MakeMessageID(ack.MessageID)}
}

// noteOwnAck moves the remembered read position to an ack the bridge is about to send, and returns
// how to put it back if sending fails. Discord tells every session about an ack, this one too, and
// with the position already moved that echo is no step back: without this, a Matrix read receipt
// for an older message, or the bridge's own mark-unread, would come back as the room being marked
// unread. It is noted before sending because the echo can arrive before the response does.
func (d *DiscordClient) noteOwnAck(channelID, messageID string) (restore func()) {
	d.readStatesLock.Lock()
	defer d.readStatesLock.Unlock()
	prev, hadPrev := d.readStates[channelID]
	d.readStates[channelID] = &discordgo.ReadState{ID: channelID, LastMessageID: discordgo.StringOrInt(messageID)}
	return func() {
		d.readStatesLock.Lock()
		defer d.readStatesLock.Unlock()
		if hadPrev {
			d.readStates[channelID] = prev
		} else {
			delete(d.readStates, channelID)
		}
	}
}

// HandleMarkedUnread marks the channel unread on Discord when the room is marked unread in Matrix,
// and read again when the mark is taken off.
func (d *DiscordClient) HandleMarkedUnread(ctx context.Context, msg *bridgev2.MatrixMarkedUnread) error {
	if !d.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	log := zerolog.Ctx(ctx)
	guildID := msg.Portal.Metadata.(*discordid.PortalMetadata).GuildID
	channelID := discordid.ParseChannelPortalID(msg.Portal.ID)
	parts, err := d.UserLogin.Bridge.DB.Message.GetLastNInPortal(ctx, msg.Portal.PortalKey, unreadLookback)
	if err != nil {
		return fmt.Errorf("failed to get the room's newest messages: %w", err)
	}
	ackID, newestID, ok := unreadAckTarget(parts)
	if !ok {
		log.Debug().Msg("Not changing the unread mark on Discord: the room has no Discord message to mark from")
		return nil
	}
	referer := makeDiscordReferer(guildID, channelID, "")
	if !msg.Content.Unread {
		restore := d.noteOwnAck(channelID, newestID)
		_, err = d.Session.ChannelMessageAckNoToken(channelID, newestID, referer)
		if err != nil {
			restore()
			return fmt.Errorf("failed to mark the channel read: %w", err)
		}
		return nil
	}
	restore := d.noteOwnAck(channelID, ackID)
	_, err = d.Session.RequestWithBucketID(
		"POST",
		discordgo.EndpointChannelMessageAck(channelID, ackID),
		manualAckFor(guildID == ""),
		discordgo.EndpointChannelMessageAck(channelID, ""),
		referer,
		discordgo.WithContext(ctx),
	)
	if err != nil {
		restore()
		return fmt.Errorf("failed to mark the channel unread: %w", err)
	}
	log.Debug().Str("ack_message_id", ackID).Msg("Marked the channel unread on Discord")
	return nil
}
