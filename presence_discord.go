// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/presence"
)

// discordOnlineTTL bounds how long an online/idle status is trusted without a
// new PRESENCE_UPDATE. Discord reliably sends offline updates while connected,
// but a full reconnect (not a resume) only lists who is online, so users who
// went offline in between would otherwise stay online forever.
const discordOnlineTTL = 2 * time.Hour

// mapDiscordStatus converts a Discord status to Matrix presence.
func mapDiscordStatus(status discordgo.Status, now time.Time) (presence.State, bool) {
	switch status {
	case discordgo.StatusOnline, discordgo.StatusDoNotDisturb:
		return presence.State{Presence: event.PresenceOnline, Until: now.Add(discordOnlineTTL)}, true
	case discordgo.StatusIdle:
		return presence.State{Presence: event.PresenceUnavailable}, true
	case discordgo.StatusOffline, discordgo.StatusInvisible:
		return presence.State{Presence: event.PresenceOffline}, true
	default:
		return presence.State{}, false
	}
}

func (br *DiscordBridge) startPresence() {
	if !br.Config.Bridge.PresenceBridging {
		return
	}
	br.presence = presence.NewManager(presence.Config{
		Refresh:       time.Duration(br.Config.Bridge.PresenceRefreshSeconds) * time.Second,
		RatePerSecond: br.Config.Bridge.PresenceMaxPerSecond,
	}, br.sendPuppetPresence)
	log := br.ZLog.With().Str("component", "presence").Logger()
	go br.presence.Run(log.WithContext(context.Background()))
}

// sendPuppetPresence sets presence on an existing puppet; unknown users are
// skipped so presence alone never creates puppets.
func (br *DiscordBridge) sendPuppetPresence(_ context.Context, userID string, p event.Presence) error {
	puppet := br.GetExistingPuppetByID(userID)
	if puppet == nil {
		return nil
	}
	intent := puppet.DefaultIntent()
	if err := intent.EnsureRegistered(); err != nil {
		return fmt.Errorf("failed to ensure puppet is registered: %w", err)
	}
	return intent.SetPresence(p)
}

func (br *DiscordBridge) handleDiscordPresence(user *discordgo.User, status discordgo.Status) {
	if br.presence == nil || user == nil || user.ID == "" {
		return
	}
	br.updateDiscordPresence(user.ID, status)
}

func (br *DiscordBridge) updateDiscordPresence(userID string, status discordgo.Status) {
	if br.GetCachedUserByID(userID) != nil {
		// Don't touch the presence of bridge users' own puppets.
		return
	}
	if st, ok := mapDiscordStatus(status, time.Now()); ok {
		br.presence.Update(userID, st)
	}
}

type mergedPresence struct {
	UserID string           `json:"user_id"`
	User   *discordgo.User  `json:"user"`
	Status discordgo.Status `json:"status"`
}

type readySupplementalPresences struct {
	MergedPresences struct {
		Friends []mergedPresence   `json:"friends"`
		Guilds  [][]mergedPresence `json:"guilds"`
	} `json:"merged_presences"`
}

// parseReadySupplementalPresences extracts merged_presences, which the
// discordgo ReadySupplemental struct doesn't include. User accounts with the
// PRIORITIZED_READY_PAYLOAD capability get friends' initial presences here
// instead of in READY.
func parseReadySupplementalPresences(raw json.RawMessage) ([]mergedPresence, error) {
	var data readySupplementalPresences
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	out := data.MergedPresences.Friends
	for _, guild := range data.MergedPresences.Guilds {
		out = append(out, guild...)
	}
	return out, nil
}

func (br *DiscordBridge) handleReadySupplementalPresences(raw json.RawMessage) {
	if br.presence == nil {
		return
	}
	presences, err := parseReadySupplementalPresences(raw)
	if err != nil {
		br.ZLog.Warn().Err(err).Msg("Failed to parse presences in READY_SUPPLEMENTAL")
		return
	}
	for _, p := range presences {
		userID := p.UserID
		if userID == "" && p.User != nil {
			userID = p.User.ID
		}
		if userID != "" {
			br.updateDiscordPresence(userID, p.Status)
		}
	}
}
