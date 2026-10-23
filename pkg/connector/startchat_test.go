package connector

import (
	"encoding/json"
	"testing"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

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
