// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 Maximilian Gaedig
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/pkg/discordid"
	"go.mau.fi/mautrix-discord/pkg/msgconv"
	"go.mau.fi/mautrix-discord/pkg/router"
)

var (
	_ bridgev2.PollHandlingNetworkAPI    = (*DiscordClient)(nil)
	_ bridgev2.PollEndHandlingNetworkAPI = (*DiscordClient)(nil)
)

const (
	// Votes and poll ends are saved as messages under IDs that never parse as
	// Discord message IDs, so that a redaction of one can be told apart from
	// the deletion of a Discord message.
	pollVoteIDPrefix = "poll-vote:"
	pollEndIDPrefix  = "poll-end:"

	// Discord lists at most this many voters per request, and we read at most this many pages per answer.
	pollVotersPageSize = 100
	pollVotersMaxPages = 10
)

func makePollVoteID(pollID networkid.MessageID, userID string, at time.Time) networkid.MessageID {
	return networkid.MessageID(fmt.Sprintf("%s%s:%s:%d", pollVoteIDPrefix, pollID, userID, at.UnixNano()))
}

// parsePollVoteID gets the ID of the poll out of the ID of a vote.
func parsePollVoteID(messageID networkid.MessageID) (pollID networkid.MessageID, ok bool) {
	rest, ok := strings.CutPrefix(string(messageID), pollVoteIDPrefix)
	if !ok {
		return "", false
	}
	rawPollID, _, ok := strings.Cut(rest, ":")
	return networkid.MessageID(rawPollID), ok
}

func makePollEndID(pollID networkid.MessageID) networkid.MessageID {
	return networkid.MessageID(pollEndIDPrefix + string(pollID))
}

func pollError(err error) error {
	return bridgev2.WrapErrorInStatus(err).
		WithErrorReason(event.MessageStatusUnsupported).
		WithIsCertain(true).
		WithSendNotice(true).
		WithErrorAsMessage()
}

// pollMetadataOf is the poll bookkeeping of a message, or nil if it isn't a bridged poll.
func pollMetadataOf(msg *database.Message) *discordid.PollMetadata {
	if msg == nil {
		return nil
	}
	if meta, ok := msg.Metadata.(*discordid.MessageMetadata); ok && meta != nil {
		return meta.Poll
	}
	return nil
}

// findPollMessage finds the message part that carries the poll of a Discord message.
func (d *DiscordClient) findPollMessage(ctx context.Context, messageID networkid.MessageID) (*database.Message, *discordid.PollMetadata, error) {
	parts, err := d.connector.Bridge.DB.Message.GetAllPartsByID(ctx, d.UserLogin.ID, messageID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get poll message: %w", err)
	}
	for _, part := range parts {
		if pm := pollMetadataOf(part); pm != nil {
			return part, pm, nil
		}
	}
	return nil, nil, nil
}

func (d *DiscordClient) ownUserID() string {
	return discordid.ParseUserID(discordid.UserLoginIDToUserID(d.UserLogin.ID))
}

// channelForMessage works out the Discord channel a bridged message is in: its thread, if it's in one.
func (d *DiscordClient) channelForMessage(ctx context.Context, portal *bridgev2.Portal, target *database.Message) (channelID string, referer discordgo.RequestOption, err error) {
	guildID := portal.Metadata.(*discordid.PortalMetadata).GuildID
	parentChannelID := discordid.ParseChannelPortalID(portal.ID)
	channelID = parentChannelID
	threadChannelID := ""
	if target != nil && target.ThreadRoot != "" {
		thread, err := d.getThreadByRootMessageID(ctx, discordid.ParseMessageID(target.ThreadRoot))
		if err != nil {
			return "", nil, err
		} else if thread != nil {
			threadChannelID = thread.ThreadChannelID
			channelID = threadChannelID
		}
	}
	return channelID, makeDiscordReferer(guildID, parentChannelID, threadChannelID), nil
}

// ---------------------------------------------------------------------------
// Matrix to Discord
// ---------------------------------------------------------------------------

