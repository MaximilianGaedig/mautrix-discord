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
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/callbridge"
	"maunium.net/go/mautrix/id"
)

// portalFor builds the minimum portal these tests need: they only ever read MXID.
func portalFor(roomID string) *bridgev2.Portal {
	// Portal embeds a *database.Portal, so the embedded struct has to exist before MXID can be set.
	return &bridgev2.Portal{Portal: &database.Portal{MXID: id.RoomID(roomID)}}
}

// joinMember records a Matrix user in a portal's call without needing a Discord session, which
// userJoined would.
func (v *voiceCalls) joinMember(roomID, userMXID string) {
	v.lock.Lock()
	defer v.lock.Unlock()
	if v.members[roomID] == nil {
		v.members[roomID] = make(map[string]struct{})
	}
	v.members[roomID][userMXID] = struct{}{}
}

func TestTheLastPersonOutClosesTheChannel(t *testing.T) {
	/*
	 * The decision this bookkeeping exists for.
	 *
	 * A Discord voice channel has no ringing, so nothing says when a call is over. Leaving on the
	 * first departure would drop the bridge out from under everyone still talking; never leaving
	 * would hold a voice connection open for nobody and show the account sitting in the channel
	 * indefinitely. So it has to be the last one out.
	 */
	v := newVoiceCalls()
	portal := portalFor("!room:example.com")
	v.joinMember("!room:example.com", "@alice:example.com")
	v.joinMember("!room:example.com", "@bob:example.com")

	v.userLeft(portal, "@alice:example.com")
	v.lock.Lock()
	remaining := len(v.members["!room:example.com"])
	v.lock.Unlock()
	if remaining != 1 {
		t.Errorf("after one of two left, %d members remain, want 1", remaining)
	}

	v.userLeft(portal, "@bob:example.com")
	v.lock.Lock()
	_, stillTracked := v.members["!room:example.com"]
	v.lock.Unlock()
	if stillTracked {
		t.Error("the room is still tracked after everyone left")
	}
}

func TestLeavingTwiceIsNotTwoDepartures(t *testing.T) {
	// State events can be re-sent, and a resend for somebody who already left must not look like
	// the last person leaving while others are still in the call.
	v := newVoiceCalls()
	portal := portalFor("!room:example.com")
	v.joinMember("!room:example.com", "@alice:example.com")
	v.joinMember("!room:example.com", "@bob:example.com")

	v.userLeft(portal, "@alice:example.com")
	v.userLeft(portal, "@alice:example.com")

	v.lock.Lock()
	remaining := len(v.members["!room:example.com"])
	v.lock.Unlock()
	if remaining != 1 {
		t.Errorf("%d members remain after a duplicate leave, want 1 (bob is still there)", remaining)
	}
}

func TestRoomsDoNotShareCallMembership(t *testing.T) {
	// Two voice channels bridged at once must not end each other's calls.
	v := newVoiceCalls()
	first, second := portalFor("!one:example.com"), portalFor("!two:example.com")
	v.joinMember("!one:example.com", "@alice:example.com")
	v.joinMember("!two:example.com", "@alice:example.com")

	v.userLeft(first, "@alice:example.com")
	v.lock.Lock()
	_, otherStillTracked := v.members["!two:example.com"]
	v.lock.Unlock()
	if !otherStillTracked {
		t.Error("leaving one room's call ended the other room's call")
	}
	_ = second
}

func TestAClearedMembershipIsADeparture(t *testing.T) {
	// How the trigger tells joining from leaving: a participant leaves by clearing the content, so
	// reading that as anything else would leave the bridge in a channel nobody is listening from.
	if callbridge.ParseRTCMembership(map[string]any{}).Joined {
		t.Error("cleared content was read as a join")
	}
	joined := callbridge.ParseRTCMembership(map[string]any{
		"application": "m.call", "device_id": "PHONE", "m.call.intent": "audio",
	})
	if !joined.Joined {
		t.Error("a real membership was not read as a join")
	}
}

func TestStopAllLeavesEverything(t *testing.T) {
	// A disconnect has to clear the bookkeeping too, or a reconnect would think people are still in
	// calls that ended while the gateway was down.
	v := newVoiceCalls()
	v.joinMember("!one:example.com", "@alice:example.com")
	v.joinMember("!two:example.com", "@bob:example.com")

	v.stopAll()
	v.lock.Lock()
	members, calls := len(v.members), len(v.calls)
	v.lock.Unlock()
	if members != 0 || calls != 0 {
		t.Errorf("after stopAll: %d rooms tracked, %d calls open, want 0 and 0", members, calls)
	}
}
