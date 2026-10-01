package connector

import (
	"context"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-discord/pkg/discordid"
	"go.mau.fi/mautrix-discord/pkg/router"
)

// bulkDeleteEvents turns a bulk delete (a moderator's purge, or a ban that takes the person's
// recent messages with it) into one removal per message. Discord sends a single event for the lot,
// and the bridge has no removal that covers more than one message.
func (d *DiscordClient) bulkDeleteEvents(evt *discordgo.MessageDeleteBulk, route *router.Route) []*DiscordMessage {
	evts := make([]*DiscordMessage, 0, len(evt.Messages))
	for _, messageID := range evt.Messages {
		wrapped := d.wrapDiscordMessage(context.Background(), &discordgo.Message{
			ID:        messageID,
			ChannelID: evt.ChannelID,
			GuildID:   evt.GuildID,
		}, route, bridgev2.RemoteEventMessageRemove)
		evts = append(evts, &wrapped)
	}
	return evts
}

// reactionClearEvent is every reaction on a message being removed at once. The event names nobody,
// so it is bridged as the message's complete set of reactions being empty: the bridge then redacts
// each reaction it knows of.
func reactionClearEvent(reaction *discordgo.MessageReaction, route *router.Route) *simplevent.ReactionSync {
	return &simplevent.ReactionSync{
		EventMeta: simplevent.EventMeta{
			Type:              bridgev2.RemoteEventReactionSync,
			PortalKey:         route.PortalKey,
			UncertainReceiver: route.Uncertain,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("message_id", reaction.MessageID).Str("action", "clear reactions")
			},
		},
		TargetMessage: discordid.MakeMessageID(reaction.MessageID),
		Reactions:     &bridgev2.ReactionSyncData{HasAllUsers: true},
	}
}

// isDiscordEmoji says whether a bridged reaction used the given emoji. A custom emoji is stored as
// "name:id" and matched on the id alone: the name is whatever the server called it when the
// reaction was bridged, and the server can rename it.
func isDiscordEmoji(id networkid.EmojiID, emoji *discordgo.Emoji) bool {
	stored := discordid.ParseEmojiID(id)
	if emoji.ID != "" {
		return strings.HasSuffix(stored, ":"+emoji.ID)
	}
	return stored == emoji.Name
}

// reactionsWithoutEmoji is what is left of a message's reactions once one emoji is removed from
// it, for the people who used that emoji. Everyone else is left out, and so left alone.
func reactionsWithoutEmoji(existing []*database.Reaction, emoji *discordgo.Emoji) *bridgev2.ReactionSyncData {
	data := &bridgev2.ReactionSyncData{Users: make(map[networkid.UserID]*bridgev2.ReactionSyncUser)}
	for _, reaction := range existing {
		if isDiscordEmoji(reaction.EmojiID, emoji) {
			data.Users[reaction.SenderID] = &bridgev2.ReactionSyncUser{HasAllReactions: true}
		}
	}
	for _, reaction := range existing {
		user, ok := data.Users[reaction.SenderID]
		if !ok || isDiscordEmoji(reaction.EmojiID, emoji) {
			continue
		}
		user.Reactions = append(user.Reactions, &bridgev2.BackfillReaction{
			Timestamp: reaction.Timestamp,
			Sender:    bridgev2.EventSender{Sender: reaction.SenderID},
			EmojiID:   reaction.EmojiID,
			Emoji:     reaction.Emoji,
		})
	}
	return data
}

// reactionEmojiRemoveEvent is one emoji being removed from a message, for everyone who reacted
// with it. Discord doesn't say who they were, so the reactions the bridge has for the message are
// looked up. That is done when the event is handled rather than here: by then every reaction event
// queued before this one has been applied, so none of them is missed or removed by mistake.
func reactionEmojiRemoveEvent(reaction *discordgo.MessageReaction, route *router.Route) *simplevent.ReactionSync {
	messageID := discordid.MakeMessageID(reaction.MessageID)
	evt := &simplevent.ReactionSync{
		EventMeta: simplevent.EventMeta{
			Type:              bridgev2.RemoteEventReactionSync,
			PortalKey:         route.PortalKey,
			UncertainReceiver: route.Uncertain,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("message_id", reaction.MessageID).
					Str("emoji_id", reaction.Emoji.ID).
					Str("emoji_name", reaction.Emoji.Name).
					Str("action", "remove emoji from reactions")
			},
		},
		TargetMessage: messageID,
		// Nobody listed and the list not complete changes nothing, which is what is wanted if the
		// lookup fails.
		Reactions: &bridgev2.ReactionSyncData{},
	}
	evt.PreHandleFunc = func(ctx context.Context, portal *bridgev2.Portal) {
		existing, err := portal.Bridge.DB.Reaction.GetAllToMessage(ctx, portal.Receiver, messageID)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to get the message's reactions to remove an emoji from them")
			return
		}
		evt.Reactions = reactionsWithoutEmoji(existing, &reaction.Emoji)
	}
	return evt
}
