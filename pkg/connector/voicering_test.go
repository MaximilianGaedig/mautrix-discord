package connector

import (
	"encoding/json"
	"testing"
)

func TestParseCallEventReadsRingingState(t *testing.T) {
	call, err := parseCallEvent(callCreateEvent, json.RawMessage(`{
		"channel_id": "111", "message_id": "222", "region": "frankfurt",
		"ringing": ["333", "444"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if call.ChannelID != "111" || call.MessageID != "222" || call.Region != "frankfurt" {
		t.Errorf("parsed %+v", call)
	}
	if !call.IsRinging("333") || !call.IsRinging("444") {
		t.Error("the people being rung were not reported as ringing")
	}
	if call.IsRinging("555") {
		t.Error("somebody not in the list was reported as ringing")
	}
	if call.Ended {
		t.Error("a call that was just created is not over")
	}
}

func TestParseCallEventAnsweredCallRingsNobody(t *testing.T) {
	// An answered call still exists, and rings nobody. "Is there a call" and "is anybody being
	// rung" are different questions, and treating the first as the second leaves a phone ringing
	// after it has been picked up.
	call, err := parseCallEvent(callUpdateEvent, json.RawMessage(`{"channel_id": "111", "ringing": []}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(call.Ringing) != 0 || call.IsRinging("333") {
		t.Errorf("an answered call reported ringing: %+v", call)
	}
	if call.Ended {
		t.Error("an answered call is not an ended one")
	}
}

func TestParseCallEventDelete(t *testing.T) {
	call, err := parseCallEvent(callDeleteEvent, json.RawMessage(`{"channel_id": "111"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !call.Ended || call.ChannelID != "111" || call.Ringing != nil {
		t.Errorf("parsed %+v", call)
	}
}

func TestParseCallEventIgnoresEverythingElse(t *testing.T) {
	// The raw-event handler sees every gateway event there is, so anything that is not a call event
	// has to come back as nothing rather than as an empty call.
	for _, name := range []string{"MESSAGE_CREATE", "VOICE_STATE_UPDATE", "READY", ""} {
		call, err := parseCallEvent(name, json.RawMessage(`{"channel_id": "111"}`))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if call != nil {
			t.Errorf("%s was read as a call event", name)
		}
	}
}

func TestParseCallEventRefusesNonsense(t *testing.T) {
	if _, err := parseCallEvent(callCreateEvent, json.RawMessage(`not json`)); err == nil {
		t.Error("a malformed payload was accepted")
	}
	// No channel means there is nothing to ring in, so it is an error rather than a call with an
	// empty channel that later requests would post to /channels//call/ring.
	if _, err := parseCallEvent(callCreateEvent, json.RawMessage(`{"ringing": ["333"]}`)); err == nil {
		t.Error("a call with no channel was accepted")
	}
}

func TestRingEndpoints(t *testing.T) {
	if got := endpointCallRing("111"); got != "https://discord.com/api/v9/channels/111/call/ring" {
		t.Errorf("ring endpoint = %q", got)
	}
	if got := endpointCallStopRinging("111"); got != "https://discord.com/api/v9/channels/111/call/stop-ringing" {
		t.Errorf("stop-ringing endpoint = %q", got)
	}
}

func TestRingBodyDistinguishesEverybodyFromSomebody(t *testing.T) {
	// nil rings everybody in the channel, which is what a one-to-one call wants. A named list is
	// what keeps a group DM from ringing a roomful of people the reader did not ask for, so the two
	// must not serialise the same way.
	everybody, err := json.Marshal(ringRequest{Recipients: nil})
	if err != nil {
		t.Fatal(err)
	}
	if string(everybody) != `{"recipients":null}` {
		t.Errorf("ringing everybody sent %s", everybody)
	}
	somebody, err := json.Marshal(ringRequest{Recipients: []string{"333"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(somebody) != `{"recipients":["333"]}` {
		t.Errorf("ringing one person sent %s", somebody)
	}
}

func TestRingRefusesAnEmptyChannel(t *testing.T) {
	// Without this the URL becomes /channels//call/ring, which is a request to Discord that means
	// nothing and is worth failing before it is sent.
	if err := ringDMCall(nil, "", nil); err == nil {
		t.Error("ringing with no channel was allowed")
	}
	if err := stopRingingDMCall(nil, "", nil); err == nil {
		t.Error("stopping with no channel was allowed")
	}
}
