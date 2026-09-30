// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
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

package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

var (
	_ bridgev2.IdentifierResolvingNetworkAPI = (*DiscordClient)(nil)
	_ bridgev2.ContactListingNetworkAPI      = (*DiscordClient)(nil)
	_ bridgev2.UserSearchingNetworkAPI       = (*DiscordClient)(nil)
	_ bridgev2.GroupCreatingNetworkAPI       = (*DiscordClient)(nil)
)

const (
	// Discord lets a group DM hold 10 people including the creator.
	maxGroupDMRecipients = 9
	maxSearchResults     = 50
)

var (
	snowflakeRe = regexp.MustCompile(`^\d{15,25}$`)
	mentionRe   = regexp.MustCompile(`^<@!?(\d{15,25})>$`)
)

// parsedIdentifier is the result of [parseIdentifier]. Exactly one of ID and
// Username is set.
type parsedIdentifier struct {
	ID       string
	Username string
}

// parseIdentifier understands a raw user ID, a "<@id>" mention, or a username
// (optionally prefixed with "@", optionally with a legacy "#1234" suffix).
// Usernames are lowercased, since Discord usernames are case-insensitive.
func parseIdentifier(identifier string) (parsedIdentifier, bool) {
	identifier = strings.TrimSpace(identifier)
	if m := mentionRe.FindStringSubmatch(identifier); m != nil {
		return parsedIdentifier{ID: m[1]}, true
	}
	identifier = strings.TrimPrefix(identifier, "@")
	if identifier == "" {
		return parsedIdentifier{}, false
	}
	if snowflakeRe.MatchString(identifier) {
		return parsedIdentifier{ID: identifier}, true
	}
	return parsedIdentifier{Username: strings.ToLower(identifier)}, true
}

// userMatchesUsername checks whether the user's username (or username#disc)
// equals the given lowercase username.
func userMatchesUsername(u *discordgo.User, username string) bool {
	return strings.ToLower(u.Username) == username || strings.ToLower(u.String()) == username
}

// findUserByUsername looks for an exact username match, friends first.
func findUserByUsername(friends, known []*discordgo.User, username string) *discordgo.User {
	for _, list := range [][]*discordgo.User{friends, known} {
		for _, u := range list {
			if u != nil && userMatchesUsername(u, username) {
				return u
			}
		}
	}
	return nil
}

// friendUsers picks the users for whom the relationship is a friendship,
// sorted by display name for stable output. Users that aren't known are skipped.
func friendUsers(rels []*discordgo.Relationship, lookup func(id string) *discordgo.User) []*discordgo.User {
	var out []*discordgo.User
	for _, rel := range rels {
		if rel == nil || rel.Type != discordgo.RelationshipFriend {
			continue
		}
		if u := lookup(rel.ID); u != nil {
			out = append(out, u)
		}
	}
	sortUsers(out)
	return out
}

func sortUsers(users []*discordgo.User) {
	sort.SliceStable(users, func(i, j int) bool {
		a, b := strings.ToLower(users[i].DisplayName()), strings.ToLower(users[j].DisplayName())
		if a != b {
			return a < b
		}
		return users[i].ID < users[j].ID
	})
}

// searchUsers filters friends and then other known users by a
// case-insensitive substring of the username, display name or ID. Friends
// come first, each user appears once, and self is excluded.
func searchUsers(friends, known []*discordgo.User, query, selfID string) []*discordgo.User {
	query = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(query), "@")))
	if query == "" {
		return nil
	}
	seen := map[string]bool{selfID: true}
	var out []*discordgo.User
	collect := func(list []*discordgo.User) {
		var matched []*discordgo.User
		for _, u := range list {
			if u == nil || seen[u.ID] {
				continue
			}
			if strings.Contains(strings.ToLower(u.Username), query) ||
				strings.Contains(strings.ToLower(u.GlobalName), query) ||
				strings.Contains(strings.ToLower(u.String()), query) ||
				u.ID == query {
				seen[u.ID] = true
				matched = append(matched, u)
			}
		}
		sortUsers(matched)
		out = append(out, matched...)
	}
	collect(friends)
	collect(known)
	if len(out) > maxSearchResults {
		out = out[:maxSearchResults]
	}
	return out
}

// groupDMRequest is the body of `POST /users/@me/channels` for a group DM.
type groupDMRequest struct {
	Recipients []string `json:"recipients"`
}

// newGroupDMRequest de-duplicates the recipients, drops self and empty IDs,
// and validates the count (at least two, since one recipient is a plain DM).
func newGroupDMRequest(participants []networkid.UserID, selfID string) (*groupDMRequest, error) {
	req := &groupDMRequest{Recipients: []string{}}
	seen := map[string]bool{selfID: true, "": true}
	for _, p := range participants {
		id := discordid.ParseUserID(p)
		if seen[id] {
			continue
		}
		seen[id] = true
		req.Recipients = append(req.Recipients, id)
	}
	if len(req.Recipients) < 2 {
		return nil, fmt.Errorf("a group DM needs at least two other participants")
	}
	if len(req.Recipients) > maxGroupDMRecipients {
		return nil, fmt.Errorf("a group DM can have at most %d other participants", maxGroupDMRecipients)
	}
	return req, nil
}

