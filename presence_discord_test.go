package main

import (
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/event"
)

func TestMapDiscordStatus(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		in     discordgo.Status
		want   event.Presence
		ttl    bool
		mapped bool
	}{
		{discordgo.StatusOnline, event.PresenceOnline, true, true},
		{discordgo.StatusDoNotDisturb, event.PresenceOnline, true, true},
		{discordgo.StatusIdle, event.PresenceUnavailable, false, true},
		{discordgo.StatusOffline, event.PresenceOffline, false, true},
		{discordgo.StatusInvisible, event.PresenceOffline, false, true},
		{"", "", false, false},
	}
	for _, c := range cases {
		st, ok := mapDiscordStatus(c.in, now)
		if ok != c.mapped || st.Presence != c.want || st.Until.IsZero() == c.ttl {
			t.Errorf("%q: got (%+v, %v)", c.in, st, ok)
		}
		if c.ttl && !st.Until.Equal(now.Add(discordOnlineTTL)) {
			t.Errorf("%q: wrong expiry %v", c.in, st.Until)
		}
	}
}

func TestParseReadySupplementalPresences(t *testing.T) {
	raw := []byte(`{"merged_members":[],"merged_presences":{
		"friends":[{"user_id":"1","status":"online","client_status":{"desktop":"online"},"activities":[]},
		           {"user_id":"2","status":"idle"}],
		"guilds":[[{"user_id":"3","status":"dnd"}],[]]}}`)
	got, err := parseReadySupplementalPresences(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].UserID != "1" || got[1].Status != discordgo.StatusIdle || got[2].UserID != "3" {
		t.Fatalf("unexpected parse result: %+v", got)
	}
	if got, err := parseReadySupplementalPresences([]byte(`{}`)); err != nil || len(got) != 0 {
		t.Fatalf("empty payload: %v %v", got, err)
	}
}
