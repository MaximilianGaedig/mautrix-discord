package connector

import (
	"encoding/json"
	"testing"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func testUsers() map[string]*discordgo.User {
	return map[string]*discordgo.User{
		"100000000000000001": {ID: "100000000000000001", Username: "alice", Discriminator: "0", GlobalName: "Alice A"},
		"100000000000000002": {ID: "100000000000000002", Username: "bobby", Discriminator: "0", GlobalName: "Robert"},
		"100000000000000003": {ID: "100000000000000003", Username: "carol", Discriminator: "1234"},
		"100000000000000009": {ID: "100000000000000009", Username: "me", Discriminator: "0"},
	}
}

func TestParseIdentifier(t *testing.T) {
	cases := []struct {
		in   string
		want parsedIdentifier
		ok   bool
	}{
		{"100000000000000001", parsedIdentifier{ID: "100000000000000001"}, true},
		{"<@100000000000000001>", parsedIdentifier{ID: "100000000000000001"}, true},
		{"<@!100000000000000001>", parsedIdentifier{ID: "100000000000000001"}, true},
		{"@Alice", parsedIdentifier{Username: "alice"}, true},
		{"  carol#1234 ", parsedIdentifier{Username: "carol#1234"}, true},
		{"12345", parsedIdentifier{Username: "12345"}, true},
		{"  ", parsedIdentifier{}, false},
		{"@", parsedIdentifier{}, false},
	}
	for _, c := range cases {
		got, ok := parseIdentifier(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseIdentifier(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestFindUserByUsername(t *testing.T) {
	u := testUsers()
	friends := []*discordgo.User{u["100000000000000001"]}
	known := []*discordgo.User{u["100000000000000003"], u["100000000000000002"]}
	if got := findUserByUsername(friends, known, "alice"); got == nil || got.ID != "100000000000000001" {
		t.Errorf("friend lookup failed: %v", got)
	}
	if got := findUserByUsername(friends, known, "carol#1234"); got == nil || got.ID != "100000000000000003" {
		t.Errorf("discriminator lookup failed: %v", got)
	}
	if got := findUserByUsername(friends, known, "nobody"); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestFriendUsers(t *testing.T) {
	u := testUsers()
	rels := []*discordgo.Relationship{
		{ID: "100000000000000002", Type: discordgo.RelationshipFriend},
		{ID: "100000000000000003", Type: discordgo.RelationshipBlocked},
		{ID: "100000000000000001", Type: discordgo.RelationshipFriend},
		{ID: "100000000000000004", Type: discordgo.RelationshipFriend}, // unknown user
		{ID: "100000000000000009", Type: discordgo.RelationshipIncomingFriendRequest},
		nil,
	}
	got := friendUsers(rels, func(id string) *discordgo.User { return u[id] })
	if len(got) != 2 || got[0].ID != "100000000000000001" || got[1].ID != "100000000000000002" {
		t.Fatalf("unexpected friends: %v", got)
	}
}

func TestSearchUsers(t *testing.T) {
	u := testUsers()
	friends := []*discordgo.User{u["100000000000000002"]}
	known := []*discordgo.User{u["100000000000000001"], u["100000000000000002"], u["100000000000000003"], u["100000000000000009"]}

	got := searchUsers(friends, known, "@ROBE", "100000000000000009")
	if len(got) != 1 || got[0].ID != "100000000000000002" {
		t.Errorf("display name search: %v", got)
	}
	// "a" matches alice, carol; friend bobby doesn't contain it in username/global "Robert"... no 'a'.
	got = searchUsers(friends, known, "a", "100000000000000009")
	if len(got) != 2 || got[0].ID != "100000000000000001" || got[1].ID != "100000000000000003" {
		t.Errorf("substring search: %v", got)
	}
	// Friends first, no duplicates, self excluded.
	got = searchUsers(friends, known, "b", "100000000000000009")
	if len(got) != 1 || got[0].ID != "100000000000000002" {
		t.Errorf("dedupe: %v", got)
	}
	if got = searchUsers(friends, known, "me", "100000000000000009"); len(got) != 0 {
		t.Errorf("self must be excluded: %v", got)
	}
	if got = searchUsers(friends, known, "  ", "x"); got != nil {
		t.Errorf("empty query must return nothing: %v", got)
	}
}

func TestNewGroupDMRequest(t *testing.T) {
	ids := []networkid.UserID{"100000000000000001", "100000000000000002", "100000000000000001", "100000000000000009"}
	req, err := newGroupDMRequest(ids, "100000000000000009")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(req)
	if string(body) != `{"recipients":["100000000000000001","100000000000000002"]}` {
		t.Errorf("payload: %s", body)
	}
	if _, err = newGroupDMRequest(ids[:1], "100000000000000009"); err == nil {
		t.Error("one recipient must be rejected")
	}
	many := make([]networkid.UserID, 10)
	for i := range many {
		many[i] = networkid.UserID("2000000000000000" + string(rune('0'+i)) + "0")
	}
	if _, err = newGroupDMRequest(many, "1"); err == nil {
		t.Error("ten recipients must be rejected")
	}
}
