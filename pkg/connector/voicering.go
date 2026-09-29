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
	"encoding/json"
	"fmt"

	"github.com/bwmarrin/discordgo"
)

/*
 * Ringing in a DM or group DM.
 *
 * A guild voice channel is membership - you are in it or you are not - so it maps onto a call that
 * is simply there. A DM call is the other shape: somebody rings, the others' clients ring back, and
 * it ends when nobody is left. Discord carries that over three gateway events and two REST calls,
 * none of which discordgo knows about.
 *
 * It does not have to know. Unknown gateway events still reach a handler registered for
 * *discordgo.Event, carrying their type and their raw body, and Session.Request will post anywhere -
 * so this needs no change to the library and no fork of it, which matters when the fork chain under
 * this bridge is already three deep.
 */

// Gateway event types Discord uses for DM calls. Not in discordgo's event table, so they arrive as
// raw events and are matched by name.
const (
	callCreateEvent = "CALL_CREATE"
	callUpdateEvent = "CALL_UPDATE"
	callDeleteEvent = "CALL_DELETE"
)

// discordCall is the state Discord reports for a DM call.
type discordCall struct {
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
	Region    string `json:"region"`
	// Ringing holds the user IDs whose clients are still ringing: whoever has neither answered nor
	// declined. It empties as people pick up, and an answered call reports nobody ringing at all -
	// so "is anybody being rung" is len(Ringing) > 0, not "is there a call".
	Ringing []string `json:"ringing"`
	// Ended is set on CALL_DELETE, which carries only the channel.
	Ended bool `json:"-"`
}

// IsRinging reports whether `userID`'s client is still being rung by this call.
func (c *discordCall) IsRinging(userID string) bool {
	for _, id := range c.Ringing {
		if id == userID {
			return true
		}
	}
	return false
}

/*
 * parseCallEvent reads one of the three DM-call events, or nothing for anything else.
 *
 * Kept apart from the handler and from the session so the shapes can be checked without a gateway:
 * these payloads are the part that a Discord change would break silently, since a field that stops
 * arriving simply reads as empty.
 */
func parseCallEvent(eventType string, raw json.RawMessage) (*discordCall, error) {
	switch eventType {
	case callCreateEvent, callUpdateEvent:
	case callDeleteEvent:
		// A delete carries the channel and nothing else worth having.
		var gone discordCall
		if err := json.Unmarshal(raw, &gone); err != nil {
			return nil, fmt.Errorf("parse %s: %w", eventType, err)
		}
		gone.Ended = true
		gone.Ringing = nil
		return &gone, nil
	default:
		return nil, nil
	}
	var call discordCall
	if err := json.Unmarshal(raw, &call); err != nil {
		return nil, fmt.Errorf("parse %s: %w", eventType, err)
	}
	if call.ChannelID == "" {
		return nil, fmt.Errorf("parse %s: no channel", eventType)
	}
	return &call, nil
}

// The two REST calls, built the way discordgo builds its own endpoints.
func endpointCallRing(channelID string) string {
	return discordgo.EndpointChannel(channelID) + "/call/ring"
}

func endpointCallStopRinging(channelID string) string {
	return discordgo.EndpointChannel(channelID) + "/call/stop-ringing"
}

// ringRequest is the body for both: the people to ring, or nil for everybody in the channel.
type ringRequest struct {
	Recipients []string `json:"recipients"`
}

/*
 * Ring the other people in a DM or group DM.
 *
 * `recipients` nil rings everybody in the channel, which is what a client does for a one-to-one
 * call. Naming them matters in a group DM, where ringing everybody when the reader asked for one
 * person is a roomful of unwanted calls.
 *
 * Only ever from something the reader did. The bridge is logged in as them, so an unasked ring is
 * indistinguishable from them placing a call.
 */
func ringDMCall(session *discordgo.Session, channelID string, recipients []string) error {
	if channelID == "" {
		return fmt.Errorf("ring: no channel")
	}
	_, err := session.Request("POST", endpointCallRing(channelID), ringRequest{Recipients: recipients})
	return err
}

// stopRingingDMCall takes the call off the others' screens, for a call the reader gave up on.
func stopRingingDMCall(session *discordgo.Session, channelID string, recipients []string) error {
	if channelID == "" {
		return fmt.Errorf("stop ringing: no channel")
	}
	_, err := session.Request("POST", endpointCallStopRinging(channelID), ringRequest{Recipients: recipients})
	return err
}
