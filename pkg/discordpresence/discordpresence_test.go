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

package discordpresence

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/event"
)

func TestMapStatus(t *testing.T) {
	tests := map[discordgo.Status]event.Presence{
		discordgo.StatusOnline:       event.PresenceOnline,
		discordgo.StatusDoNotDisturb: event.PresenceOnline,
		discordgo.StatusIdle:         event.PresenceUnavailable,
		discordgo.StatusOffline:      event.PresenceOffline,
		discordgo.StatusInvisible:    event.PresenceOffline,
	}
	for status, want := range tests {
		if got, ok := MapStatus(status); !ok || got != want {
			t.Errorf("%q: got %q, %v, want %q", status, got, ok, want)
		}
	}
	for _, status := range []discordgo.Status{"", "streaming"} {
		if got, ok := MapStatus(status); ok {
			t.Errorf("%q says nothing, got %q", status, got)
		}
	}
}

func TestActivityOf(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	sent := now.Add(-2 * time.Second)
	message := func(author *discordgo.User, webhook string) *discordgo.MessageCreate {
		return &discordgo.MessageCreate{Message: &discordgo.Message{Author: author, WebhookID: webhook, Timestamp: sent}}
	}
	tests := []struct {
		name string
		evt  any
		who  string
		at   time.Time
	}{
		{"a message", message(&discordgo.User{ID: "1"}, ""), "1", sent},
		{"our own message", message(&discordgo.User{ID: "me"}, ""), "", time.Time{}},
		{"a bot's message", message(&discordgo.User{ID: "2", Bot: true}, ""), "", time.Time{}},
		{"a webhook's message", message(&discordgo.User{ID: "3"}, "77"), "", time.Time{}},
		{"a message without an author", message(nil, ""), "", time.Time{}},
		{"typing", &discordgo.TypingStart{UserID: "1", Timestamp: int(sent.Unix())}, "1", sent},
		{"typing without a time", &discordgo.TypingStart{UserID: "1"}, "1", now},
		{"our own typing", &discordgo.TypingStart{UserID: "me"}, "", time.Time{}},
		{"a reaction", &discordgo.MessageReactionAdd{MessageReaction: &discordgo.MessageReaction{UserID: "1"}}, "1", now},
		{"our own reaction", &discordgo.MessageReactionAdd{MessageReaction: &discordgo.MessageReaction{UserID: "me"}}, "", time.Time{}},
		{"taking a reaction back is not announced with a time or a reason to trust", &discordgo.MessageReactionRemove{MessageReaction: &discordgo.MessageReaction{UserID: "1"}}, "", time.Time{}},
		{"presence itself is not activity", &discordgo.PresenceUpdate{}, "", time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			who, at, ok := ActivityOf(tt.evt, "me", now)
			if ok != (tt.who != "") || who != tt.who || !at.Equal(tt.at) {
				t.Errorf("got %q at %v (%v), want %q at %v", who, at, ok, tt.who, tt.at)
			}
		})
	}
}

func TestFromReady(t *testing.T) {
	got := FromReady([]*discordgo.Presence{
		{User: &discordgo.User{ID: "1"}, Status: discordgo.StatusOnline},
		nil,
		{Status: discordgo.StatusIdle},
		{User: &discordgo.User{ID: "2"}, Status: discordgo.StatusIdle},
	})
	want := []Entry{{"1", discordgo.StatusOnline}, {"2", discordgo.StatusIdle}}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestFromSupplemental(t *testing.T) {
	raw := json.RawMessage(`{
		"merged_members": [],
		"merged_presences": {
			"friends": [{"user_id": "1", "status": "online", "client_status": {"mobile": "online"}, "activities": []}],
			"guilds": [
				[{"user_id": "2", "status": "idle"}, {"user": {"id": "3"}, "status": "dnd"}],
				[],
				[{"status": "online"}]
			]
		}
	}`)
	got, err := FromSupplemental(raw)
	want := []Entry{{"1", discordgo.StatusOnline}, {"2", discordgo.StatusIdle}, {"3", discordgo.StatusDoNotDisturb}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("got %+v, %v", got, err)
	}
	// Nobody online is an answer; no answer is not.
	if got, err = FromSupplemental(json.RawMessage(`{"merged_presences": {"friends": [], "guilds": []}}`)); err != nil || len(got) != 0 {
		t.Errorf("empty: got %+v, %v", got, err)
	}
	if _, err = FromSupplemental(json.RawMessage(`{"merged_members": []}`)); !errors.Is(err, ErrNoMergedPresences) {
		t.Errorf("missing: got %v", err)
	}
	if _, err = FromSupplemental(json.RawMessage(`[`)); err == nil {
		t.Error("broken JSON must not read as nobody online")
	}
}

func TestTrackerGone(t *testing.T) {
	var tr Tracker
	if gone := tr.Gone(nil); len(gone) != 0 {
		t.Fatalf("nobody was online, got %v", gone)
	}
	tr.Set("1", event.PresenceOnline)
	tr.Set("2", event.PresenceOnline)
	tr.Set("3", event.PresenceOnline)
	tr.Set("3", event.PresenceOffline) // seen leaving: already told
	tr.Set("4", event.PresenceUnavailable)
	gone := tr.Gone([]Entry{{"1", discordgo.StatusOnline}, {"9", discordgo.StatusOnline}})
	if !slices.Equal(gone, []string{"2"}) {
		t.Fatalf("got %v, want only the one who vanished while we were away", gone)
	}
	if gone = tr.Gone([]Entry{{"1", discordgo.StatusOnline}}); len(gone) != 0 {
		t.Errorf("told twice: %v", gone)
	}
	if gone = tr.Gone(nil); !slices.Equal(gone, []string{"1"}) {
		t.Errorf("an empty snapshot means everyone left, got %v", gone)
	}
}
