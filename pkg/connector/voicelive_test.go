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
	"os"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/pion/rtp"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2/callbridge"
)

/*
 * The Discord voice pipeline, carried by a real SFU.
 *
 * Everything between a Discord packet arriving and a Matrix participant hearing it is exercised
 * here: the SSRC-to-speaker mapping, the router, the RTP rebuild, a LiveKit leg published as a real
 * Matrix identity, and a second participant subscribing to it. The only part simulated is Discord's
 * own gateway, because joining a voice channel needs a logged-in Discord account.
 *
 * Skipped unless the homeserver credentials are in the environment, so the ordinary suite stays
 * offline. Credentials are never logged.
 */
func TestDiscordVoiceReachesAMatrixParticipant(t *testing.T) {
	server, domain := os.Getenv("MX_SERVER"), os.Getenv("MX_DOMAIN")
	user, pass := os.Getenv("MX_USER"), os.Getenv("MX_PASS")
	if server == "" || domain == "" || user == "" || pass == "" {
		t.Skip("set MX_SERVER, MX_DOMAIN, MX_USER and MX_PASS to run against a real homeserver")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cli, err := mautrix.NewClient(server, "", "")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if _, err = cli.Login(ctx, &mautrix.ReqLogin{
		Type:             mautrix.AuthTypePassword,
		Identifier:       mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: user},
		Password:         pass,
		StoreCredentials: true,
	}); err != nil {
		t.Fatalf("login: %v", err)
	}

	room, err := cli.CreateRoom(ctx, &mautrix.ReqCreateRoom{Name: "discord voice check", Preset: "private_chat"})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	t.Cleanup(func() { _, _ = cli.LeaveRoom(context.Background(), room.RoomID) })

	focus, err := callbridge.DiscoverRTCTransport(ctx, cli, domain)
	if err != nil {
		t.Fatalf("discover focus: %v", err)
	}

	join := func(device string) *callbridge.RTCLeg {
		t.Helper()
		url, token, err := callbridge.LiveKitToken(ctx, cli, focus, room.RoomID, device)
		if err != nil {
			t.Fatalf("token for %s: %v", device, err)
		}
		leg, err := callbridge.JoinRTC(ctx, callbridge.RTCLegConfig{URL: url, Token: token, Log: zerolog.Nop()})
		if err != nil {
			t.Fatalf("join as %s: %v", device, err)
		}
		t.Cleanup(leg.Close)
		return leg
	}

	// The leg that carries one Discord speaker, and a Matrix participant listening to the call.
	speakerLeg := join("DCSPEAKER")
	listener := join("DCLISTENER")

	// The bridge's own plumbing, wired exactly as voiceCall wires it.
	speakers := newVoiceSpeakers()
	router := newVoiceRouter(speakers)
	writer := speakerLeg.AudioWriter()
	router.Attach("alice", func(p *rtp.Packet) {
		if err := writer.WriteRTP(p); err != nil {
			t.Logf("write: %v", err)
		}
	})
	// The speaking update Discord would have sent, which is the only thing that says whose audio
	// this SSRC carries.
	speakers.Learn("alice", 12345)

	arrived := make(chan int, 1)
	go func() {
		track, trackErr := listener.RemoteAudio(ctx)
		if trackErr != nil {
			arrived <- 0
			return
		}
		count := 0
		for count < 10 {
			if _, _, readErr := track.ReadRTP(); readErr != nil {
				break
			}
			count++
		}
		arrived <- count
	}()

	// Feed packets shaped exactly as discordgo delivers them.
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(60 * time.Second)
	sent := 0
	for {
		select {
		case got := <-arrived:
			if got == 0 {
				t.Fatalf("the Matrix participant received nothing after %d Discord packets", sent)
			}
			t.Logf("%d packets reached the Matrix participant (of %d Discord packets fed in)", got, sent)
			if routerDropped := router.Unattributed(); routerDropped != 0 {
				t.Errorf("%d packets were dropped as unattributed; the speaking update should have covered them", routerDropped)
			}
			return
		case <-deadline:
			t.Fatalf("nothing reached the Matrix participant within 60s (%d sent)", sent)
		case <-ticker.C:
		}
		if !router.Route(&discordgo.Packet{
			SSRC: 12345, Sequence: uint16(sent), Timestamp: uint32(sent) * 960, Opus: make([]byte, 80),
		}) {
			t.Fatalf("the router refused a packet from a known speaker after %d", sent)
		}
		sent++
	}
}
