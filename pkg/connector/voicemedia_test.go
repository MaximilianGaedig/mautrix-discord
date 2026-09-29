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
	"github.com/pion/rtp"
)

func voicePacket(ssrc uint32, sequence uint16, timestamp uint32) *discordgo.Packet {
	return &discordgo.Packet{SSRC: ssrc, Sequence: sequence, Timestamp: timestamp, Opus: []byte{1, 2, 3}}
}

func TestSpeakersLearnWhoAnSSRCIs(t *testing.T) {
	s := newVoiceSpeakers()
	s.Learn("alice", 100)

	if got := s.UserOf(100); got != "alice" {
		t.Errorf("SSRC 100 resolved to %q, want alice", got)
	}
	if got := s.SSRCOf("alice"); got != 100 {
		t.Errorf("alice resolved to SSRC %d, want 100", got)
	}
	if got := s.UserOf(999); got != "" {
		t.Errorf("an unknown SSRC resolved to %q, want nothing", got)
	}
}

func TestSpeakersRetireAUsersOldSSRC(t *testing.T) {
	/*
	 * The trap this map exists to avoid.
	 *
	 * A user's SSRC changes when they rejoin, and Discord hands the freed number out again. If the
	 * old number kept resolving to them, the next person to get it would have their voice published
	 * under the wrong name - which is not a glitch, it is one person appearing to say what another
	 * person said.
	 */
	s := newVoiceSpeakers()
	s.Learn("alice", 100)
	s.Learn("alice", 200)

	if got := s.UserOf(100); got != "" {
		t.Errorf("alice's old SSRC still resolves to %q", got)
	}
	if got := s.UserOf(200); got != "alice" {
		t.Errorf("alice's new SSRC resolved to %q, want alice", got)
	}

	// And now someone else inherits the number alice gave up.
	s.Learn("bob", 100)
	if got := s.UserOf(100); got != "bob" {
		t.Errorf("the reused SSRC resolved to %q, want bob", got)
	}
	if got := s.SSRCOf("alice"); got != 200 {
		t.Errorf("alice lost her own SSRC, got %d, want 200", got)
	}
}

func TestSpeakersTakeAnSSRCFromItsPreviousOwner(t *testing.T) {
	// The same collision seen from the other side: if bob is given alice's SSRC, alice must not
	// still be listed as owning it.
	s := newVoiceSpeakers()
	s.Learn("alice", 100)
	s.Learn("bob", 100)

	if got := s.SSRCOf("alice"); got != 0 {
		t.Errorf("alice still claims SSRC %d after bob took it", got)
	}
	if got := s.UserOf(100); got != "bob" {
		t.Errorf("SSRC 100 resolved to %q, want bob", got)
	}
}

func TestSpeakersForgetAndReset(t *testing.T) {
	s := newVoiceSpeakers()
	s.Learn("alice", 100)
	s.Learn("bob", 200)

	s.Forget("alice")
	if got := s.UserOf(100); got != "" {
		t.Errorf("alice's SSRC still resolves to %q after she left", got)
	}
	if got := s.UserOf(200); got != "bob" {
		t.Errorf("forgetting alice disturbed bob, who resolved to %q", got)
	}

	// SSRCs are assigned per connection and mean nothing across one.
	s.Reset()
	if got := s.UserOf(200); got != "" {
		t.Errorf("a mapping survived the reset as %q", got)
	}
}

func TestSpeakersIgnoreNonsense(t *testing.T) {
	s := newVoiceSpeakers()
	s.Learn("", 100)
	s.Learn("alice", 0)
	if got := s.UserOf(100); got != "" {
		t.Errorf("an update with no user was recorded as %q", got)
	}
	if got := s.SSRCOf("alice"); got != 0 {
		t.Errorf("an update with no SSRC was recorded as %d", got)
	}
}

func TestDiscordNumberingIsKept(t *testing.T) {
	/*
	 * Discord's sequence and timestamp are carried through rather than replaced.
	 *
	 * Renumbering locally would paper over exactly what the receiver's jitter buffer needs to see:
	 * a gap in the sequence is how it learns a packet was lost, and a timestamp is how it learns
	 * how long a silence was. A locally generated, always-consecutive stream looks perfect and
	 * plays badly.
	 */
	got := discordPacketToRTP(voicePacket(100, 4242, 960000))
	if got == nil {
		t.Fatal("a packet with audio in it produced nothing")
	}
	if got.SequenceNumber != 4242 {
		t.Errorf("sequence number %d, want Discord's 4242", got.SequenceNumber)
	}
	if got.Timestamp != 960000 {
		t.Errorf("timestamp %d, want Discord's 960000", got.Timestamp)
	}
	if got.Version != 2 {
		t.Errorf("RTP version %d, want 2", got.Version)
	}
	// SSRC and payload type belong to the track that sends it, not to Discord.
	if got.SSRC != 0 || got.PayloadType != 0 {
		t.Errorf("SSRC %d / PT %d should be left for the local track to stamp", got.SSRC, got.PayloadType)
	}
}

