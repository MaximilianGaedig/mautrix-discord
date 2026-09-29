// mautrix-discord - A Matrix-Discord puppeting bridge.
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
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/pion/rtp"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/callbridge"
	"maunium.net/go/mautrix/bridgev2/matrix"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

/*
 * A Discord voice channel, as a Matrix call.
 *
 * Nothing in here transcodes. Discord and Matrix both carry Opus at 48 kHz, so every packet that
 * crosses is the packet that arrived, re-headered. What the bridge has to get right instead is
 * *identity*: who each stream belongs to, on both sides.
 *
 * Going out, each Discord speaker is published to LiveKit as their own ghost - their own token,
 * their own membership, their own leg - rather than mixed into one "Discord" participant. Mixing
 * would mean decoding and re-encoding everything, and it would lose who is talking, which is most
 * of what a group call's interface shows. Per-speaker legs cost a LiveKit connection each and keep
 * both.
 *
 * Coming back, every Matrix participant is one stream into Discord's single outgoing socket, which
 * is the one place the asymmetry bites: Discord gives the bridge one identity to speak as, so
 * Matrix users arrive in the channel as the logged-in account.
 */

// rtcDeviceID is the virtual device the ghosts join calls from.
const rtcDeviceID = "DISCORDBRIDGE"

// voiceJoinTimeout bounds the Discord voice handshake, which can hang rather than fail when the
// voice gateway is unhappy.
const voiceJoinTimeout = 30 * time.Second

// voiceCall is one bridged voice channel.
type voiceCall struct {
	client *DiscordClient
	portal *bridgev2.Portal
	log    zerolog.Logger

	ctx    context.Context
	cancel context.CancelFunc

	guildID   string
	channelID string

	speakers *voiceSpeakers
	router   *voiceRouter

	lock sync.Mutex
	conn *discordgo.VoiceConnection
	// One published leg per Discord speaker, by Discord user ID.
	legs map[string]*speakerLeg
	// The leg that carries Matrix audio into Discord.
	inbound *callbridge.RTCLeg

	closeOnce sync.Once
}

// speakerLeg is one Discord speaker, published into the Matrix call as their ghost.
type speakerLeg struct {
	leg   *callbridge.RTCLeg
	ghost *mautrix.Client
}

func newVoiceCall(client *DiscordClient, portal *bridgev2.Portal, guildID, channelID string) *voiceCall {
	ctx, cancel := context.WithCancel(context.WithoutCancel(client.UserLogin.Log.WithContext(context.Background())))
	speakers := newVoiceSpeakers()
	return &voiceCall{
		client: client, portal: portal,
		log: client.UserLogin.Log.With().
			Str("component", "voice call").
			Str("channel_id", channelID).
			Str("portal_id", string(portal.ID)).
			Logger(),
		ctx: ctx, cancel: cancel,
		guildID: guildID, channelID: channelID,
		speakers: speakers,
		router:   newVoiceRouter(speakers),
		legs:     make(map[string]*speakerLeg),
	}
}

// Start joins the Discord voice channel and begins carrying audio both ways.
func (c *voiceCall) Start() error {
	conn, err := c.client.Session.ChannelVoiceJoin(c.guildID, c.channelID, false, false)
	if err != nil {
		return fmt.Errorf("join Discord voice: %w", err)
	}
	c.lock.Lock()
	c.conn = conn
	c.lock.Unlock()

	// The speaking updates are the only thing that says which SSRC is whose, so the handler is
	// attached before any audio is read rather than after.
	conn.AddHandler(func(_ *discordgo.VoiceConnection, update *discordgo.VoiceSpeakingUpdate) {
		if update == nil || update.SSRC < 0 {
			return
		}
		c.speakers.Learn(update.UserID, uint32(update.SSRC))
		if update.Speaking {
			// A speaker is published the first time they are heard rather than when they join the
			// channel: someone sitting muted in a voice channel has no audio to publish, and a
			// LiveKit participant that never sends anything just clutters the call.
			go c.ensureSpeaker(update.UserID)
		}
	})

	if err = c.waitReady(conn); err != nil {
		c.Close()
		return err
	}
	// Speaking must be declared before Discord will forward anything the bridge sends.
	if err = conn.Speaking(true); err != nil {
		c.log.Warn().Err(err).Msg("Failed to declare speaking on the Discord voice connection")
	}

	go c.readDiscord(conn)
	go c.writeDiscord(conn)
	c.log.Info().Msg("Joined Discord voice channel")
	return nil
}

