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
