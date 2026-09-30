package msgconv

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

func testDiscordPoll() *discordgo.Poll {
	expiry := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	return &discordgo.Poll{
		Question: discordgo.PollMedia{Text: "Best fruit?"},
		Answers: []discordgo.PollAnswer{
			{AnswerID: 1, Media: &discordgo.PollMedia{Text: "Apple"}},
			{AnswerID: 2, Media: &discordgo.PollMedia{Text: "Pear <b>", Emoji: &discordgo.ComponentEmoji{Name: "🍐"}}},
			{AnswerID: 3, Media: &discordgo.PollMedia{Text: "Plum"}},
		},
		Expiry: &expiry,
	}
}

func testMatrixPoll(maxSelections int) *event.PollStartEventContent {
	answer := func(id, text string) event.PollOption {
		return event.PollOption{ID: id, MSC1767Message: event.MSC1767Message{Text: text}}
	}
	return &event.PollStartEventContent{PollStart: event.PollStart{
		Kind:          "org.matrix.msc3381.poll.disclosed",
		MaxSelections: maxSelections,
		Question:      event.MSC1767Message{Text: "Lunch spot?"},
		Answers:       []event.PollOption{answer("a-1", "Noodles"), answer("a-2", "Tacos"), answer("a-3", "Soup")},
	}}
}

func TestPollToMatrix(t *testing.T) {
	poll := testDiscordPoll()
	poll.AllowMultiselect = true

	content, extra := PollToMatrix(poll)

	start, ok := extra["org.matrix.msc3381.poll.start"].(map[string]any)
	if !ok {
		t.Fatal("the poll start field is missing")
	}
	if start["kind"] != "org.matrix.msc3381.poll.disclosed" {
		t.Errorf("unexpected kind %v", start["kind"])
	}
	if start["max_selections"] != 3 {
		t.Errorf("multiselect poll of 3 answers should allow 3 selections, got %v", start["max_selections"])
	}
	question := start["question"].(map[string]any)
	if question["org.matrix.msc1767.text"] != "Best fruit?" {
		t.Errorf("unexpected question %v", question)
	}
	answers := start["answers"].([]map[string]any)
	if len(answers) != 3 {
		t.Fatalf("expected 3 answers, got %d", len(answers))
	}
	if answers[0]["id"] != "1" || answers[2]["id"] != "3" {
		t.Errorf("answer IDs should be the Discord answer IDs, got %v and %v", answers[0]["id"], answers[2]["id"])
	}
	if answers[1]["org.matrix.msc1767.text"] != "🍐 Pear <b>" {
		t.Errorf("unicode emoji should lead the answer text, got %v", answers[1]["org.matrix.msc1767.text"])
	}
	if !strings.Contains(content.Body, "Best fruit?") || !strings.Contains(content.Body, "2. 🍐 Pear <b>") {
		t.Errorf("text fallback is missing the question or answers: %q", content.Body)
	}
	if !strings.Contains(content.FormattedBody, "Pear &lt;b&gt;") {
		t.Errorf("answers must be escaped in the HTML fallback: %q", content.FormattedBody)
	}
	if strings.Contains(content.Body, "ended") {
		t.Errorf("an open poll must not say it ended: %q", content.Body)
	}
	if _, err := json.Marshal(extra); err != nil {
		t.Errorf("extra content doesn't marshal: %v", err)
	}

	poll.AllowMultiselect = false
	_, extra = PollToMatrix(poll)
	if extra["org.matrix.msc3381.poll.start"].(map[string]any)["max_selections"] != 1 {
		t.Error("single choice polls allow one selection")
	}
}

func TestPollToMatrixEnded(t *testing.T) {
	poll := testDiscordPoll()
	poll.Results = &discordgo.PollResults{Finalized: true}
	content, extra := PollToMatrix(poll)
	if !strings.Contains(content.Body, "ended") {
		t.Errorf("an ended poll should say so: %q", content.Body)
	}
	if extra["fi.mau.discord.poll"].(map[string]any)["ended"] != true {
		t.Error("ended flag missing")
	}
}

func TestRenderDiscordPoll(t *testing.T) {
	if renderDiscordPoll(nil) != nil {
		t.Error("no poll, no part")
	}
	if renderDiscordPoll(&discordgo.Poll{}) != nil {
		t.Error("a poll without answers is not bridged as a poll")
	}
	part := renderDiscordPoll(testDiscordPoll())
	if part == nil {
		t.Fatal("expected a part")
	}
	if part.Type != event.EventUnstablePollStart {
		t.Errorf("wrong event type %v", part.Type)
	}
	if part.ID != PollPartID {
		t.Errorf("wrong part ID %q", part.ID)
	}
	meta, ok := part.DBMetadata.(*discordid.MessageMetadata)
	if !ok || meta.Poll == nil {
		t.Fatalf("the poll bookkeeping must be saved with the message, got %#v", part.DBMetadata)
	}
	if meta.Poll.MaxSelections != 1 || meta.Poll.Ended || meta.Poll.Seeded {
		t.Errorf("unexpected bookkeeping %+v", meta.Poll)
	}
	if len(meta.Poll.AnswerIDs) != 3 || meta.Poll.AnswerIDs[2] != 3 {
		t.Errorf("answer IDs missing from bookkeeping: %+v", meta.Poll.AnswerIDs)
	}
}

