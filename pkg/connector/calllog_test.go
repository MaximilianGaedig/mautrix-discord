package connector

import (
	"encoding/json"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/calllog"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
)

const callSelf = "111"

var callPortal = networkid.PortalKey{ID: "dm"}

func testCallLogger() *callLogger {
	return newCallLogger(func() string { return callSelf }, func(id string) bridgev2.EventSender {
		return bridgev2.EventSender{Sender: networkid.UserID(id), IsFromMe: id == callSelf}
	})
}

func callMsg(t *testing.T, eventType, body string) *discordCallMessage {
	t.Helper()
	msg, err := parseCallMessage(eventType, json.RawMessage(body))
	if err != nil || msg == nil {
		t.Fatalf("parse %s: %v %v", body, msg, err)
	}
	return msg
}

const callStart = `{"id":"9","channel_id":"5","type":3,"timestamp":"2026-09-30T10:00:00Z","author":{"id":"222"},"call":{"participants":["222"]}}`

func callBody(t *testing.T, evt bridgev2.RemoteEvent) (bridgev2.RemoteEventType, string) {
	t.Helper()
	m, ok := evt.(*simplevent.Message[*calllog.Call])
	if !ok {
		t.Fatalf("unexpected event %T", evt)
	}
	return m.Type, m.Data.Text()
}

func TestParseCallMessageIgnoresOthers(t *testing.T) {
	if m, err := parseCallMessage("MESSAGE_CREATE", json.RawMessage(`{"id":"1","type":0,"author":{"id":"2"}}`)); m != nil || err != nil {
		t.Fatalf("normal message parsed as call: %v %v", m, err)
	}
	if m, _ := parseCallMessage("TYPING_START", json.RawMessage(callStart)); m != nil {
		t.Fatal("other event parsed as call")
	}
}

func TestCallStartsAndMissed(t *testing.T) {
	cl := testCallLogger()
	evts := cl.fromMessage(callMsg(t, "MESSAGE_CREATE", callStart), callPortal, false)
	if len(evts) != 1 {
		t.Fatalf("got %d events", len(evts))
	}
	if typ, text := callBody(t, evts[0]); typ != bridgev2.RemoteEventMessage || text != "Incoming voice call" {
		t.Fatalf("got %v %q", typ, text)
	}
	ended := strings.Replace(callStart, `"call":{"participants":["222"]}`, `"call":{"participants":["222"],"ended_timestamp":"2026-09-30T10:00:30Z"}`, 1)
	evts = cl.fromMessage(callMsg(t, "MESSAGE_UPDATE", ended), callPortal, false)
	if len(evts) != 1 {
		t.Fatalf("got %d events", len(evts))
	}
	if typ, text := callBody(t, evts[0]); typ != bridgev2.RemoteEventEdit || text != "Missed voice call" {
		t.Fatalf("got %v %q", typ, text)
	}
}

func TestCallAnsweredHasDuration(t *testing.T) {
	cl := testCallLogger()
	cl.fromMessage(callMsg(t, "MESSAGE_CREATE", callStart), callPortal, true)
	ended := strings.Replace(callStart, `"call":{"participants":["222"]}`, `"call":{"participants":["222","111"],"ended_timestamp":"2026-09-30T10:01:14Z"}`, 1)
	evts := cl.fromMessage(callMsg(t, "MESSAGE_UPDATE", ended), callPortal, true)
	last := evts[len(evts)-1]
	if _, text := callBody(t, last); text != "Group voice call, 1:14" {
		t.Fatalf("got %q", text)
	}
}

func TestCallUpdateBeforeCreate(t *testing.T) {
	cl := testCallLogger()
	ended := strings.Replace(callStart, `"call":{"participants":["222"]}`, `"call":{"participants":["222","111"],"ended_timestamp":"2026-09-30T10:00:10Z"}`, 1)
	first := cl.fromMessage(callMsg(t, "MESSAGE_UPDATE", ended), callPortal, false)
	if _, text := callBody(t, first[len(first)-1]); text != "Voice call, 0:10" {
		t.Fatalf("got %q", text)
	}
	// The late create must not bring the call back to ringing.
	if late := cl.fromMessage(callMsg(t, "MESSAGE_CREATE", callStart), callPortal, false); len(late) != 0 {
		t.Fatalf("late create produced %d events", len(late))
	}
}

func TestOutgoingUnansweredIsCancelled(t *testing.T) {
	cl := testCallLogger()
	body := strings.NewReplacer(`"222"`, `"111"`).Replace(callStart)
	body = strings.Replace(body, `"participants":["111"]`, `"participants":["111"],"ended_timestamp":"2026-09-30T10:00:05Z"`, 1)
	evts := cl.fromMessage(callMsg(t, "MESSAGE_CREATE", body), callPortal, false)
	if _, text := callBody(t, evts[len(evts)-1]); text != "Cancelled voice call" {
		t.Fatalf("got %q", text)
	}
}