func TestAnEmptyPacketIsNotAFrame(t *testing.T) {
	// Discord sends silence frames with no payload; forwarding them as RTP would put empty packets
	// into the stream rather than letting the gap speak for itself.
	if got := discordPacketToRTP(&discordgo.Packet{SSRC: 100, Opus: nil}); got != nil {
		t.Error("a packet with no audio produced an RTP packet")
	}
	if got := discordPacketToRTP(nil); got != nil {
		t.Error("a nil packet produced an RTP packet")
	}
}

func TestRouterSendsEachSpeakerToTheirOwnSink(t *testing.T) {
	speakers := newVoiceSpeakers()
	speakers.Learn("alice", 100)
	speakers.Learn("bob", 200)
	router := newVoiceRouter(speakers)

	var toAlice, toBob []*rtp.Packet
	router.Attach("alice", func(p *rtp.Packet) { toAlice = append(toAlice, p) })
	router.Attach("bob", func(p *rtp.Packet) { toBob = append(toBob, p) })

	// Two people talking at once, interleaved on the one socket, which is the normal case.
	router.Route(voicePacket(100, 1, 960))
	router.Route(voicePacket(200, 1, 960))
	router.Route(voicePacket(100, 2, 1920))

	if len(toAlice) != 2 {
		t.Errorf("alice's sink got %d packets, want 2", len(toAlice))
	}
	if len(toBob) != 1 {
		t.Errorf("bob's sink got %d packets, want 1", len(toBob))
	}
}

func TestRouterDropsAudioItCannotAttribute(t *testing.T) {
	/*
	 * Audio can arrive before the speaking update that says whose it is.
	 *
	 * Guessing - sending it to the only attached speaker, say - would publish one person's voice
	 * under another person's name. Dropping costs the start of a first word; guessing costs the
	 * listener's ability to believe who said what.
	 */
	speakers := newVoiceSpeakers()
	router := newVoiceRouter(speakers)
	var received int
	router.Attach("alice", func(*rtp.Packet) { received++ })

	if router.Route(voicePacket(100, 1, 960)) {
		t.Error("a packet from an unknown SSRC was routed")
	}
	if received != 0 {
		t.Errorf("alice's sink got %d packets from an unidentified speaker", received)
	}
	if router.Unattributed() != 1 {
		t.Errorf("counted %d unattributed packets, want 1", router.Unattributed())
	}

	// Once the mapping arrives, the same SSRC routes.
	speakers.Learn("alice", 100)
	if !router.Route(voicePacket(100, 2, 1920)) {
		t.Error("the packet was still dropped after the speaking update arrived")
	}
	if received != 1 {
		t.Errorf("alice's sink got %d packets, want 1", received)
	}
}

func TestRouterIgnoresASpeakerNobodyIsListeningTo(t *testing.T) {
	// Everyone in the channel is heard on the socket, but only the ones attached to a Matrix
	// participant go anywhere. An unattached speaker is not an error.
	speakers := newVoiceSpeakers()
	speakers.Learn("carol", 300)
	router := newVoiceRouter(speakers)

	if router.Route(voicePacket(300, 1, 960)) {
		t.Error("a packet was routed for a speaker with no sink")
	}
	if router.Unattributed() != 0 {
		t.Error("a known speaker with no sink was counted as unattributed")
	}
}

func TestRouterDetachStopsDelivery(t *testing.T) {
	speakers := newVoiceSpeakers()
	speakers.Learn("alice", 100)
	router := newVoiceRouter(speakers)
	var received int
	router.Attach("alice", func(*rtp.Packet) { received++ })
	router.Route(voicePacket(100, 1, 960))

	router.Detach("alice")
	router.Route(voicePacket(100, 2, 1920))
	if received != 1 {
		t.Errorf("alice's sink got %d packets, want 1 - the second came after detaching", received)
	}
	if got := router.Speakers(); !slices.Equal(got, []string{}) && len(got) != 0 {
		t.Errorf("still routing %v after detaching everyone", got)
	}
}
