package connector

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

var _ bridgev2.UserBlockingNetworkAPI = (*DiscordClient)(nil)

// ghostBlocker mirrors a block made on Discord into Matrix. *bridgev2.UserLogin is one.
type ghostBlocker interface {
	SetGhostBlocked(ctx context.Context, ghostID networkid.UserID, blocked bool) error
}

// blockTracker remembers who the user has blocked on Discord (relationships of type 2), so that the
// gateway's relationship events can be turned into blocks and unblocks.
type blockTracker struct {
	lock    sync.Mutex
	blocked map[string]bool
	synced  bool
}

// sync replaces the set with the blocked people of a full relationship list (READY) and returns who was
// blocked or unblocked compared to before. The first list only blocks: what was unblocked while the bridge
// was offline isn't known.
func (t *blockTracker) sync(rels []*discordgo.Relationship) (blocked, unblocked []string) {
	t.lock.Lock()
	defer t.lock.Unlock()
	now := make(map[string]bool)
	for _, rel := range rels {
		if rel != nil && rel.Type == discordgo.RelationshipBlocked {
			now[rel.ID] = true
		}
	}
	for id := range now {
		if !t.blocked[id] {
			blocked = append(blocked, id)
		}
	}
	for id := range t.blocked {
		if !now[id] {
			unblocked = append(unblocked, id)
		}
	}
	t.blocked = now
	t.synced = true
	slices.Sort(blocked)
	slices.Sort(unblocked)
	return
}

// update applies a RELATIONSHIP_ADD or RELATIONSHIP_UPDATE. changed is false when it says nothing new about
// blocking.
func (t *blockTracker) update(rel *discordgo.Relationship) (id string, blocked, changed bool) {
	if rel == nil {
		return "", false, false
	}
	t.lock.Lock()
	defer t.lock.Unlock()
	isBlocked := rel.Type == discordgo.RelationshipBlocked
	if t.blocked[rel.ID] == isBlocked {
		return rel.ID, isBlocked, false
	}
	if t.blocked == nil {
		t.blocked = make(map[string]bool)
	}
	if isBlocked {
		t.blocked[rel.ID] = true
	} else {
		delete(t.blocked, rel.ID)
	}
	return rel.ID, isBlocked, true
}

// remove applies a RELATIONSHIP_REMOVE, which unblocks the person if they were blocked.
func (t *blockTracker) remove(id string) (changed bool) {
	t.lock.Lock()
	defer t.lock.Unlock()
	if !t.blocked[id] {
		return false
	}
	delete(t.blocked, id)
	return true
}

// applyBlockChanges ignores the ghosts of the people the user blocked on Discord and stops ignoring the ones
// they unblocked. It returns false if any change failed.
func applyBlockChanges(ctx context.Context, blocker ghostBlocker, log *zerolog.Logger, blocked, unblocked []string) bool {
	ok := true
	apply := func(ids []string, isBlocked bool) {
		for _, id := range ids {
			if err := blocker.SetGhostBlocked(ctx, discordid.MakeUserID(id), isBlocked); err != nil {
				log.Err(err).Str("user_id", id).Bool("blocked", isBlocked).Msg("Failed to mirror Discord block into the ignore list")
				ok = false
			}
		}
	}
	apply(blocked, true)
	apply(unblocked, false)
	return ok
}

func (d *DiscordClient) mirrorReadyBlocks(ctx context.Context, rels []*discordgo.Relationship) {
	blocked, unblocked := d.blocks.sync(rels)
	applyBlockChanges(ctx, d.UserLogin, zerolog.Ctx(ctx), blocked, unblocked)
}

func (d *DiscordClient) mirrorRelationshipBlock(ctx context.Context, rel *discordgo.Relationship) {
	if id, blocked, changed := d.blocks.update(rel); changed {
		applyBlockChanges(ctx, d.UserLogin, zerolog.Ctx(ctx), blockedIf(id, blocked), blockedIf(id, !blocked))
	}
}

func (d *DiscordClient) mirrorRelationshipRemoved(ctx context.Context, userID string) {
	if d.blocks.remove(userID) {
		applyBlockChanges(ctx, d.UserLogin, zerolog.Ctx(ctx), nil, []string{userID})
	}
}

func blockedIf(id string, cond bool) []string {
	if cond {
		return []string{id}
	}
	return nil
}

// HandleMatrixBlock blocks or unblocks the person a ghost stands for on Discord, where the user ignoring
// or un-ignoring the ghost on Matrix is what triggers it.
func (d *DiscordClient) HandleMatrixBlock(ctx context.Context, ghost *bridgev2.Ghost, blocked bool) error {
	userID := discordid.ParseUserID(ghost.ID)
	if userID == "" {
		return fmt.Errorf("can't block %s", ghost.ID)
	} else if userID == d.ownUserID() {
		return fmt.Errorf("can't block yourself")
	}
	rel := d.relationshipWithUserID(userID)
	isBlocked := rel != nil && rel.Type == discordgo.RelationshipBlocked
	if blocked == isBlocked {
		return nil
	}
	if blocked {
		return d.Session.RelationshipUserBlock(userID)
	}
	// Unblocking removes the relationship; it has to be the block that goes, never a friendship.
	return d.Session.RelationshipDelete(userID)
}
