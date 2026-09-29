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
	"sync"

	"github.com/bwmarrin/discordgo"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/callbridge"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

/*
 * When to be in a Discord voice channel.
 *
 * A Discord voice channel has no ringing: people are in it or they are not. So there is no event
 * that means "a call started" and nothing to answer. What decides whether the bridge should be in
 * the channel is the Matrix side - somebody joined the room's call - because that is the only
 * moment where being there does anyone any good.
 *
 * The bridge therefore follows Matrix's call membership: the first real user to join the room's
 * call brings the bridge into the Discord channel, and the last one to leave takes it out again.
 * Staying joined after everyone has gone would hold a voice connection open for nobody and show
 * the account as sitting in the channel indefinitely.
 */

// voiceCalls tracks the bridged voice channel per portal, if any.
type voiceCalls struct {
	lock  sync.Mutex
	calls map[string]*voiceCall
	// Matrix users currently in each portal's call, so the last one leaving can be told from one of
	// several leaving.
	members map[string]map[string]struct{}
}

func newVoiceCalls() *voiceCalls {
	return &voiceCalls{
		calls:   make(map[string]*voiceCall),
		members: make(map[string]map[string]struct{}),
	}
}

// registerVoiceHandlers hooks call membership into the Matrix event processor, which bridgev2 does
// not deliver on its own.
func (d *DiscordConnector) registerVoiceHandlers() {
	if !d.Config.VoiceBridging {
		return
	}
	mx, ok := d.Bridge.Matrix.(*matrix.Connector)
	if !ok || mx.EventProcessor == nil {
		d.Bridge.Log.Warn().Msg("Matrix connector doesn't expose an event processor, voice bridging can't see call membership")
		return
	}
	mx.EventProcessor.On(callbridge.CallMemberEventType, d.handleCallMembership)
}

func (d *DiscordConnector) handleCallMembership(ctx context.Context, evt *event.Event) {
	if !d.Config.VoiceBridging || d.Bridge.IsGhostMXID(evt.Sender) || evt.Sender == d.Bridge.Bot.GetMXID() {
		// The bridge's own ghosts publish memberships too; reacting to those would have the bridge
		// join because it had joined.
		return
	}
	portal, err := d.Bridge.GetPortalByMXID(ctx, evt.RoomID)
	if err != nil || portal == nil || portal.MXID == "" {
		return
	}
	login, err := d.Bridge.GetExistingUserLoginByID(ctx, portal.Receiver)
	if err != nil || login == nil {
		user, userErr := d.Bridge.GetUserByMXID(ctx, evt.Sender)
		if userErr != nil || user == nil {
			return
		}
		login = user.GetDefaultLogin()
	}
	if login == nil {
		return
	}
	client, ok := login.Client.(*DiscordClient)
	if !ok || client.Session == nil || client.voiceCalls == nil {
		return
	}

	membership := callbridge.ParseRTCMembership(evt.Content.Raw)
	if membership.Joined {
		client.voiceCalls.userJoined(ctx, client, portal, string(evt.Sender))
	} else {
		client.voiceCalls.userLeft(portal, string(evt.Sender))
	}
}

// userJoined brings the bridge into the Discord channel for the first Matrix user in the call.
func (v *voiceCalls) userJoined(ctx context.Context, client *DiscordClient, portal *bridgev2.Portal, userMXID string) {
	key := string(portal.MXID)
	v.lock.Lock()
	if v.members[key] == nil {
		v.members[key] = make(map[string]struct{})
	}
	v.members[key][userMXID] = struct{}{}
	_, already := v.calls[key]
	v.lock.Unlock()
	if already {
		return
	}

	channelID := discordid.ParseChannelPortalID(portal.ID)
	if channelID == "" {
		return
	}
	channel, err := client.Session.State.Channel(channelID)
	if err != nil || channel == nil {
		client.UserLogin.Log.Debug().Str("channel_id", channelID).Msg("No Discord channel for a room with a call")
		return
	}
	// Only an actual voice channel can be joined; a text room's call is a Matrix-only call and
	// nothing to bridge.
	if channel.Type != discordgo.ChannelTypeGuildVoice && channel.Type != discordgo.ChannelTypeGuildStageVoice {
		return
	}

	call := newVoiceCall(client, portal, channel.GuildID, channelID)
	v.lock.Lock()
	if _, raced := v.calls[key]; raced {
		v.lock.Unlock()
		return
	}
	v.calls[key] = call
	v.lock.Unlock()

	go func() {
		if err := call.Start(); err != nil {
			client.UserLogin.Log.Warn().Err(err).Str("channel_id", channelID).Msg("Failed to join the Discord voice channel")
			v.lock.Lock()
			if v.calls[key] == call {
				delete(v.calls, key)
			}
			v.lock.Unlock()
			call.Close()
		}
	}()
}

// userLeft takes the bridge out of the channel once the last Matrix user has gone.
func (v *voiceCalls) userLeft(portal *bridgev2.Portal, userMXID string) {
	key := string(portal.MXID)
	v.lock.Lock()
	if members := v.members[key]; members != nil {
		delete(members, userMXID)
		if len(members) > 0 {
			// Somebody is still in the call.
			v.lock.Unlock()
			return
		}
		delete(v.members, key)
	}
	call := v.calls[key]
	delete(v.calls, key)
	v.lock.Unlock()
	if call != nil {
		call.Close()
	}
}

// stopAll leaves every bridged channel, for a disconnect or a logout.
func (v *voiceCalls) stopAll() {
	v.lock.Lock()
	calls := v.calls
	v.calls = make(map[string]*voiceCall)
	v.members = make(map[string]map[string]struct{})
	v.lock.Unlock()
	for _, call := range calls {
		call.Close()
	}
}
