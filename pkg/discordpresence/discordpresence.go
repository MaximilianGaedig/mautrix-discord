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

// Package discordpresence holds the decisions of Discord presence bridging that need no session:
// what a Discord status means in Matrix, what counts as someone being active, and who went away
// while the gateway was not looking. It is kept apart from the connector so that it tests alone.
package discordpresence

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/event"
)

// MapStatus converts a Discord status to Matrix presence. Do-not-disturb is someone who is there
// and wants no notifications, so they are online. Idle is someone who has stopped using Discord:
// that is Matrix's "unavailable", which the presence manager sends as the one "offline" of the
// moment they went away. Invisible is how a user's own sessions see them; to everyone else they
// are offline. An unknown status says nothing.
func MapStatus(status discordgo.Status) (event.Presence, bool) {
	switch status {
	case discordgo.StatusOnline, discordgo.StatusDoNotDisturb:
		return event.PresenceOnline, true
	case discordgo.StatusIdle:
		return event.PresenceUnavailable, true
	case discordgo.StatusOffline, discordgo.StatusInvisible:
		return event.PresenceOffline, true
	default:
		return "", false
	}
}

// ActivityOf returns who did something and when, for the gateway events that mean a person was
// at Discord just then: sending a message, typing and reacting. Bots, webhooks and the system
// user are not people. now stands in for the events Discord does not timestamp.
func ActivityOf(rawEvt any, selfID string, now time.Time) (userID string, at time.Time, ok bool) {
	switch evt := rawEvt.(type) {
	case *discordgo.MessageCreate:
		if evt.Message == nil || evt.Author == nil || evt.Author.Bot || evt.Author.System || evt.WebhookID != "" {
			return "", time.Time{}, false
		}
		userID, at = evt.Author.ID, evt.Timestamp
	case *discordgo.TypingStart:
		userID, at = evt.UserID, now
		if evt.Timestamp > 0 {
			at = time.Unix(int64(evt.Timestamp), 0)
		}
	case *discordgo.MessageReactionAdd:
		if evt.MessageReaction == nil {
			return "", time.Time{}, false
		}
		userID, at = evt.UserID, now
	default:
		return "", time.Time{}, false
	}
	if userID == "" || userID == selfID {
		return "", time.Time{}, false
	}
	return userID, at, true
}

// Entry is one user's status in a snapshot.
type Entry struct {
	UserID string
	Status discordgo.Status
}

// FromReady lists the presences of a READY payload.
func FromReady(presences []*discordgo.Presence) []Entry {
	entries := make([]Entry, 0, len(presences))
	for _, p := range presences {
		if p != nil && p.User != nil && p.User.ID != "" {
			entries = append(entries, Entry{UserID: p.User.ID, Status: p.Status})
		}
	}
	return entries
}

// ErrNoMergedPresences means a READY_SUPPLEMENTAL payload said nothing about presence at all,
// which is not the same as saying that nobody is online.
var ErrNoMergedPresences = errors.New("no merged_presences in READY_SUPPLEMENTAL")

type mergedPresence struct {
	UserID string           `json:"user_id"`
	User   *discordgo.User  `json:"user"`
	Status discordgo.Status `json:"status"`
}

// FromSupplemental lists the presences of a READY_SUPPLEMENTAL payload, where user accounts get
// them: merged_presences holds the friends who are online and, guild by guild, the members
// Discord chose to tell this session about. discordgo does not parse the field.
func FromSupplemental(raw json.RawMessage) ([]Entry, error) {
	var payload struct {
		MergedPresences *struct {
			Friends []mergedPresence   `json:"friends"`
			Guilds  [][]mergedPresence `json:"guilds"`
		} `json:"merged_presences"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	} else if payload.MergedPresences == nil {
		return nil, ErrNoMergedPresences
	}
	var entries []Entry
	add := func(list []mergedPresence) {
		for _, p := range list {
			id := p.UserID
			if id == "" && p.User != nil {
				id = p.User.ID
			}
			if id != "" {
				entries = append(entries, Entry{UserID: id, Status: p.Status})
			}
		}
	}
	add(payload.MergedPresences.Friends)
	for _, guild := range payload.MergedPresences.Guilds {
		add(guild)
	}
	return entries, nil
}

// Tracker remembers who a session last saw online. Discord only says that someone went offline
// to a session that is connected: after a reconnect that could not resume, those who left in the
// meantime are simply missing from the new snapshot, and would stay online in Matrix forever.
type Tracker struct {
	lock   sync.Mutex
	online map[string]struct{}
}

// Set records one user's presence.
func (t *Tracker) Set(userID string, presence event.Presence) {
	t.lock.Lock()
	defer t.lock.Unlock()
	if presence != event.PresenceOnline {
		delete(t.online, userID)
		return
	}
	if t.online == nil {
		t.online = make(map[string]struct{})
	}
	t.online[userID] = struct{}{}
}

// Gone returns the users last seen online who are not in a new snapshot, and forgets them. The
// snapshot's own entries are for the caller to record with Set.
func (t *Tracker) Gone(snapshot []Entry) []string {
	present := make(map[string]struct{}, len(snapshot))
	for _, e := range snapshot {
		present[e.UserID] = struct{}{}
	}
	t.lock.Lock()
	defer t.lock.Unlock()
	var gone []string
	for id := range t.online {
		if _, ok := present[id]; !ok {
			gone = append(gone, id)
			delete(t.online, id)
		}
	}
	return gone
}