func TestPollEndedDetection(t *testing.T) {
	if PollEnded(nil) {
		t.Error("nil poll is not ended")
	}
	if PollEnded(&discordgo.Poll{}) {
		t.Error("poll without results is not ended")
	}
	if PollEnded(&discordgo.Poll{Results: &discordgo.PollResults{}}) {
		t.Error("poll with unfinalized results is not ended")
	}
	if !PollEnded(&discordgo.Poll{Results: &discordgo.PollResults{Finalized: true}}) {
		t.Error("poll with finalized results is ended")
	}
}

func TestPollResultTarget(t *testing.T) {
	ref := &discordgo.MessageReference{MessageID: "1234"}
	if id, ok := PollResultTarget(&discordgo.Message{Type: 46, MessageReference: ref}); !ok || id != "1234" {
		t.Errorf("type 46 message should point at its poll, got %q %v", id, ok)
	}
	if _, ok := PollResultTarget(&discordgo.Message{Type: discordgo.MessageTypeDefault, MessageReference: ref}); ok {
		t.Error("only type 46 is a poll result")
	}
	if _, ok := PollResultTarget(&discordgo.Message{Type: 46}); ok {
		t.Error("a poll result without a reference has no target")
	}
	if _, ok := PollResultTarget(nil); ok {
		t.Error("nil message")
	}
}

func TestPollEndToMatrix(t *testing.T) {
	poll := testDiscordPoll()
	poll.Results = &discordgo.PollResults{Finalized: true, AnswerCounts: []*discordgo.PollAnswerCount{
		{ID: 1, Count: 2}, {ID: 2, Count: 5}, {ID: 3, Count: 5},
	}}
	content, extra := PollEndToMatrix("$poll:example.com", poll)
	if content.RelatesTo.Type != event.RelReference || content.RelatesTo.EventID != "$poll:example.com" {
		t.Errorf("end must reference the poll: %+v", content.RelatesTo)
	}
	if _, ok := extra["org.matrix.msc3381.poll.end"]; !ok {
		t.Error("poll end field missing")
	}
	if content.Body != "The poll has ended. Top answers: 🍐 Pear <b>, Plum" {
		t.Errorf("unexpected body %q", content.Body)
	}
	content, _ = PollEndToMatrix("$poll:example.com", nil)
	if content.Body != "The poll has ended." {
		t.Errorf("unexpected body without results: %q", content.Body)
	}
}

func TestPollResponseToMatrix(t *testing.T) {
	content, extra := PollResponseToMatrix("$poll:example.com", nil)
	if content.RelatesTo.EventID != "$poll:example.com" {
		t.Errorf("wrong target %v", content.RelatesTo)
	}
	answers := extra["org.matrix.msc3381.poll.response"].(map[string]any)["answers"].([]string)
	if answers == nil || len(answers) != 0 {
		t.Errorf("a retracted vote is an empty list, not null: %#v", answers)
	}
}

func TestPollFromMatrix(t *testing.T) {
	poll, err := PollFromMatrix(testMatrixPoll(2), 24)
	if err != nil {
		t.Fatal(err)
	}
	if poll.Question.Text != "Lunch spot?" {
		t.Errorf("wrong question %q", poll.Question.Text)
	}
	if len(poll.Answers) != 3 || poll.Answers[1].Media.Text != "Tacos" {
		t.Errorf("wrong answers %+v", poll.Answers)
	}
	for _, answer := range poll.Answers {
		if answer.AnswerID != 0 {
			t.Error("answer IDs must not be set on creation")
		}
	}
	if !poll.AllowMultiselect {
		t.Error("max_selections 2 should allow multiselect")
	}
	if poll.Duration != 24 {
		t.Errorf("wrong duration %d", poll.Duration)
	}
	if poll.LayoutType != discordgo.PollLayoutTypeDefault {
		t.Error("layout type should be the default one")
	}
	body, err := json.Marshal(poll)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	if parsed["allow_multiselect"] != true || parsed["duration"] != float64(24) {
		t.Errorf("unexpected payload %s", body)
	}
	if _, has := parsed["results"]; has {
		t.Errorf("creation payload must not carry results: %s", body)
	}

	single, err := PollFromMatrix(testMatrixPoll(1), 24)
	if err != nil {
		t.Fatal(err)
	}
	if single.AllowMultiselect {
		t.Error("max_selections 1 is a single choice poll")
	}
}

