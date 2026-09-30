// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 Maximilian Gaedig
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package msgconv

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	// PollPartID is the part ID of the message part that carries a poll.
	PollPartID = "poll"

	// MessageTypePollResult is the type of the system message Discord sends when a poll ends.
	MessageTypePollResult discordgo.MessageType = 46

	// DefaultPollDurationHours is how long a poll started from Matrix runs
	// when the event doesn't say otherwise.
	DefaultPollDurationHours = 24
	// PollDurationHoursKey is the key in the content of a Matrix poll start
	// event that sets the duration of the Discord poll, in hours.
	PollDurationHoursKey = "fi.mau.discord.poll_duration_hours"

	// Discord's limits for polls.
	MinPollAnswers        = 2
	MaxPollAnswers        = 10
	MaxPollQuestionLength = 300
	MaxPollAnswerLength   = 55
	MaxPollDurationHours  = 32 * 24

	pollKindDisclosed = "org.matrix.msc3381.poll.disclosed"
)

// PollMaxSelections is the number of answers one vote may pick, as a Matrix poll states it.
func PollMaxSelections(poll *discordgo.Poll) int {
	if poll.AllowMultiselect {
		return max(len(poll.Answers), 1)
	}
	return 1
}

// PollEnded reports whether Discord says that a poll is closed, which it does
// in a message update that has finalized results.
func PollEnded(poll *discordgo.Poll) bool {
	return poll != nil && poll.Results != nil && poll.Results.Finalized
}

// PollResultTarget is the ID of the poll message that a "poll result" system
// message is about. ok is false for any other message.
func PollResultTarget(msg *discordgo.Message) (pollMessageID string, ok bool) {
	if msg == nil || msg.Type != MessageTypePollResult {
		return "", false
	}
	if msg.MessageReference == nil || msg.MessageReference.MessageID == "" {
		return "", false
	}
	return msg.MessageReference.MessageID, true
}

// pollMediaText is the text of a question or answer, with its emoji in front
// if it's a Unicode one.
func pollMediaText(media *discordgo.PollMedia) string {
	if media == nil {
		return ""
	}
	text := strings.TrimSpace(media.Text)
	if media.Emoji != nil && media.Emoji.ID == "" && media.Emoji.Name != "" {
		if text == "" {
			return media.Emoji.Name
		}
		return media.Emoji.Name + " " + text
	}
	return text
}

// PollToMatrix converts a Discord poll to the content of an
// org.matrix.msc3381.poll.start event. The text fallback is in the plain body
// and in the extensible event fields, for clients without poll support.
func PollToMatrix(poll *discordgo.Poll) (*event.MessageEventContent, map[string]any) {
	question := pollMediaText(&poll.Question)
	ended := PollEnded(poll)
	answers := make([]map[string]any, 0, len(poll.Answers))
	textAnswers := make([]string, 0, len(poll.Answers))
	var htmlAnswers strings.Builder
	for i, answer := range poll.Answers {
		text := pollMediaText(answer.Media)
		answers = append(answers, map[string]any{
			"id":                      fmt.Sprint(answer.AnswerID),
			"org.matrix.msc1767.text": text,
		})
		textAnswers = append(textAnswers, fmt.Sprintf("%d. %s", i+1, text))
		htmlAnswers.WriteString("<li>" + event.TextToHTML(text) + "</li>")
	}

	body := fmt.Sprintf("Poll: %s\n\n%s", question, strings.Join(textAnswers, "\n"))
	formattedBody := fmt.Sprintf("<p><strong>Poll</strong>: %s</p><ol>%s</ol>", event.TextToHTML(question), htmlAnswers.String())
	if ended {
		body += "\n\n(This poll has ended.)"
		formattedBody += "<p>(This poll has ended.)</p>"
	}

	content := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          body,
		Format:        event.FormatHTML,
		FormattedBody: formattedBody,
	}
	discordInfo := map[string]any{
		"multiselect": poll.AllowMultiselect,
		"ended":       ended,
	}
	if poll.Expiry != nil {
		discordInfo["expiry"] = poll.Expiry.UnixMilli()
	}
	extra := map[string]any{
		"org.matrix.msc1767.message": []map[string]any{
			{"mimetype": "text/html", "body": formattedBody},
			{"mimetype": "text/plain", "body": body},
		},
		"org.matrix.msc3381.poll.start": map[string]any{
			"kind":           pollKindDisclosed,
			"max_selections": PollMaxSelections(poll),
			"question": map[string]any{
				"org.matrix.msc1767.text": question,
			},
			"answers": answers,
		},
		"fi.mau.discord.poll": discordInfo,
	}
	return content, extra
}

// PollTopAnswers is the text of the answers with the most votes. It's empty
// when nobody voted or the poll has no counts.
func PollTopAnswers(poll *discordgo.Poll) []string {
	if poll == nil || poll.Results == nil {
		return nil
	}
	best := 0
	var topIDs []int
	for _, count := range poll.Results.AnswerCounts {
		if count.Count == 0 || count.Count < best {
			continue
		}
		if count.Count > best {
			best = count.Count
			topIDs = topIDs[:0]
		}
		topIDs = append(topIDs, count.ID)
	}
	var top []string
	for _, answer := range poll.Answers {
		if slices.Contains(topIDs, answer.AnswerID) {
			top = append(top, pollMediaText(answer.Media))
		}
	}
	return top
}

