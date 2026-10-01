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
	"errors"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/pkg/discordid"
	"go.mau.fi/mautrix-discord/pkg/discordpresence"
	"go.mau.fi/mautrix-discord/pkg/presence"
)

func (d *DiscordConnector) startPresence() {
	if !d.Config.PresenceBridging {
		return
	}
	d.presence = presence.NewManager(presence.Config{
		// Discord's status changes are deliberate already; forward them quickly.
		Debounce: 2 * time.Second,
	}, presence.GhostSender(d.Bridge))
	log := d.Bridge.Log.With().Str("component", "presence").Logger()
	go d.presence.Run(log.WithContext(d.Bridge.BackgroundCtx))
}

// handlePresenceEvent bridges what the gateway says about who is online: PRESENCE_UPDATE, and
// the snapshots in READY, READY_SUPPLEMENTAL and guild member chunks. Nothing is asked of
// Discord for it. It runs on the gateway's own goroutine so that two updates for one user are
// applied in the order they came, and is cheap enough for that: a map lookup per presence.
func (d *DiscordClient) handlePresenceEvent(ctx context.Context, rawEvt any) {
	if d.connector.presence == nil {
		return
	}
	switch evt := rawEvt.(type) {
	case *discordgo.PresenceUpdate:
		if evt.User != nil {
			d.applyPresence(discordpresence.Entry{UserID: evt.User.ID, Status: evt.Status})
		}
	case *discordgo.GuildMembersChunk:
		d.applyPresences(discordpresence.FromReady(evt.Presences))
	case *discordgo.Ready:
		// Kept for READY_SUPPLEMENTAL, which has the rest of the snapshot.
		d.readyPresences = discordpresence.FromReady(evt.Presences)
		d.applyPresences(d.readyPresences)
	case *discordgo.Event:
		if evt.Type != "READY_SUPPLEMENTAL" {
			return
		}
		entries, err := discordpresence.FromSupplemental(evt.RawData)
		if err != nil {
			// Without the snapshot there is no telling who left, so nobody is marked offline.
			if !errors.Is(err, discordpresence.ErrNoMergedPresences) {
				zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to parse presences in supplemental READY")
			}
			return
		}
		snapshot := append(d.readyPresences, entries...)
		d.readyPresences = nil
		// A READY is a new session: whoever went offline while there was none was never
		// announced, and is only missing here.
		for _, userID := range d.presenceTracker.Gone(snapshot) {
			d.connector.presence.Update(string(discordid.MakeUserID(userID)), presence.State{Presence: event.PresenceOffline})
		}
		d.applyPresences(entries)
	}
}

func (d *DiscordClient) applyPresences(entries []discordpresence.Entry) {
	for _, entry := range entries {
		d.applyPresence(entry)
	}
}

// applyPresence passes one user's status on, unless it is the user's own or of someone the bridge
// has never dealt with: a session in large guilds hears about far more people than have ghosts,
// and each of them would spend a homeserver request slot to find that out.
func (d *DiscordClient) applyPresence(entry discordpresence.Entry) {
	if entry.UserID == discordid.ParseUserLoginID(d.UserLogin.ID) || !d.userCache.Known(entry.UserID) {
		return
	}
	mapped, ok := discordpresence.MapStatus(entry.Status)
	if !ok {
		return
	}
	d.presenceTracker.Set(entry.UserID, mapped)
	d.connector.presence.Update(string(discordid.MakeUserID(entry.UserID)), presence.State{Presence: mapped})
}

// notePresenceActivity marks the sender of a message, typing notification or reaction online for
// a while (presence.Manager.Activity): what someone does is the most accurate presence there is,
// and the only one for people whose status Discord does not send to this session.
func (d *DiscordClient) notePresenceActivity(rawEvt any) {
	if d.connector.presence == nil {
		return
	}
	userID, at, ok := discordpresence.ActivityOf(rawEvt, discordid.ParseUserLoginID(d.UserLogin.ID), time.Now())
	if ok {
		d.connector.presence.Activity(string(discordid.MakeUserID(userID)), at)
	}
}