func TestPollFromMatrixRejects(t *testing.T) {
	mutate := func(fn func(c *event.PollStartEventContent)) *event.PollStartEventContent {
		c := testMatrixPoll(1)
		fn(c)
		return c
	}
	cases := map[string]*event.PollStartEventContent{
		"no question": mutate(func(c *event.PollStartEventContent) { c.PollStart.Question.Text = "  " }),
		"long question": mutate(func(c *event.PollStartEventContent) {
			c.PollStart.Question.Text = strings.Repeat("q", MaxPollQuestionLength+1)
		}),
		"one answer": mutate(func(c *event.PollStartEventContent) { c.PollStart.Answers = c.PollStart.Answers[:1] }),
		"many answers": mutate(func(c *event.PollStartEventContent) {
			for i := 0; i < MaxPollAnswers; i++ {
				c.PollStart.Answers = append(c.PollStart.Answers, event.PollOption{ID: strings.Repeat("x", i+1), MSC1767Message: event.MSC1767Message{Text: "more"}})
			}
		}),
		"empty answer": mutate(func(c *event.PollStartEventContent) { c.PollStart.Answers[0].Text = "" }),
		"long answer": mutate(func(c *event.PollStartEventContent) {
			c.PollStart.Answers[0].Text = strings.Repeat("a", MaxPollAnswerLength+1)
		}),
		"duplicate on id": mutate(func(c *event.PollStartEventContent) { c.PollStart.Answers[1].ID = c.PollStart.Answers[0].ID }),
	}
	for name, content := range cases {
		if _, err := PollFromMatrix(content, 24); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := PollFromMatrix(testMatrixPoll(1), 0); err == nil {
		t.Error("zero duration should be rejected")
	}
	if _, err := PollFromMatrix(testMatrixPoll(1), MaxPollDurationHours+1); err == nil {
		t.Error("too long duration should be rejected")
	}
}

func TestPollDurationFromContent(t *testing.T) {
	if got := PollDurationFromContent(nil); got != DefaultPollDurationHours {
		t.Errorf("default should be %d, got %d", DefaultPollDurationHours, got)
	}
	if got := PollDurationFromContent(map[string]any{PollDurationHoursKey: float64(72)}); got != 72 {
		t.Errorf("explicit duration ignored, got %d", got)
	}
	if got := PollDurationFromContent(map[string]any{PollDurationHoursKey: float64(0)}); got != DefaultPollDurationHours {
		t.Errorf("invalid duration should fall back, got %d", got)
	}
	if got := PollDurationFromContent(map[string]any{PollDurationHoursKey: "soon"}); got != DefaultPollDurationHours {
		t.Errorf("non-number duration should fall back, got %d", got)
	}
}

func TestPollAnswerMap(t *testing.T) {
	content := testMatrixPoll(1)
	got := PollAnswerMap(content, nil)
	if got["a-1"] != 1 || got["a-2"] != 2 || got["a-3"] != 3 {
		t.Errorf("without a reply, answers are numbered from 1: %v", got)
	}
	sent := &discordgo.Poll{Answers: []discordgo.PollAnswer{{AnswerID: 5}, {AnswerID: 6}, {AnswerID: 9}}}
	got = PollAnswerMap(content, sent)
	if got["a-1"] != 5 || got["a-2"] != 6 || got["a-3"] != 9 {
		t.Errorf("answers should take the IDs Discord assigned: %v", got)
	}
}

func TestToMatrixPollMessage(t *testing.T) {
	mc := &MessageConverter{}
	msg := &discordgo.Message{
		ID:        "100",
		ChannelID: "200",
		Author:    &discordgo.User{ID: "300"},
		Poll:      testDiscordPoll(),
	}
	converted := mc.ToMatrix(context.Background(), nil, nil, nil, nil, msg, nil)
	if len(converted.Parts) != 1 {
		t.Fatalf("a poll without text is one part, got %d", len(converted.Parts))
	}
	if converted.Parts[0].Type != event.EventUnstablePollStart || converted.Parts[0].ID != PollPartID {
		t.Errorf("part is not a poll start: %v %q", converted.Parts[0].Type, converted.Parts[0].ID)
	}
}

func TestToMatrixDropsPollResultMessage(t *testing.T) {
	mc := &MessageConverter{}
	msg := &discordgo.Message{
		ID:               "101",
		ChannelID:        "200",
		Type:             MessageTypePollResult,
		Author:           &discordgo.User{ID: "300"},
		MessageReference: &discordgo.MessageReference{MessageID: "100"},
		Embeds:           []*discordgo.MessageEmbed{{Type: "poll_result", Title: "Best fruit?"}},
	}
	converted := mc.ToMatrix(context.Background(), nil, nil, nil, nil, msg, nil)
	if len(converted.Parts) != 0 {
		t.Errorf("the poll result system message must not be bridged as a message, got %d parts", len(converted.Parts))
	}
}
