package connector

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

func rel(id string, typ discordgo.RelationshipType) *discordgo.Relationship {
	return &discordgo.Relationship{ID: id, Type: typ}
}

func TestBlockTrackerReadyList(t *testing.T) {
	var tr blockTracker
	blocked, unblocked := tr.sync([]*discordgo.Relationship{
		rel("1", discordgo.RelationshipBlocked), rel("2", discordgo.RelationshipFriend), rel("3", discordgo.RelationshipBlocked), nil,
	})
	if !slices.Equal(blocked, []string{"1", "3"}) || len(unblocked) != 0 {
		t.Fatalf("first list: %v %v", blocked, unblocked)
	}
	// A later READY (fresh session): 1 was unblocked meanwhile, 2 is newly blocked, 3 unchanged.
	blocked, unblocked = tr.sync([]*discordgo.Relationship{
		rel("2", discordgo.RelationshipBlocked), rel("3", discordgo.RelationshipBlocked),
	})
	if !slices.Equal(blocked, []string{"2"}) || !slices.Equal(unblocked, []string{"1"}) {
		t.Fatalf("second list: %v %v", blocked, unblocked)
	}
}

func TestBlockTrackerEvents(t *testing.T) {
	var tr blockTracker
	if _, _, changed := tr.update(rel("1", discordgo.RelationshipFriend)); changed {
		t.Fatal("a friend is not a block change")
	}
	if id, blocked, changed := tr.update(rel("1", discordgo.RelationshipBlocked)); !changed || !blocked || id != "1" {
		t.Fatal("block not detected")
	}
	if _, _, changed := tr.update(rel("1", discordgo.RelationshipBlocked)); changed {
		t.Fatal("repeated block must change nothing")
	}
	if !tr.remove("1") {
		t.Fatal("removing a blocked relationship unblocks")
	}
	if tr.remove("1") || tr.remove("2") {
		t.Fatal("removing someone not blocked changes nothing")
	}
	tr.update(rel("2", discordgo.RelationshipBlocked))
	if id, blocked, changed := tr.update(rel("2", discordgo.RelationshipFriend)); !changed || blocked || id != "2" {
		t.Fatal("block turning into another relationship is an unblock")
	}
}

type fakeBlocker struct {
	calls map[networkid.UserID]bool
	fail  networkid.UserID
}

func (f *fakeBlocker) SetGhostBlocked(_ context.Context, id networkid.UserID, blocked bool) error {
	if id == f.fail {
		return errors.New("boom")
	}
	f.calls[id] = blocked
	return nil
}

func TestApplyBlockChanges(t *testing.T) {
	f := &fakeBlocker{calls: map[networkid.UserID]bool{}, fail: discordid.MakeUserID("1")}
	log := zerolog.Nop()
	if applyBlockChanges(context.Background(), f, &log, []string{"1", "2"}, []string{"3"}) {
		t.Fatal("failure must be reported")
	}
	v, ok := f.calls[discordid.MakeUserID("3")]
	if !f.calls[discordid.MakeUserID("2")] || !ok || v {
		t.Fatalf("one failure must not stop the rest: %v", f.calls)
	}
}