func (d *DiscordClient) HandleMatrixPollStart(ctx context.Context, msg *bridgev2.MatrixPollStart) (*bridgev2.MatrixMessageResponse, error) {
	resp, sent, err := d.sendMatrixMessage(ctx, &msg.MatrixMessage, func(ctx context.Context, channelID string, refererOpt discordgo.RequestOption) (*discordgo.MessageSend, error) {
		req, err := d.connector.MsgConv.PollToDiscord(msg)
		if err != nil {
			return nil, pollError(err)
		}
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	var sentPoll *discordgo.Poll
	if sent != nil {
		sentPoll = sent.Poll
	}
	answers := msgconv.PollAnswerMap(msg.Content, sentPoll)
	answerIDs := make([]int, 0, len(answers))
	for _, id := range answers {
		answerIDs = append(answerIDs, id)
	}
	slices.Sort(answerIDs)
	resp.DB.PartID = discordid.MakePartID(msgconv.PollPartID)
	resp.DB.Metadata = &discordid.MessageMetadata{Poll: &discordid.PollMetadata{
		MaxSelections: min(max(msg.Content.PollStart.MaxSelections, 1), len(answers)),
		AnswerIDs:     answerIDs,
		Answers:       answers,
		// The poll was created just now, so nobody has voted on it yet.
		Seeded: true,
	}}
	return resp, nil
}

// putPollVote sets the votes of the logged in user on a poll. An empty list retracts the vote.
func (d *DiscordClient) putPollVote(ctx context.Context, channelID, messageID string, answerIDs []int, referer discordgo.RequestOption) error {
	if answerIDs == nil {
		answerIDs = []int{}
	}
	endpoint := discordgo.EndpointPoll(channelID, messageID) + "/answers/@me"
	_, err := d.Session.RequestWithBucketID("PUT", endpoint, map[string]any{"answer_ids": answerIDs}, endpoint, referer, discordgo.WithContext(ctx))
	return d.tryWrappingError(ctx, err)
}

func (d *DiscordClient) HandleMatrixPollVote(ctx context.Context, msg *bridgev2.MatrixPollVote) (*bridgev2.MatrixMessageResponse, error) {
	if !d.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}
	pm := pollMetadataOf(msg.VoteTo)
	if pm == nil {
		return nil, pollError(errors.New("this message was bridged without poll data, so it can't be voted on"))
	} else if pm.Ended {
		return nil, pollError(errors.New("this poll has ended"))
	}
	answerIDs, err := pm.DiscordAnswerIDs(msg.Content.Response.Answers)
	if err != nil {
		return nil, pollError(err)
	}
	channelID, referer, err := d.channelForMessage(ctx, msg.Portal, msg.VoteTo)
	if err != nil {
		return nil, err
	}
	if err = d.putPollVote(ctx, channelID, discordid.ParseMessageID(msg.VoteTo.ID), answerIDs, referer); err != nil {
		return nil, err
	}
	// Remember the vote, so that Discord's events about it aren't bridged back as new responses.
	own := d.ownUserID()
	if pm.SetVotes(own, answerIDs) {
		if err = d.connector.Bridge.DB.Message.Update(ctx, msg.VoteTo); err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to save own poll vote")
		}
	}
	now := time.Now()
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        makePollVoteID(msg.VoteTo.ID, own, now),
			SenderID:  discordid.MakeUserID(own),
			Timestamp: now,
			Metadata:  &discordid.MessageMetadata{},
		},
	}, nil
}

func (d *DiscordClient) HandleMatrixPollEnd(ctx context.Context, msg *bridgev2.MatrixPollEnd) error {
	if !d.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	pm := pollMetadataOf(msg.Poll)
	if pm == nil {
		return fmt.Errorf("%w: the poll was bridged before polls were supported", bridgev2.ErrUnknownPoll)
	} else if pm.Ended {
		return nil
	}
	if discordid.ParseUserID(msg.Poll.SenderID) != d.ownUserID() {
		return pollError(errors.New("only the author of a poll can end it on Discord"))
	}
	channelID, referer, err := d.channelForMessage(ctx, msg.Portal, msg.Poll)
	if err != nil {
		return err
	}
	// Mark it ended first: Discord echoes the end back as a message update, which must not end the poll in Matrix a second time.
	pm.Ended = true
	if err = d.connector.Bridge.DB.Message.Update(ctx, msg.Poll); err != nil {
		return fmt.Errorf("failed to save poll state: %w", err)
	}
	endpoint := discordgo.EndpointPollExpire(channelID, discordid.ParseMessageID(msg.Poll.ID))
	_, err = d.Session.RequestWithBucketID("POST", endpoint, nil, endpoint, referer, discordgo.WithContext(ctx))
	if err != nil {
		pm.Ended = false
		if dbErr := d.connector.Bridge.DB.Message.Update(ctx, msg.Poll); dbErr != nil {
			zerolog.Ctx(ctx).Err(dbErr).Msg("Failed to reset poll state after failing to end it")
		}
		return d.tryWrappingError(ctx, err)
	}
	return nil
}

