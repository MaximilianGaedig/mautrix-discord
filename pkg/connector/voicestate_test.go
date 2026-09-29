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
	"slices"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func wantTrue(t *testing.T, got bool, what string) {
	t.Helper()
	if !got {
		t.Errorf("expected %s", what)
	}
}

func wantFalse(t *testing.T, got bool, what string) {
	t.Helper()
	if got {
		t.Errorf("expected not %s", what)
	}
}

func wantString(t *testing.T, got, want, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %q, want %q", what, got, want)
	}
}

func wantMembers(t *testing.T, got, want []string, what string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s: got %v, want %v", what, got, want)
	}
}

func state(userID, channelID string) *discordgo.VoiceState {
	return &discordgo.VoiceState{UserID: userID, ChannelID: channelID}
}

func TestVoiceChannelsJoinAndLeave(t *testing.T) {
	v := newVoiceChannels()

	joined := v.Apply(state("alice", "general"))
	wantTrue(t, joined.Joined(), "a join")
	wantFalse(t, joined.Left(), "a leave")
	wantMembers(t, v.Members("general"), []string{"alice"}, "members of general")
	wantTrue(t, v.Occupied("general"), "general occupied")

	// An empty channel is Discord saying they left voice altogether.
	left := v.Apply(state("alice", ""))
	wantTrue(t, left.Left(), "a leave")
	wantFalse(t, left.Joined(), "a join")
	wantString(t, left.PreviousChannelID, "general", "the channel before")
	wantMembers(t, v.Members("general"), nil, "members of general")
	wantFalse(t, v.Occupied("general"), "general occupied")
}

func TestVoiceChannelsMoveIsOneUpdate(t *testing.T) {
	v := newVoiceChannels()
	v.Apply(state("alice", "general"))

	// Discord reports a move as a single update naming the new channel.
	moved := v.Apply(state("alice", "music"))
	wantTrue(t, moved.Joined(), "a join")
	wantTrue(t, moved.Left(), "a leave")
	wantString(t, moved.PreviousChannelID, "general", "the channel before")
	wantString(t, moved.ChannelID, "music", "the channel now")

	// And it must not be left behind in the channel it came from.
	wantMembers(t, v.Members("general"), nil, "members of general")
	wantMembers(t, v.Members("music"), []string{"alice"}, "members of music")
	wantString(t, v.ChannelOf("alice"), "music", "where alice is")
}

func TestVoiceChannelsMuteIsNotAJoin(t *testing.T) {
	v := newVoiceChannels()
	v.Apply(state("alice", "general"))

	// Muting, deafening or starting a stream all arrive as an update for the same channel.
	muted := v.Apply(&discordgo.VoiceState{UserID: "alice", ChannelID: "general", SelfMute: true})
	wantFalse(t, muted.Joined(), "a join")
	wantFalse(t, muted.Left(), "a leave")
	wantMembers(t, v.Members("general"), []string{"alice"}, "members of general")
}

func TestVoiceChannelsHoldsEveryoneInAChannel(t *testing.T) {
	v := newVoiceChannels()
	v.Apply(state("bob", "general"))
	v.Apply(state("alice", "general"))
	v.Apply(state("carol", "music"))

	// Sorted, so what the bridge does with it does not depend on arrival order.
	wantMembers(t, v.Members("general"), []string{"alice", "bob"}, "members of general")
	wantMembers(t, v.Members("music"), []string{"carol"}, "members of music")
}

func TestVoiceChannelsIgnoresALeaveItAlreadyKnows(t *testing.T) {
	v := newVoiceChannels()
	v.Apply(state("alice", "general"))
	v.Apply(state("alice", ""))

	// A second leave, which the gateway can send after a reconnect, is not a second departure.
	again := v.Apply(state("alice", ""))
	wantFalse(t, again.Left(), "a leave")
	wantFalse(t, again.Joined(), "a join")
	wantString(t, v.ChannelOf("alice"), "", "where alice is")
}

func TestVoiceChannelsIgnoresNonsense(t *testing.T) {
	v := newVoiceChannels()
	wantFalse(t, v.Apply(nil).Joined(), "a join")
	wantFalse(t, v.Apply(&discordgo.VoiceState{ChannelID: "general"}).Joined(), "a join")
	wantMembers(t, v.Members("general"), nil, "members of general")
}

func TestVoiceChannelsResetForgetsTheGap(t *testing.T) {
	v := newVoiceChannels()
	v.Apply(state("alice", "general"))

	// After a reconnect the gateway replays what it knows; anything held from before may have
	// ended unobserved, so it is dropped rather than kept as a call nobody is in.
	v.Reset()
	wantMembers(t, v.Members("general"), nil, "members of general")
	wantString(t, v.ChannelOf("alice"), "", "where alice is")
	wantFalse(t, v.Occupied("general"), "general occupied")
}
