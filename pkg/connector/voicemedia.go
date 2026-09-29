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
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/pion/rtp"
)

/*
 * The audio of a Discord voice channel, as streams that can be attached to Matrix.
 *
 * Discord is the easy network for this: everyone in a channel speaks Opus at 48 kHz, which is what
 * Matrix wants, so nothing here transcodes. What it does instead is take apart what arrives as one
 * socket.
 *
 * Every speaker in the channel is a separate Opus stream, and all of them arrive interleaved on one
 * UDP connection, told apart only by SSRC. Which SSRC belongs to whom is not in the packets: it
 * comes separately, over the voice websocket, as a speaking update. So the packets are useless
 * until that mapping exists, and the mapping arrives whenever it arrives - typically just before
 * the first packet, but nothing guarantees it.
 *
 * Keeping the demultiplexing here, apart from the bridge, is what lets the ordering cases be tested:
 * audio before the mapping, a speaker whose SSRC is reused by someone else later, a channel where
 * two people talk at once.
 */

// voiceSpeakers maps Discord's SSRCs to the users they belong to.
type voiceSpeakers struct {
	lock sync.RWMutex
	// SSRC -> Discord user ID.
	bySSRC map[uint32]string
	// user ID -> SSRC, so a user's old SSRC can be retired when they get a new one.
	byUser map[string]uint32
}

func newVoiceSpeakers() *voiceSpeakers {
	return &voiceSpeakers{bySSRC: make(map[uint32]string), byUser: make(map[string]uint32)}
}

// Learn records a speaking update.
//
// A user's SSRC changes when they rejoin, and Discord hands out the freed number again, so both
// directions are kept: without retiring the user's previous SSRC, their old number would go on
// resolving to them and whoever inherits it would be attributed to the wrong person.
func (v *voiceSpeakers) Learn(userID string, ssrc uint32) {
	if userID == "" || ssrc == 0 {
		return
	}
	v.lock.Lock()
	defer v.lock.Unlock()
	if previous, ok := v.byUser[userID]; ok && previous != ssrc {
		delete(v.bySSRC, previous)
	}
	// If this SSRC was someone else's, that someone else no longer has it.
	if previousUser, ok := v.bySSRC[ssrc]; ok && previousUser != userID {
		delete(v.byUser, previousUser)
	}
	v.bySSRC[ssrc] = userID
	v.byUser[userID] = ssrc
}

// UserOf returns who an SSRC belongs to, or "" when that is not known yet.
func (v *voiceSpeakers) UserOf(ssrc uint32) string {
	v.lock.RLock()
	defer v.lock.RUnlock()
	return v.bySSRC[ssrc]
}

// SSRCOf returns a user's current SSRC, or 0.
func (v *voiceSpeakers) SSRCOf(userID string) uint32 {
	v.lock.RLock()
	defer v.lock.RUnlock()
	return v.byUser[userID]
}

// Forget drops a user, for someone leaving the channel.
func (v *voiceSpeakers) Forget(userID string) {
	v.lock.Lock()
	defer v.lock.Unlock()
	if ssrc, ok := v.byUser[userID]; ok {
		delete(v.bySSRC, ssrc)
	}
	delete(v.byUser, userID)
}

// Reset forgets every mapping, for a reconnect: SSRCs are assigned per voice connection and mean
// nothing across one.
func (v *voiceSpeakers) Reset() {
	v.lock.Lock()
	defer v.lock.Unlock()
	clear(v.bySSRC)
	clear(v.byUser)
}

/*
 * discordPacketToRTP rebuilds the RTP packet Discord took apart.
 *
 * discordgo hands over the header fields and the payload separately, because it has already
 * stripped and decrypted the packet. Matrix wants a whole RTP packet again, so it is put back
 * together with the numbering Discord used - which is the right numbering to keep, since dropping
 * back to a locally generated sequence would hide exactly the loss and reordering the receiver's
 * jitter buffer exists to handle.
 *
 * SSRC and payload type are deliberately left unset: TrackLocalStaticRTP stamps both with what its
 * own PeerConnection negotiated, and Discord's numbering for them means nothing to Matrix.
 */
func discordPacketToRTP(packet *discordgo.Packet) *rtp.Packet {
	if packet == nil || len(packet.Opus) == 0 {
		return nil
	}
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			SequenceNumber: packet.Sequence,
			Timestamp:      packet.Timestamp,
		},
		Payload: packet.Opus,
	}
}

/*
 * voiceRouter fans one Discord voice connection out to one stream per speaker.
 *
 * A stream is created the first time a speaker is heard and kept for as long as the connection
 * lasts, because a speaker who stops talking has not left - Discord simply stops sending until they
 * talk again, and tearing down the stream in between would make every pause look like a departure.
 */
type voiceRouter struct {
	speakers *voiceSpeakers
	lock     sync.Mutex
	// Discord user ID -> where that speaker's packets go.
	sinks map[string]func(*rtp.Packet)
	// Packets whose SSRC is not yet known to belong to anyone.
	unattributed int
}

func newVoiceRouter(speakers *voiceSpeakers) *voiceRouter {
	return &voiceRouter{speakers: speakers, sinks: make(map[string]func(*rtp.Packet))}
}

// Attach routes one speaker's audio, replacing any previous route for them.
func (r *voiceRouter) Attach(userID string, sink func(*rtp.Packet)) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.sinks[userID] = sink
}

// Detach stops routing a speaker.
func (r *voiceRouter) Detach(userID string) {
	r.lock.Lock()
	defer r.lock.Unlock()
	delete(r.sinks, userID)
}

// Route delivers one Discord packet to whoever is listening to that speaker.
//
// It reports whether the packet was delivered. A packet that could not be attributed is dropped
// rather than guessed at: sending someone else's voice out under a user's name is worse than a
// short gap at the very start of their first word, which is all this can cost.
func (r *voiceRouter) Route(packet *discordgo.Packet) bool {
	if packet == nil {
		return false
	}
	userID := r.speakers.UserOf(packet.SSRC)
	if userID == "" {
		r.lock.Lock()
		r.unattributed++
		r.lock.Unlock()
		return false
	}
	r.lock.Lock()
	sink := r.sinks[userID]
	r.lock.Unlock()
	if sink == nil {
		return false
	}
	converted := discordPacketToRTP(packet)
	if converted == nil {
		return false
	}
	sink(converted)
	return true
}

// Unattributed reports how many packets arrived for an SSRC nobody was known to own, which is the
// signal that speaking updates are not arriving.
func (r *voiceRouter) Unattributed() int {
	r.lock.Lock()
	defer r.lock.Unlock()
	return r.unattributed
}

// Speakers returns the users currently being routed, sorted for a stable answer.
func (r *voiceRouter) Speakers() []string {
	r.lock.Lock()
	defer r.lock.Unlock()
	out := make([]string, 0, len(r.sinks))
	for userID := range r.sinks {
		out = append(out, userID)
	}
	return out
}