func (d *DiscordClient) friendList() []*discordgo.User {
	if d.Session == nil || d.Session.State == nil {
		return nil
	}
	d.relationshipLock.RLock()
	rels := make([]*discordgo.Relationship, 0, len(d.relationships))
	for _, rel := range d.relationships {
		rels = append(rels, rel)
	}
	d.relationshipLock.RUnlock()

	return friendUsers(rels, func(id string) *discordgo.User {
		return d.userCache.Resolve(context.Background(), id)
	})
}

func (d *DiscordClient) existingDMChannel(userID string) *discordgo.Channel {
	if d.Session == nil || d.Session.State == nil {
		return nil
	}
	d.Session.State.RLock()
	defer d.Session.State.RUnlock()
	for _, ch := range d.Session.State.PrivateChannels {
		if id := dmChannelRecipientID(ch); id != nil && *id == userID {
			return ch
		}
	}
	return nil
}

func (d *DiscordClient) chatResponseForChannel(ctx context.Context, ch *discordgo.Channel) (*bridgev2.CreateChatResponse, error) {
	if d.Session.State != nil {
		if err := d.Session.State.ChannelAdd(ch); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to add new channel to state")
		}
	}
	info, err := d.getChannelChatInfo(ctx, ch)
	if err != nil {
		return nil, fmt.Errorf("failed to get chat info: %w", err)
	}
	return &bridgev2.CreateChatResponse{
		PortalKey:  d.portalKeyForChannel(ch),
		PortalInfo: info,
	}, nil
}

func (d *DiscordClient) resolveUserResponse(ctx context.Context, user *discordgo.User, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	userID := discordid.MakeUserID(user.ID)
	ghost, err := d.connector.Bridge.GetGhostByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ghost: %w", err)
	}
	resp := &bridgev2.ResolveIdentifierResponse{
		Ghost:    ghost,
		UserID:   userID,
		UserInfo: d.getUserInfo(ctx, user),
		Context:  "@" + user.String(),
	}
	if createChat {
		ch := d.existingDMChannel(user.ID)
		if ch == nil {
			ch, err = d.Session.UserChannelCreate(user.ID)
			if err != nil {
				return nil, fmt.Errorf("failed to create DM: %w", err)
			}
		}
		resp.Chat, err = d.chatResponseForChannel(ctx, ch)
		if err != nil {
			return nil, err
		}
	} else if ch := d.existingDMChannel(user.ID); ch != nil {
		resp.Chat = &bridgev2.CreateChatResponse{PortalKey: d.portalKeyForChannel(ch)}
	}
	return resp, nil
}

func (d *DiscordClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	if d.Session == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	parsed, ok := parseIdentifier(identifier)
	if !ok {
		return nil, fmt.Errorf("empty identifier")
	}
	var user *discordgo.User
	if parsed.ID != "" {
		user = d.userCache.Resolve(ctx, parsed.ID)
	} else {
		// Discord has no username lookup for arbitrary users, so only friends
		// and users seen in this session can be found by name.
		user = findUserByUsername(d.friendList(), d.userCache.Snapshot(), parsed.Username)
	}
	if user == nil {
		return nil, nil
	}
	return d.resolveUserResponse(ctx, user, createChat)
}

func (d *DiscordClient) GetContactList(ctx context.Context) ([]*bridgev2.ResolveIdentifierResponse, error) {
	if d.Session == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	return d.usersToResponses(ctx, d.friendList())
}

func (d *DiscordClient) SearchUsers(ctx context.Context, query string) ([]*bridgev2.ResolveIdentifierResponse, error) {
	if d.Session == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	selfID := discordid.ParseUserID(discordid.UserLoginIDToUserID(d.UserLogin.ID))
	return d.usersToResponses(ctx, searchUsers(d.friendList(), d.userCache.Snapshot(), query, selfID))
}

func (d *DiscordClient) usersToResponses(ctx context.Context, users []*discordgo.User) ([]*bridgev2.ResolveIdentifierResponse, error) {
	out := make([]*bridgev2.ResolveIdentifierResponse, 0, len(users))
	for _, u := range users {
		resp, err := d.resolveUserResponse(ctx, u, false)
		if err != nil {
			return nil, err
		}
		out = append(out, resp)
	}
	return out, nil
}

func (d *DiscordClient) CreateGroup(ctx context.Context, params *bridgev2.GroupCreateParams) (*bridgev2.CreateChatResponse, error) {
	if d.Session == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	selfID := discordid.ParseUserID(discordid.UserLoginIDToUserID(d.UserLogin.ID))
	req, err := newGroupDMRequest(params.Participants, selfID)
	if err != nil {
		return nil, err
	}
	// The library only has a helper for the single-recipient form, so send the
	// same request the web client does: POST /users/@me/channels {recipients}.
	body, err := d.Session.RequestWithBucketID("POST", discordgo.EndpointUserChannels("@me"), req, discordgo.EndpointUserChannels(""))
	if err != nil {
		return nil, fmt.Errorf("failed to create group DM: %w", err)
	}
	var ch discordgo.Channel
	if err = json.Unmarshal(body, &ch); err != nil {
		return nil, fmt.Errorf("failed to parse group DM response: %w", err)
	}
	if params.Name != nil && params.Name.Name != "" {
		if edited, err := d.Session.ChannelEdit(ch.ID, &discordgo.ChannelEdit{Name: params.Name.Name}); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to name new group DM")
		} else if edited != nil {
			ch = *edited
		}
	}
	return d.chatResponseForChannel(ctx, &ch)
}
