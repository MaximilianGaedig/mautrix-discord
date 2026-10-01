package msgconv

import (
	"context"
	"encoding/json"
	"html"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

// componentMessageJSON is a bot message the way the gateway sends it, so the
// test also covers the types discordgo decodes components into.
const componentMessageJSON = `{
	"id": "900000000000000201",
	"channel_id": "900000000000000202",
	"author": {"id": "900000000000000203", "username": "bot", "bot": true},
	"content": "Pick something",
	"components": [
		{"type": 1, "components": [
			{"type": 2, "style": 1, "label": "Accept", "custom_id": "a", "emoji": {"name": "✅"}},
			{"type": 2, "style": 4, "label": "Decline <now>", "custom_id": "d"},
			{"type": 2, "style": 5, "label": "Docs", "url": "https://example.test/docs?a=1&b=2"}
		]},
		{"type": 1, "components": [
			{"type": 3, "custom_id": "color", "placeholder": "Favourite colour", "options": [
				{"label": "Red", "value": "r", "emoji": {"name": "red", "id": "900000000000000210"}},
				{"label": "Green", "value": "g", "default": true}
			]}
		]},
		{"type": 1, "components": [
			{"type": 5, "custom_id": "who", "placeholder": "Assignee"}
		]}
	]
}`

func convertComponentMessage(t *testing.T, raw string) string {
	t.Helper()
	var msg discordgo.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("decoding the test message: %v", err)
	}
	mc := &MessageConverter{Bridge: &bridgev2.Bridge{Matrix: fakeMatrix{}}}
	source := &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "900000000000000099"}}
	conv := mc.ToMatrix(context.Background(), &bridgev2.Portal{}, nil, source, nil, &msg, nil)
	if len(conv.Parts) != 1 {
		t.Fatalf("got %d parts, want the text part only", len(conv.Parts))
	}
	return conv.Parts[0].Content.FormattedBody
}

func TestComponentsAreListed(t *testing.T) {
	got := convertComponentMessage(t, componentMessageJSON)
	for _, want := range []string{
		"Pick something",
		"Buttons:<br>✅ Accept | Decline &lt;now&gt; | " +
			`<a href="https://example.test/docs?a=1&amp;b=2">Docs</a>`,
		"<br>Menu “Favourite colour”: :red: Red | <strong>Green</strong>",
		"<br>Menu “Assignee” (pick a user)",
		"<br>Use the Discord app to press the buttons and to pick from the menus.</p>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("converted message lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "interactive elements") {
		t.Errorf("converted message still has the generic notice:\n%s", got)
	}
}

func TestComponentsAppNote(t *testing.T) {
	render := func(s string) string { return "<p>" + html.EscapeString(s) + "</p>" }
	link := &discordgo.Button{Style: discordgo.LinkButton, Label: "Open", URL: "https://example.test/"}
	tests := []struct {
		name       string
		components []discordgo.MessageComponent
		want       []string
	}{{
		name:       "only link buttons need no app",
		components: []discordgo.MessageComponent{&discordgo.ActionsRow{Components: []discordgo.MessageComponent{link}}},
		want:       []string{`<p>Buttons:<br><a href="https://example.test/">Open</a></p>`},
	}, {
		name: "a disabled button can't be pressed anywhere",
		components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Expired", Disabled: true, CustomID: "x"},
		}}},
		want: []string{`<p>Buttons:<br><del>Expired</del></p>`},
	}, {
		name: "only buttons",
		components: []discordgo.MessageComponent{&discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			&discordgo.Button{Emoji: &discordgo.ComponentEmoji{Name: "🎲"}, CustomID: "roll"}, link,
		}}},
		want: []string{`<p>Buttons:<br>🎲 | <a href="https://example.test/">Open</a><br>Use the Discord app to press the buttons.</p>`},
	}, {
		name: "only a menu, with more options than are listed",
		components: []discordgo.MessageComponent{&discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			&discordgo.SelectMenu{MenuType: discordgo.StringSelectMenu, Options: manyOptions(12)},
		}}},
		want: []string{`<p>Menu: o0 | o1 | o2 | o3 | o4 | o5 | o6 | o7 | o8 | o9 | …<br>Use the Discord app to pick from the menus.</p>`},
	}, {
		name: "components v2 carry the text themselves",
		components: []discordgo.MessageComponent{&discordgo.Container{Components: []discordgo.MessageComponent{
			&discordgo.TextDisplay{Content: "Hello <world>"},
			&discordgo.Separator{},
			&discordgo.Section{
				Components: []discordgo.MessageComponent{&discordgo.TextDisplay{Content: "Details"}},
				Accessory:  &discordgo.Button{Label: "More", CustomID: "more"},
			},
			&discordgo.MediaGallery{},
		}}},
		want: []string{
			"<p>Hello &lt;world&gt;</p>", "<p>Details</p>",
			"<p>Buttons:<br>More<br>Use the Discord app to press the buttons. Some parts of this message are only shown in the Discord app.</p>",
		},
	}, {
		name:       "nothing to show",
		components: []discordgo.MessageComponent{&discordgo.Separator{}},
		want:       nil,
	}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := renderComponentsHTML(test.components, render)
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Errorf("got  %q\nwant %q", got, test.want)
			}
		})
	}
}

func manyOptions(n int) []discordgo.SelectMenuOption {
	options := make([]discordgo.SelectMenuOption, n)
	for i := range options {
		options[i] = discordgo.SelectMenuOption{Label: "o" + string(rune('0'+i%10)), Value: "v"}
	}
	return options
}