// waitReady blocks until the voice handshake finishes, or gives up.
//
// Polled rather than waited on: discordgo signals readiness through a field and an internal
// channel it does not export, and a handshake that stalls leaves both silent, so the timeout is
// the only thing that ends it.
func (c *voiceCall) waitReady(conn *discordgo.VoiceConnection) error {
	deadline := time.NewTimer(voiceJoinTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		conn.RLock()
		ready := conn.Ready
		conn.RUnlock()
		if ready {
			return nil
		}
		select {
		case <-deadline.C:
			return errors.New("timed out waiting for the Discord voice connection")
		case <-c.ctx.Done():
			return c.ctx.Err()
		case <-tick.C:
		}
	}
}

// ensureSpeaker gives a Discord speaker their own published leg in the Matrix call.
func (c *voiceCall) ensureSpeaker(userID string) {
	if userID == "" || c.ctx.Err() != nil {
		return
	}
	// The logged-in account's own audio is not published: it is the Matrix users' own voices coming
	// back out of Discord, and publishing it would put everyone in an echo of themselves.
	if userID == c.client.Session.State.User.ID {
		return
	}
	c.lock.Lock()
	_, exists := c.legs[userID]
	c.lock.Unlock()
	if exists {
		return
	}

	ghost, err := c.client.connector.Bridge.GetGhostByID(c.ctx, discordid.MakeUserID(userID))
	if err != nil || ghost == nil {
		c.log.Warn().Err(err).Str("speaker_id", userID).Msg("Can't get ghost for a Discord speaker")
		return
	}
	intent, ok := ghost.Intent.(*matrix.ASIntent)
	if !ok {
		return
	}
	leg, err := c.joinAs(intent.Matrix.Client)
	if err != nil {
		c.log.Warn().Err(err).Str("speaker_id", userID).Msg("Failed to publish a Discord speaker into the call")
		return
	}

	c.lock.Lock()
	if _, raced := c.legs[userID]; raced || c.ctx.Err() != nil {
		// Two speaking updates can arrive together; the loser closes its leg rather than leaving a
		// second silent participant in the call.
		c.lock.Unlock()
		leg.Close()
		return
	}
	c.legs[userID] = &speakerLeg{leg: leg, ghost: intent.Matrix.Client}
	c.lock.Unlock()

	writer := leg.AudioWriter()
	c.router.Attach(userID, func(packet *rtp.Packet) {
		if err := writer.WriteRTP(packet); err != nil {
			c.log.Debug().Err(err).Str("speaker_id", userID).Msg("Failed to write Discord audio to the call")
		}
	})
	c.log.Info().Str("speaker_id", userID).Msg("Published a Discord speaker into the Matrix call")
}

// joinAs connects one Matrix identity to the room's LiveKit call and publishes its membership.
func (c *voiceCall) joinAs(cli *mautrix.Client) (*callbridge.RTCLeg, error) {
	bot, ok := c.client.connector.Bridge.Bot.(*matrix.ASIntent)
	if !ok {
		return nil, errors.New("bot intent isn't an appservice intent")
	}
	focus, err := c.client.connector.rtcFocus.Get(c.ctx, bot.Matrix.Client, c.client.connector.Bridge.Matrix.ServerName())
	if err != nil {
		return nil, err
	}
	url, token, err := callbridge.LiveKitToken(c.ctx, cli, focus, c.portal.MXID, rtcDeviceID)
	if err != nil {
		return nil, err
	}
	leg, err := callbridge.JoinRTC(c.ctx, callbridge.RTCLegConfig{URL: url, Token: token, Log: c.log})
	if err != nil {
		return nil, err
	}
	// The membership is what makes this identity visible to the other participants; without it the
	// audio arrives from somebody the clients do not know is in the call.
	if _, err = cli.SendStateEvent(c.ctx, c.portal.MXID, callbridge.CallMemberEventType,
		callbridge.RTCStateKey(cli.UserID, rtcDeviceID),
		callbridge.RTCMemberContent(focus, c.portal.MXID, rtcDeviceID, string(cli.UserID), false, time.Now().UnixMilli()),
	); err != nil {
		leg.Close()
		return nil, fmt.Errorf("publish call membership: %w", err)
	}
	return leg, nil
}