// handlePollRowRemoval handles the redaction of a vote or a poll end on
// Matrix, which have no Discord message to delete. handled is false for
// everything else.
func (d *DiscordClient) handlePollRowRemoval(ctx context.Context, removal *bridgev2.MatrixMessageRemove) (handled bool, err error) {
	if strings.HasPrefix(string(removal.TargetMessage.ID), pollEndIDPrefix) {
		return true, pollError(errors.New("a poll can't be reopened"))
	}
	pollID, ok := parsePollVoteID(removal.TargetMessage.ID)
	if !ok {
		return false, nil
	}
	if discordid.ParseUserID(removal.TargetMessage.SenderID) != d.ownUserID() {
		return true, pollError(errors.New("can't retract the vote of somebody else"))
	}
	pollMsg, pm, err := d.findPollMessage(ctx, pollID)
	if err != nil {
		return true, err
	} else if pm == nil {
		return true, pollError(errors.New("the poll of the vote isn't bridged"))
	} else if pm.Ended {
		return true, nil
	}
	channelID, referer, err := d.channelForMessage(ctx, removal.Portal, pollMsg)
	if err != nil {
		return true, err
	}
	if err = d.putPollVote(ctx, channelID, discordid.ParseMessageID(pollID), nil, referer); err != nil {
		return true, err
	}
	if pm.SetVotes(d.ownUserID(), nil) {
		if err = d.connector.Bridge.DB.Message.Update(ctx, pollMsg); err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to save retracted poll vote")
		}
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// Discord to Matrix
// ---------------------------------------------------------------------------

type pollVoteEvent struct {
	ChannelID string
	MessageID string
	UserID    string
	AnswerID  int
	Add       bool
}

// fetchPollVoters lists who picked which answer, as Discord's voter lists say.
func (d *DiscordClient) fetchPollVoters(ctx context.Context, channelID, messageID string, answerIDs []int) (map[string][]int, error) {
	votes := make(map[string][]int)
	for _, answerID := range answerIDs {
		after := ""
		for page := 0; page < pollVotersMaxPages; page++ {
			query := url.Values{"limit": {strconv.Itoa(pollVotersPageSize)}}
			if after != "" {
				query.Set("after", after)
			}
			endpoint := discordgo.EndpointPollAnswerVoters(channelID, messageID, answerID)
			body, err := d.Session.RequestWithBucketID("GET", endpoint+"?"+query.Encode(), nil, endpoint, discordgo.WithContext(ctx))
			if err != nil {
				return nil, err
			}
			var resp struct {
				Users []*discordgo.User `json:"users"`
			}
			if err = json.Unmarshal(body, &resp); err != nil {
				return nil, err
			}
			for _, user := range resp.Users {
				votes[user.ID] = append(votes[user.ID], answerID)
				after = user.ID
			}
			if len(resp.Users) < pollVotersPageSize {
				break
			}
		}
	}
	return votes, nil
}

// applyPollVote updates the bookkeeping of a poll for a vote event from
// Discord, and says which selection to send to Matrix. changed is false when
// there is nothing new to tell Matrix.
func (d *DiscordClient) applyPollVote(ctx context.Context, pm *discordid.PollMetadata, evt *pollVoteEvent) (selection []int, changed bool) {
	if pm.Seeded {
		return pm.ApplyVote(evt.UserID, evt.AnswerID, evt.Add)
	}
	// This is the first vote we hear about on this poll. Votes from before
	// the bridge saw the poll aren't in any event, so read the voter lists.
	before := pm.VotesOf(evt.UserID)
	votes, err := d.fetchPollVoters(ctx, evt.ChannelID, evt.MessageID, pm.AnswerIDs)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to read poll voters, going by the vote event alone")
		return pm.ApplyVote(evt.UserID, evt.AnswerID, evt.Add)
	}
	pm.Votes = nil
	for userID, answerIDs := range votes {
		pm.SetVotes(userID, answerIDs)
	}
	pm.Seeded = true
	// The lists may not have caught up with the event yet.
	pm.ApplyVote(evt.UserID, evt.AnswerID, evt.Add)
	selection = pm.VotesOf(evt.UserID)
	return selection, !slices.Equal(before, selection)
}

