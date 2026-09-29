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
	"maps"
	"slices"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
)

/*
 * Who is sitting in which voice channel.
 *
 * A Discord voice channel is not a call that rings: people are simply in it or not, and the gateway
 * says so with VOICE_STATE_UPDATE. One update means all of join, leave and move - which one it is
 * can only be told by comparing the channel it names with the channel that user was in before - so
 * the comparison lives here, once, rather than at each place that wants to know.
 *
 * Kept apart from the bridge so it can be tested on its own: the ordering cases (a move arriving as
 * one update, a stale leave for a channel the user already left) are the kind of thing that is easy
 * to get subtly wrong and invisible when it is.
 */

// VoiceChange is what one VOICE_STATE_UPDATE turned out to mean.
type VoiceChange struct {
	UserID string
	// Where they are now, empty if they left voice entirely.
	ChannelID string
	// Where they were before, empty if they were not in a channel this knew about.
	PreviousChannelID string
}

// Joined reports whether the user is now in a channel they were not in before.
func (c VoiceChange) Joined() bool { return c.ChannelID != "" && c.ChannelID != c.PreviousChannelID }

// Left reports whether the user has left a channel this knew they were in.
func (c VoiceChange) Left() bool {
	return c.PreviousChannelID != "" && c.PreviousChannelID != c.ChannelID
}

// voiceChannels tracks the membership of every voice channel this login can see.
type voiceChannels struct {
	lock sync.RWMutex
	// channel ID -> user IDs in it.
	members map[string]map[string]struct{}
	// user ID -> the channel it is in, so a move can be told from a join.
	whereis map[string]string
}

func newVoiceChannels() *voiceChannels {
	return &voiceChannels{
		members: make(map[string]map[string]struct{}),
		whereis: make(map[string]string),
	}
}

// Apply records one update and says what it meant.
//
// Discord sends the update with the *new* channel, and an empty channel when the user has left
// voice altogether; the state cache's BeforeUpdate is not always populated, so where they were is
// taken from what was recorded here rather than from the event.
func (v *voiceChannels) Apply(update *discordgo.VoiceState) VoiceChange {
	if update == nil || update.UserID == "" {
		return VoiceChange{}
	}
	v.lock.Lock()
	defer v.lock.Unlock()

	previous := v.whereis[update.UserID]
	change := VoiceChange{UserID: update.UserID, ChannelID: update.ChannelID, PreviousChannelID: previous}
	if previous == update.ChannelID {
		// Muted, deafened, started a stream: still in the same place.
		return change
	}
	if previous != "" {
		delete(v.members[previous], update.UserID)
		if len(v.members[previous]) == 0 {
			delete(v.members, previous)
		}
	}
	if update.ChannelID == "" {
		delete(v.whereis, update.UserID)
		return change
	}
	if v.members[update.ChannelID] == nil {
		v.members[update.ChannelID] = make(map[string]struct{})
	}
	v.members[update.ChannelID][update.UserID] = struct{}{}
	v.whereis[update.UserID] = update.ChannelID
	return change
}

// Members returns the user IDs in a channel, sorted so the answer is stable.
func (v *voiceChannels) Members(channelID string) []string {
	v.lock.RLock()
	defer v.lock.RUnlock()
	return slices.Sorted(maps.Keys(v.members[channelID]))
}

// ChannelOf returns the voice channel a user is in, or an empty string.
func (v *voiceChannels) ChannelOf(userID string) string {
	v.lock.RLock()
	defer v.lock.RUnlock()
	return v.whereis[userID]
}

// Occupied reports whether anyone at all is in the channel, which is what decides whether there is
// a call to join.
func (v *voiceChannels) Occupied(channelID string) bool {
	v.lock.RLock()
	defer v.lock.RUnlock()
	return len(v.members[channelID]) > 0
}

// Reset forgets everything, for a reconnect: the gateway replays the voice states it knows about,
// and anything held from before the gap may have ended while we were not listening.
func (v *voiceChannels) Reset() {
	v.lock.Lock()
	defer v.lock.Unlock()
	clear(v.members)
	clear(v.whereis)
}

/*
 * A voice state update from the gateway.
 *
 * Nothing is bridged from it yet: this keeps the membership so that joining a channel's call knows
 * who is expected to be in it, and says what happened in the log. The media leg and the MatrixRTC
 * membership that go with it are the next stage (MEO-54).
 */
func (d *DiscordClient) handleVoiceStateUpdate(ctx context.Context, evt *discordgo.VoiceStateUpdate) {
	if evt == nil || evt.VoiceState == nil {
		return
	}
	change := d.voice.Apply(evt.VoiceState)
	if !change.Joined() && !change.Left() {
		// Muted, deafened, started a stream: nothing about who is where has changed.
		return
	}
	zerolog.Ctx(ctx).Debug().
		Str("voice_user_id", change.UserID).
		Str("voice_channel_id", change.ChannelID).
		Str("voice_previous_channel_id", change.PreviousChannelID).
		Bool("voice_joined", change.Joined()).
		Bool("voice_left", change.Left()).
		Int("voice_channel_size", len(d.voice.Members(change.ChannelID))).
		Msg("Voice channel membership changed")
}
