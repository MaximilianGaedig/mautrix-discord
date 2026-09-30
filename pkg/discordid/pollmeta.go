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

package discordid

import (
	"fmt"
	"slices"
	"strconv"
)

// MessageMetadata is what the bridge remembers about a bridged Discord message.
type MessageMetadata struct {
	// Poll is set on the message part that carries a poll.
	Poll *PollMetadata `json:"poll,omitempty"`
}

// PollMetadata is the bookkeeping for one bridged poll.
//
// Matrix poll responses carry the whole selection of a voter, while Discord
// only tells us that one answer was added or removed. Votes keeps each voter's
// current selection so that the full set can be sent on every change.
type PollMetadata struct {
	// MaxSelections is how many answers one vote may pick.
	MaxSelections int `json:"max_selections,omitempty"`
	// Ended is set once the poll has been closed, from either side.
	Ended bool `json:"ended,omitempty"`
	// Seeded says whether Votes has been filled in from Discord's voter lists.
	// Votes cast before the bridge saw the poll aren't in any gateway event.
	Seeded bool `json:"seeded,omitempty"`
	// AnswerIDs is every Discord answer ID of the poll, used to look up voters.
	AnswerIDs []int `json:"answer_ids,omitempty"`
	// Answers maps Matrix answer IDs to Discord answer IDs for polls that were
	// started from Matrix. Polls that started on Discord use the Discord
	// answer ID as the Matrix answer ID, and leave this empty.
	Answers map[string]int `json:"answers,omitempty"`
	// Votes maps a Discord user ID to the sorted answer IDs the user has picked.
	Votes map[string][]int `json:"votes,omitempty"`
}

// MatrixAnswerID is the ID of the Matrix poll answer that stands for a Discord answer.
func (pm *PollMetadata) MatrixAnswerID(discordAnswerID int) string {
	for matrixID, id := range pm.Answers {
		if id == discordAnswerID {
			return matrixID
		}
	}
	return strconv.Itoa(discordAnswerID)
}

// DiscordAnswerID is the Discord answer that a Matrix poll answer stands for.
func (pm *PollMetadata) DiscordAnswerID(matrixAnswerID string) (int, error) {
	if len(pm.Answers) > 0 {
		id, ok := pm.Answers[matrixAnswerID]
		if !ok {
			return 0, fmt.Errorf("unknown poll answer %q", matrixAnswerID)
		}
		return id, nil
	}
	id, err := strconv.Atoi(matrixAnswerID)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("unknown poll answer %q", matrixAnswerID)
	}
	return id, nil
}

// DiscordAnswerIDs turns the answers of a Matrix poll response into sorted
// Discord answer IDs, without duplicates. An empty result retracts the vote.
func (pm *PollMetadata) DiscordAnswerIDs(matrixAnswerIDs []string) ([]int, error) {
	ids := make([]int, 0, len(matrixAnswerIDs))
	for _, matrixID := range matrixAnswerIDs {
		id, err := pm.DiscordAnswerID(matrixID)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	if pm.MaxSelections > 0 && len(ids) > pm.MaxSelections {
		return nil, fmt.Errorf("the poll allows at most %d answers, but the vote has %d", pm.MaxSelections, len(ids))
	}
	return ids, nil
}

// MatrixAnswerIDs is the Matrix form of a set of Discord answer IDs.
func (pm *PollMetadata) MatrixAnswerIDs(discordAnswerIDs []int) []string {
	ids := make([]string, len(discordAnswerIDs))
	for i, id := range discordAnswerIDs {
		ids[i] = pm.MatrixAnswerID(id)
	}
	return ids
}

// VotesOf is the answers a user has currently picked.
func (pm *PollMetadata) VotesOf(userID string) []int {
	return slices.Clone(pm.Votes[userID])
}

// SetVotes replaces the selection of a user, and reports whether that changed anything.
func (pm *PollMetadata) SetVotes(userID string, answerIDs []int) (changed bool) {
	next := slices.Clone(answerIDs)
	slices.Sort(next)
	next = slices.Compact(next)
	if slices.Equal(pm.Votes[userID], next) {
		return false
	}
	if len(next) == 0 {
		delete(pm.Votes, userID)
		return true
	}
	if pm.Votes == nil {
		pm.Votes = make(map[string][]int)
	}
	pm.Votes[userID] = next
	return true
}

// ApplyVote applies one add or remove from Discord to the selection of a user.
// It returns the selection after the change, and whether there was any change:
// an add of an answer that is already picked, or a remove of one that isn't,
// changes nothing. That's what makes the echo of a vote cast from Matrix a no-op.
func (pm *PollMetadata) ApplyVote(userID string, answerID int, add bool) (selection []int, changed bool) {
	selection = pm.VotesOf(userID)
	has := slices.Contains(selection, answerID)
	switch {
	case add && !has:
		selection = append(selection, answerID)
	case !add && has:
		selection = slices.DeleteFunc(selection, func(id int) bool { return id == answerID })
	default:
		return selection, false
	}
	pm.SetVotes(userID, selection)
	return pm.VotesOf(userID), true
}