// PollEndToMatrix is the content of the org.matrix.msc3381.poll.end event for
// a poll that ended on Discord. The poll may be nil when only the fact that it
// ended is known.
func PollEndToMatrix(pollEventID id.EventID, poll *discordgo.Poll) (*event.MessageEventContent, map[string]any) {
	text := "The poll has ended."
	switch top := PollTopAnswers(poll); len(top) {
	case 0:
	case 1:
		text += " Top answer: " + top[0]
	default:
		text += " Top answers: " + strings.Join(top, ", ")
	}
	return &event.MessageEventContent{
		MsgType:   event.MsgText,
		Body:      text,
		RelatesTo: &event.RelatesTo{Type: event.RelReference, EventID: pollEventID},
	}, map[string]any{
		"org.matrix.msc3381.poll.end": map[string]any{},
		"org.matrix.msc1767.text":     text,
	}
}

// PollResponseToMatrix is the content of an org.matrix.msc3381.poll.response
// event. An empty list retracts the vote.
func PollResponseToMatrix(pollEventID id.EventID, answerIDs []string) (*event.MessageEventContent, map[string]any) {
	if answerIDs == nil {
		answerIDs = []string{}
	}
	return &event.MessageEventContent{
		RelatesTo: &event.RelatesTo{Type: event.RelReference, EventID: pollEventID},
	}, map[string]any{
		"org.matrix.msc3381.poll.response": map[string]any{"answers": answerIDs},
	}
}

// PollDurationFromContent reads the duration of a Matrix poll in hours from
// the raw event content, and uses the default if it's not there.
func PollDurationFromContent(raw map[string]any) int {
	switch v := raw[PollDurationHoursKey].(type) {
	case float64:
		if v >= 1 && v <= MaxPollDurationHours {
			return int(v)
		}
	case int:
		if v >= 1 && v <= MaxPollDurationHours {
			return v
		}
	}
	return DefaultPollDurationHours
}

// PollFromMatrix converts a Matrix poll start event to the poll object that
// Discord wants in a new message. Discord assigns the answer IDs itself.
func PollFromMatrix(content *event.PollStartEventContent, durationHours int) (*discordgo.Poll, error) {
	start := &content.PollStart
	question := strings.TrimSpace(start.Question.GetText())
	switch {
	case question == "":
		return nil, errors.New("the poll has no question")
	case utf8.RuneCountInString(question) > MaxPollQuestionLength:
		return nil, fmt.Errorf("the poll question is longer than Discord's limit of %d characters", MaxPollQuestionLength)
	case len(start.Answers) < MinPollAnswers:
		return nil, fmt.Errorf("polls on Discord need at least %d answers", MinPollAnswers)
	case len(start.Answers) > MaxPollAnswers:
		return nil, fmt.Errorf("polls on Discord can have at most %d answers", MaxPollAnswers)
	case durationHours < 1 || durationHours > MaxPollDurationHours:
		return nil, fmt.Errorf("the poll duration must be between 1 and %d hours", MaxPollDurationHours)
	}
	seen := make(map[string]struct{}, len(start.Answers))
	answers := make([]discordgo.PollAnswer, len(start.Answers))
	for i, answer := range start.Answers {
		text := strings.TrimSpace(answer.GetText())
		if text == "" {
			return nil, fmt.Errorf("answer %d of the poll is empty", i+1)
		} else if utf8.RuneCountInString(text) > MaxPollAnswerLength {
			return nil, fmt.Errorf("answer %d of the poll is longer than Discord's limit of %d characters", i+1, MaxPollAnswerLength)
		} else if _, dup := seen[answer.ID]; dup {
			return nil, fmt.Errorf("the poll has two answers with the ID %q", answer.ID)
		}
		seen[answer.ID] = struct{}{}
		answers[i] = discordgo.PollAnswer{Media: &discordgo.PollMedia{Text: text}}
	}
	return &discordgo.Poll{
		Question:         discordgo.PollMedia{Text: question},
		Answers:          answers,
		AllowMultiselect: start.MaxSelections > 1,
		LayoutType:       discordgo.PollLayoutTypeDefault,
		Duration:         durationHours,
	}, nil
}

// PollAnswerMap pairs the answers of a Matrix poll with the Discord answer IDs
// that Discord gave them, which it does in order, counting from 1. sent is the
// poll of the message Discord returned; without it the numbering is assumed.
func PollAnswerMap(content *event.PollStartEventContent, sent *discordgo.Poll) map[string]int {
	answers := make(map[string]int, len(content.PollStart.Answers))
	for i, answer := range content.PollStart.Answers {
		discordID := i + 1
		if sent != nil && i < len(sent.Answers) && sent.Answers[i].AnswerID != 0 {
			discordID = sent.Answers[i].AnswerID
		}
		answers[answer.ID] = discordID
	}
	return answers
}