func (d *DiscordClient) handlePollVoteEvent(ctx context.Context, evt *pollVoteEvent) {
	if bridged, route := d.channelIsBridged(ctx, evt.ChannelID); bridged {
		d.queuePollVote(route, evt)
	}
}

// queuePollVote bridges a vote that was added or removed on Discord as a poll
// response, which carries the whole selection of the voter.
func (d *DiscordClient) queuePollVote(route *router.Route, evt *pollVoteEvent) {
	now := time.Now()
	d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &simplevent.Message[*pollVoteEvent]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessage,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.
					Str("action", "poll_vote").
					Str("poll_message_id", evt.MessageID).
					Str("voter_id", evt.UserID)
			},
			Sender:            d.makeEventSenderWithID(evt.UserID),
			PortalKey:         route.PortalKey,
			UncertainReceiver: route.Uncertain,
			Timestamp:         now,
		},
		ID:   makePollVoteID(discordid.MakeMessageID(evt.MessageID), evt.UserID, now),
		Data: evt,
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, evt *pollVoteEvent) (*bridgev2.ConvertedMessage, error) {
			pollMsg, pm, err := d.findPollMessage(ctx, discordid.MakeMessageID(evt.MessageID))
			if err != nil {
				return nil, err
			} else if pm == nil {
				return nil, fmt.Errorf("%w (poll message not bridged)", bridgev2.ErrIgnoringRemoteEvent)
			}
			selection, changed := d.applyPollVote(ctx, pm, evt)
			if !changed {
				return nil, fmt.Errorf("%w (vote already known)", bridgev2.ErrIgnoringRemoteEvent)
			}
			if err = d.connector.Bridge.DB.Message.Update(ctx, pollMsg); err != nil {
				zerolog.Ctx(ctx).Err(err).Msg("Failed to save poll votes")
			}
			content, extra := msgconv.PollResponseToMatrix(pollMsg.MXID, pm.MatrixAnswerIDs(selection))
			return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
				Type:       event.EventUnstablePollResponse,
				Content:    content,
				Extra:      extra,
				DBMetadata: &discordid.MessageMetadata{},
			}}}, nil
		},
	})
}

// queuePollEnd bridges the end of a poll as a poll end event from the poll's
// author. poll is the poll as Discord last sent it, or nil if not known. The
// same end is usually seen twice, as a message update and as a "poll result"
// message; the ID of the event and the ended flag make the second one a no-op.
func (d *DiscordClient) queuePollEnd(ctx context.Context, route *router.Route, pollMessageID string, poll *discordgo.Poll) {
	log := zerolog.Ctx(ctx)
	pollMsg, pm, err := d.findPollMessage(ctx, discordid.MakeMessageID(pollMessageID))
	if err != nil {
		log.Err(err).Str("poll_message_id", pollMessageID).Msg("Failed to look up ended poll")
		return
	} else if pm == nil {
		log.Debug().Str("poll_message_id", pollMessageID).Msg("Ignoring end of a poll that isn't bridged")
		return
	} else if pm.Ended {
		return
	}
	d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &simplevent.Message[*discordgo.Poll]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessage,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("action", "poll_end").Str("poll_message_id", pollMessageID)
			},
			Sender:            d.makeEventSenderWithID(discordid.ParseUserID(pollMsg.SenderID)),
			PortalKey:         route.PortalKey,
			UncertainReceiver: route.Uncertain,
			Timestamp:         time.Now(),
		},
		ID:   makePollEndID(discordid.MakeMessageID(pollMessageID)),
		Data: poll,
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, poll *discordgo.Poll) (*bridgev2.ConvertedMessage, error) {
			pollMsg, pm, err := d.findPollMessage(ctx, discordid.MakeMessageID(pollMessageID))
			if err != nil {
				return nil, err
			} else if pm == nil || pm.Ended {
				return nil, fmt.Errorf("%w (poll not bridged or already ended)", bridgev2.ErrIgnoringRemoteEvent)
			}
			pm.Ended = true
			if err = d.connector.Bridge.DB.Message.Update(ctx, pollMsg); err != nil {
				zerolog.Ctx(ctx).Err(err).Msg("Failed to save ended poll")
			}
			content, extra := msgconv.PollEndToMatrix(pollMsg.MXID, poll)
			return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
				Type:       event.EventUnstablePollEnd,
				Content:    content,
				Extra:      extra,
				DBMetadata: &discordid.MessageMetadata{},
			}}}, nil
		},
	})
}