// readDiscord carries the channel's audio into the Matrix call.
func (c *voiceCall) readDiscord(conn *discordgo.VoiceConnection) {
	for {
		select {
		case <-c.ctx.Done():
			return
		case packet, ok := <-conn.OpusRecv:
			if !ok {
				c.log.Info().Msg("Discord voice receive channel closed")
				c.Close()
				return
			}
			if !c.router.Route(packet) && c.router.Unattributed()%500 == 1 {
				// Logged sparsely: an unknown SSRC at the very start of a call is ordinary, a
				// steady stream of them means speaking updates are not arriving at all.
				c.log.Debug().Uint32("ssrc", packet.SSRC).Msg("Dropped Discord audio from an unidentified speaker")
			}
		}
	}
}

// writeDiscord carries the Matrix call's audio into the channel.
func (c *voiceCall) writeDiscord(conn *discordgo.VoiceConnection) {
	leg, err := c.inboundLeg()
	if err != nil {
		c.log.Warn().Err(err).Msg("Failed to join the Matrix call to carry audio into Discord")
		return
	}
	track, err := leg.RemoteAudio(c.ctx)
	if err != nil {
		if c.ctx.Err() == nil {
			c.log.Warn().Err(err).Msg("No Matrix audio to carry into Discord")
		}
		return
	}
	for c.ctx.Err() == nil {
		packet, _, err := track.ReadRTP()
		if err != nil {
			if c.ctx.Err() == nil {
				c.log.Debug().Err(err).Msg("Matrix audio track ended")
			}
			return
		}
		if len(packet.Payload) == 0 {
			continue
		}
		// Opus straight through: Discord wants the frame, not the packet, and both sides are
		// already 48 kHz 20 ms.
		select {
		case conn.OpusSend <- packet.Payload:
		case <-c.ctx.Done():
			return
		default:
			// Discord's sender has a small buffer and its own clock. Dropping when it is full is
			// the same trade as everywhere else here: a late frame is worth less than a current
			// one, and blocking would stall the reader that feeds every other participant too.
		}
	}
}

// inboundLeg joins the call as the logged-in user's own ghost, to hear the Matrix side.
func (c *voiceCall) inboundLeg() (*callbridge.RTCLeg, error) {
	c.lock.Lock()
	existing := c.inbound
	c.lock.Unlock()
	if existing != nil {
		return existing, nil
	}
	ghost, err := c.client.connector.Bridge.GetGhostByID(c.ctx, discordid.MakeUserID(c.client.Session.State.User.ID))
	if err != nil || ghost == nil {
		return nil, fmt.Errorf("get own ghost: %w", err)
	}
	intent, ok := ghost.Intent.(*matrix.ASIntent)
	if !ok {
		return nil, errors.New("ghost intent isn't an appservice intent")
	}
	leg, err := c.joinAs(intent.Matrix.Client)
	if err != nil {
		return nil, err
	}
	c.lock.Lock()
	if c.inbound != nil || c.ctx.Err() != nil {
		c.lock.Unlock()
		leg.Close()
		return c.inbound, nil
	}
	c.inbound = leg
	c.lock.Unlock()
	return leg, nil
}

// Close leaves the channel and the call, clearing every membership it published.
//
// Clearing matters more than closing: a leg that is merely dropped leaves a membership behind, and
// the other participants go on seeing a ghost in the call that nothing is coming from.
func (c *voiceCall) Close() {
	c.closeOnce.Do(func() {
		c.lock.Lock()
		conn, inbound := c.conn, c.inbound
		legs := c.legs
		c.legs = make(map[string]*speakerLeg)
		c.inbound = nil
		c.lock.Unlock()

		// Memberships are cleared before the context is cancelled, since sending needs it.
		for userID, speaker := range legs {
			c.clearMembership(speaker.ghost)
			speaker.leg.Close()
			c.router.Detach(userID)
		}
		if inbound != nil {
			c.clearMembership(nil)
			inbound.Close()
		}
		c.cancel()

		if conn != nil {
			if err := conn.Disconnect(); err != nil {
				c.log.Debug().Err(err).Msg("Failed to leave the Discord voice channel")
			}
		}
		c.speakers.Reset()
		c.log.Info().Msg("Left Discord voice channel")
	})
}

func (c *voiceCall) clearMembership(cli *mautrix.Client) {
	if cli == nil {
		return
	}
	// A fresh context: this runs on the way down, and the session's own is about to be cancelled.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 10*time.Second)
	defer cancel()
	if _, err := cli.SendStateEvent(ctx, c.portal.MXID, callbridge.CallMemberEventType,
		callbridge.RTCStateKey(cli.UserID, rtcDeviceID), map[string]any{}); err != nil {
		c.log.Warn().Err(err).Msg("Failed to clear a call membership")
	}
}
