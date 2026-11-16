package connector

import (
	"testing"

	"go.mau.fi/mautrix-discord/pkg/msgconv"
)

func TestSetUseDirectMediaFollowsNewMessagesOption(t *testing.T) {
	off, on := false, true
	for _, tc := range []struct {
		name   string
		option *bool
		want   bool
	}{
		{"unset keeps upstream behaviour", nil, true},
		{"true", &on, true},
		{"false serves only", &off, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &DiscordConnector{MsgConv: &msgconv.MessageConverter{}}
			d.Config.DirectMediaNewMessages = tc.option
			d.SetUseDirectMedia()
			if d.MsgConv.DirectMedia != tc.want {
				t.Fatalf("MsgConv.DirectMedia = %v, want %v", d.MsgConv.DirectMedia, tc.want)
			}
		})
	}
}
