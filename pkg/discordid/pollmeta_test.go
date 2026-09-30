package discordid

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestApplyVote(t *testing.T) {
	pm := &PollMetadata{}

	sel, changed := pm.ApplyVote("u1", 2, true)
	if !changed || !slices.Equal(sel, []int{2}) {
		t.Fatalf("first vote: got %v %v", sel, changed)
	}
	sel, changed = pm.ApplyVote("u1", 1, true)
	if !changed || !slices.Equal(sel, []int{1, 2}) {
		t.Fatalf("second answer should extend the set, sorted: got %v %v", sel, changed)
	}
	if _, changed = pm.ApplyVote("u1", 1, true); changed {
		t.Error("adding an answer that is already picked changes nothing")
	}
	sel, changed = pm.ApplyVote("u2", 3, true)
	if !changed || !slices.Equal(sel, []int{3}) {
		t.Fatalf("voters are independent: got %v %v", sel, changed)
	}
	if !slices.Equal(pm.VotesOf("u1"), []int{1, 2}) {
		t.Errorf("u2's vote leaked into u1: %v", pm.VotesOf("u1"))
	}

	// Changing a vote is a remove and an add, and every step reports the whole set.
	sel, changed = pm.ApplyVote("u1", 2, false)
	if !changed || !slices.Equal(sel, []int{1}) {
		t.Fatalf("remove: got %v %v", sel, changed)
	}
	sel, changed = pm.ApplyVote("u1", 3, true)
	if !changed || !slices.Equal(sel, []int{1, 3}) {
		t.Fatalf("change: got %v %v", sel, changed)
	}
	if _, changed = pm.ApplyVote("u1", 2, false); changed {
		t.Error("removing an answer that isn't picked changes nothing")
	}

	// Removing the last answer retracts the vote: the empty set, and no leftover entry.
	pm.ApplyVote("u1", 1, false)
	sel, changed = pm.ApplyVote("u1", 3, false)
	if !changed || len(sel) != 0 {
		t.Fatalf("retract: got %v %v", sel, changed)
	}
	if _, has := pm.Votes["u1"]; has {
		t.Error("a retracted vote should not leave an empty entry")
	}
	if !slices.Equal(pm.VotesOf("u2"), []int{3}) {
		t.Errorf("u2 lost its vote: %v", pm.VotesOf("u2"))
	}
}

func TestSetVotes(t *testing.T) {
	pm := &PollMetadata{}
	if !pm.SetVotes("u1", []int{3, 1, 3}) {
		t.Error("first set should change")
	}
	if !slices.Equal(pm.VotesOf("u1"), []int{1, 3}) {
		t.Errorf("sets are sorted and deduplicated: %v", pm.VotesOf("u1"))
	}
	if pm.SetVotes("u1", []int{1, 3}) {
		t.Error("same set is not a change")
	}
	if pm.SetVotes("u9", nil) {
		t.Error("empty set for unknown voter is not a change")
	}
	if !pm.SetVotes("u1", nil) || len(pm.Votes) != 0 {
		t.Error("empty set clears the vote")
	}
}

func TestVotesSurviveJSON(t *testing.T) {
	pm := &MessageMetadata{Poll: &PollMetadata{MaxSelections: 2}}
	pm.Poll.ApplyVote("u1", 1, true)
	pm.Poll.ApplyVote("u1", 2, true)
	data, err := json.Marshal(pm)
	if err != nil {
		t.Fatal(err)
	}
	var back MessageMetadata
	if err = json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(back.Poll.VotesOf("u1"), []int{1, 2}) || back.Poll.MaxSelections != 2 {
		t.Errorf("bookkeeping lost in the database round trip: %s", data)
	}
	var empty MessageMetadata
	if err = json.Unmarshal([]byte(`{}`), &empty); err != nil || empty.Poll != nil {
		t.Errorf("rows from before polls must load without poll data: %v %+v", err, empty)
	}
}

func TestAnswerIDMapping(t *testing.T) {
	discordOrigin := &PollMetadata{MaxSelections: 2}
	if got := discordOrigin.MatrixAnswerID(4); got != "4" {
		t.Errorf("Discord polls use the answer ID: %q", got)
	}
	ids, err := discordOrigin.DiscordAnswerIDs([]string{"3", "1", "3"})
	if err != nil || !slices.Equal(ids, []int{1, 3}) {
		t.Errorf("got %v %v", ids, err)
	}
	if _, err = discordOrigin.DiscordAnswerIDs([]string{"1", "2", "3"}); err == nil {
		t.Error("more answers than the poll allows must fail")
	}
	if _, err = discordOrigin.DiscordAnswerIDs([]string{"pear"}); err == nil {
		t.Error("unknown answer must fail")
	}
	if ids, err = discordOrigin.DiscordAnswerIDs(nil); err != nil || len(ids) != 0 {
		t.Errorf("an empty response retracts the vote: %v %v", ids, err)
	}

	matrixOrigin := &PollMetadata{MaxSelections: 1, Answers: map[string]int{"a-1": 1, "a-2": 2}}
	if got := matrixOrigin.MatrixAnswerID(2); got != "a-2" {
		t.Errorf("Matrix polls map back to the Matrix answer ID: %q", got)
	}
	ids, err = matrixOrigin.DiscordAnswerIDs([]string{"a-2"})
	if err != nil || !slices.Equal(ids, []int{2}) {
		t.Errorf("got %v %v", ids, err)
	}
	if _, err = matrixOrigin.DiscordAnswerIDs([]string{"2"}); err == nil {
		t.Error("a Discord ID is not a Matrix answer ID on a Matrix-origin poll")
	}
	if got := matrixOrigin.MatrixAnswerIDs([]int{1, 2}); !slices.Equal(got, []string{"a-1", "a-2"}) {
		t.Errorf("got %v", got)
	}
}
