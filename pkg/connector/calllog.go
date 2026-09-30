package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/calllog"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

// discordCallMessage is the part of a call message (type 3) that discordgo doesn't decode: who was in the call
// and when it ended. The gateway sends it in MESSAGE_CREATE when the call starts and again in MESSAGE_UPDATE
// when it ends, so it's read from the raw event.
type discordCallMessage struct {
	ID        string            `json:"id"`
	ChannelID string            `json:"channel_id"`
	Type      int               `json:"type"`
	Timestamp time.Time         `json:"timestamp"`
	Author    *discordgo.User   `json:"author"`
	Call      *discordCallState `json:"call"`
}

type discordCallState struct {
	Participants   []string   `json:"participants"`
	EndedTimestamp *time.Time `json:"ended_timestamp"`
}

func parseCallMessage(eventType string, raw json.RawMessage) (*discordCallMessage, error) {
	if eventType != "MESSAGE_CREATE" && eventType != "MESSAGE_UPDATE" {
		return nil, nil
	}
	var msg discordCallMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", eventType, err)
	}
	if msg.Type != int(discordgo.MessageTypeCall) || msg.Call == nil || msg.Author == nil {
		return nil, nil
	}
	return &msg, nil
}

// answered is whether anyone besides the caller was in the call.
func (m *discordCallMessage) answered() bool {
	for _, id := range m.Call.Participants {
		if id != m.Author.ID {
			return true
		}
	}
	return false
}

// callLogger turns Discord's call messages into one editable timeline entry per call.
type callLogger struct {
	log    *calllog.Log
	self   func() string
	sender func(userID string) bridgev2.EventSender
}

func newCallLogger(self func() string, sender func(string) bridgev2.EventSender) *callLogger {
	return &callLogger{log: calllog.New(), self: self, sender: sender}
}

// fromMessage is what a call message changes. Every report describes the whole call so far, so it doesn't
// matter which of the create and the update arrives first, or whether the start was ever seen.
func (cl *callLogger) fromMessage(msg *discordCallMessage, portal networkid.PortalKey, group bool) []bridgev2.RemoteEvent {
	var out []bridgev2.RemoteEvent
	add := func(evt bridgev2.RemoteEvent) {
		if evt != nil {
			out = append(out, evt)
		}
	}
	// Discord doesn't say whether a call had video.
	add(cl.log.Start(msg.ID, calllog.Call{
		Portal:   portal,
		Caller:   cl.sender(msg.Author.ID),
		Group:    group,
		Outgoing: msg.Author.ID == cl.self(),
		Started:  msg.Timestamp,
	}))
	if msg.answered() {
		add(cl.log.Answer(msg.ID, msg.Timestamp))
	}
	if msg.Call.EndedTimestamp != nil {
		add(cl.log.End(msg.ID, *msg.Call.EndedTimestamp))
	}
	return out
}

// handleCallEvent logs a call message from a raw gateway event. It reports whether the event was one.
func (d *DiscordClient) handleCallEvent(ctx context.Context, evt *discordgo.Event) bool {
	msg, err := parseCallMessage(evt.Type, evt.RawData)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to parse call message")
		return false
	}
	if msg == nil {
		return false
	}
	route, err := d.Route(ctx, msg.ChannelID)
	if err != nil || route == nil || route.FromChannel == nil || !channelIsPrivate(route.FromChannel) {
		return true
	}
	group := route.FromChannel.Type == discordgo.ChannelTypeGroupDM
	for _, remote := range d.callLog.fromMessage(msg, route.PortalKey, group) {
		d.UserLogin.QueueRemoteEvent(remote)
	}
	return true
}
