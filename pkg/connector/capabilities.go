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
	"fmt"

	"github.com/bwmarrin/discordgo"
	"go.mau.fi/util/ffmpeg"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/pkg/discordid"
	"go.mau.fi/mautrix-discord/pkg/msgconv"
)

var DiscordGeneralCaps = &bridgev2.NetworkGeneralCapabilities{
	// Aggressive ghost info updates let us refresh ghost profiles during
	// backfill and when handling incoming messages, edits, and reactions.
	// Otherwise, mautrix skips updating ghosts that already have a name and
	// avatar set[1].
	//
	// Also, generally, it is cheap for DiscordClient's GetUserInfo to be
	// called as it should mostly hit the user cache. Otherwise, it asks
	// Discord's REST API for the user, which the first-party client itself
	// does fairly liberally. Regardless, no Matrix calls occur if the profile
	// didn't actually change, as Mautrix diffs.
	//
	// [1]: https://github.com/mautrix/go/blob/2b8e6caf43bea108e7fccacfa793fe46069ce3a8/bridgev2/ghost.go#L295
	AggressiveUpdateInfo: true,

	Provisioning: bridgev2.ProvisioningCapabilities{
		ResolveIdentifier: bridgev2.ResolveIdentifierCapabilities{
			CreateDM: true,
			// Only friends and users already seen by the bridge can be
			// found by name; Discord has no global username lookup.
			LookupUsername: true,
			ContactList:    true,
			Search:         true,
		},
		GroupCreation: map[string]bridgev2.GroupTypeCapabilities{
			"group_dm": {
				TypeDescription: "Discord group DM",
				Name:            bridgev2.GroupFieldCapability{Allowed: true, MaxLength: 100},
				Participants:    bridgev2.GroupFieldCapability{Allowed: true, Required: true, MinLength: 2, MaxLength: maxGroupDMRecipients},
			},
		},
	},
}

func (d *DiscordConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return DiscordGeneralCaps
}

func (d *DiscordConnector) GetBridgeInfoVersion() (info, caps int) {
	return 1, 5
}

/*func supportedIfFFmpeg() event.CapabilitySupportLevel {
	if ffmpeg.Supported() {
		return event.CapLevelPartialSupport
	}
	return event.CapLevelRejected
}*/

func capID() string {
	base := "fi.mau.discord.capabilities.2026_09_30"
	if ffmpeg.Supported() {
		return base + "+ffmpeg"
	}
	return base
}

// MaxTextLength and MaxFileSize are what an account without Nitro can send.
// limitsFor says what a given account can send in a given server.
const MaxTextLength = 2000
const MaxFileSize = 10 * 1024 * 1024

// The limits below aren't in discordgo. They are the ones Discord documents
// for its plans and server boost levels (Nitro: 500 MB uploads and 4000
// character messages; Nitro Basic and Nitro Classic: 50 MB uploads; server
// boost level 2: 50 MB, level 3: 100 MB), counted in MiB like the free limit
// is, as Discord's own client does.
const (
	nitroMaxTextLength = 4000

	nitroMaxFileSize      = 500 * 1024 * 1024
	nitroBasicMaxFileSize = 50 * 1024 * 1024
	boostTier2MaxFileSize = 50 * 1024 * 1024
	boostTier3MaxFileSize = 100 * 1024 * 1024
)

// limitsFor returns the longest message and the largest file that an account
// with the given subscription can send to a server with the given boost
// level. Outside servers the level is PremiumTierNone.
//
// A boosted server raises the upload limit for everyone in it and Nitro raises
// it for the account everywhere, so the larger of the two applies. Message
// length only depends on the account.
func limitsFor(premiumType discordgo.UserPremiumType, guildTier discordgo.PremiumTier) (maxTextLength int, maxFileSize int64) {
	maxTextLength, maxFileSize = MaxTextLength, MaxFileSize
	switch premiumType {
	case discordgo.UserPremiumTypeNitro:
		maxTextLength, maxFileSize = nitroMaxTextLength, nitroMaxFileSize
	case discordgo.UserPremiumTypeNitroBasic, discordgo.UserPremiumTypeNitroClassic:
		maxFileSize = nitroBasicMaxFileSize
	}
	// Level 1 doesn't raise the upload limit, and neither does a plan or a
	// level this code doesn't know: too low a limit only refuses a file that
	// would have gone through, too high a one lets a send fail on Discord.
	switch guildTier {
	case discordgo.PremiumTier2:
		maxFileSize = max(maxFileSize, boostTier2MaxFileSize)
	case discordgo.PremiumTier3:
		maxFileSize = max(maxFileSize, boostTier3MaxFileSize)
	}
	return maxTextLength, maxFileSize
}

// applyLimits sets the limits of caps, and marks them in the ID when they
// differ from the defaults: the ID is what decides whether a room's
// capabilities are sent again.
func applyLimits(caps *event.RoomFeatures, maxTextLength int, maxFileSize int64) {
	if maxTextLength == MaxTextLength && maxFileSize == MaxFileSize {
		return
	}
	caps.ID += fmt.Sprintf("+text%d+file%d", maxTextLength, maxFileSize)
	caps.MaxTextLength = maxTextLength
	for _, file := range caps.File {
		file.MaxCaptionLength = maxTextLength
		file.MaxSize = maxFileSize
	}
}

var discordCaps = &event.RoomFeatures{
	ID:       capID(),
	Reply:    event.CapLevelFullySupported,
	Reaction: event.CapLevelFullySupported,
	Edit:     event.CapLevelFullySupported,
	Delete:   event.CapLevelFullySupported,
	Formatting: event.FormattingFeatureMap{
		event.FmtBold:               event.CapLevelFullySupported,
		event.FmtItalic:             event.CapLevelFullySupported,
		event.FmtStrikethrough:      event.CapLevelFullySupported,
		event.FmtInlineCode:         event.CapLevelFullySupported,
		event.FmtCodeBlock:          event.CapLevelFullySupported,
		event.FmtSyntaxHighlighting: event.CapLevelFullySupported,
		event.FmtBlockquote:         event.CapLevelFullySupported,
		event.FmtInlineLink:         event.CapLevelFullySupported,
		event.FmtUserLink:           event.CapLevelFullySupported,
		event.FmtRoomLink:           event.CapLevelUnsupported, // TODO: Support.
		event.FmtEventLink:          event.CapLevelUnsupported, // TODO: Support.
		event.FmtAtRoomMention:      event.CapLevelUnsupported, // TODO: Support.
		event.FmtUnorderedList:      event.CapLevelFullySupported,
		event.FmtOrderedList:        event.CapLevelFullySupported,
		event.FmtListStart:          event.CapLevelFullySupported,
		event.FmtListJumpValue:      event.CapLevelUnsupported,
		event.FmtCustomEmoji:        event.CapLevelUnsupported, // TODO: Support.
	},
	File: event.FileFeatureMap{
		event.MsgImage: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"image/jpeg": event.CapLevelFullySupported,
				"image/png":  event.CapLevelFullySupported,
				"image/gif":  event.CapLevelFullySupported,
				"image/webp": event.CapLevelFullySupported,
				"image/*":    event.CapLevelPartialSupport,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
		event.MsgVideo: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"video/mp4":  event.CapLevelFullySupported,
				"video/webm": event.CapLevelFullySupported,
				"video/*":    event.CapLevelPartialSupport,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
		event.MsgAudio: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"audio/mpeg": event.CapLevelFullySupported,
				"audio/webm": event.CapLevelFullySupported,
				"audio/wav":  event.CapLevelFullySupported,
				"audio/*":    event.CapLevelPartialSupport,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
		event.CapMsgVoice: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"audio/ogg; codecs=opus":  event.CapLevelFullySupported,
				"audio/ogg":               event.CapLevelFullySupported,
				"audio/webm; codecs=opus": event.CapLevelFullySupported,
				"audio/webm":              event.CapLevelFullySupported,
				"audio/*":                 event.CapLevelPartialSupport,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
		event.MsgFile: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"*/*": event.CapLevelFullySupported,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
		event.CapMsgGIF: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"image/gif": event.CapLevelFullySupported,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
	},
	// Discord polls: two to ten answers, and a single or a multiple choice.
	Poll:                event.CapLevelFullySupported,
	PollEnd:             event.CapLevelFullySupported,
	PollMaxOptions:      msgconv.MaxPollAnswers,
	PollOptionMaxLength: msgconv.MaxPollAnswerLength,
	LocationMessage:     event.CapLevelUnsupported,
	MaxTextLength:       MaxTextLength,
	Thread:              event.CapLevelPartialSupport,
}

func (d *DiscordClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	meta := portal.Metadata.(*discordid.PortalMetadata)
	var premiumType discordgo.UserPremiumType
	var guildTier discordgo.PremiumTier
	// Before the first connection there is no session state to ask, and the
	// limits of an account without Nitro are the ones that always work.
	if d.Session != nil && d.Session.State != nil {
		if user := d.Session.State.User; user != nil {
			premiumType = user.PremiumType
		}
		if meta.GuildID != "" {
			if guild, err := d.Session.State.Guild(meta.GuildID); err == nil && guild != nil {
				guildTier = guild.PremiumTier
			}
		}
	}
	return portalCaps(meta, premiumType, guildTier)
}

// portalCaps returns the capabilities of a room for an account with the given
// subscription, in a server with the given boost level.
func portalCaps(meta *discordid.PortalMetadata, premiumType discordgo.UserPremiumType, guildTier discordgo.PremiumTier) *event.RoomFeatures {
	caps := discordCaps.Clone()
	if meta.GuildID == "" {
		caps.Thread = event.CapLevelUnsupported
	}
	roomManagementCaps(caps, portalChannelKind(meta))
	maxTextLength, maxFileSize := limitsFor(premiumType, guildTier)
	applyLimits(caps, maxTextLength, maxFileSize)
	return caps
}

// roomManagementCaps says which room settings can be changed from Matrix (roommgmt.go).
func roomManagementCaps(caps *event.RoomFeatures, kind channelKind) {
	caps.ID += "+" + [...]string{"other", "dm", "group", "channel"}[kind]
	switch kind {
	case kindDM:
		caps.DeleteChat = true
	case kindGroupDM:
		caps.State = event.StateFeatureMap{
			event.StateRoomName.Type:   {Level: event.CapLevelFullySupported},
			event.StateRoomAvatar.Type: {Level: event.CapLevelFullySupported},
		}
		caps.MemberActions = event.MemberFeatureMap{
			event.MemberActionInvite: event.CapLevelFullySupported,
			event.MemberActionKick:   event.CapLevelFullySupported,
		}
		caps.DeleteChat = true
	case kindServerChannel:
		caps.State = event.StateFeatureMap{
			event.StateRoomName.Type: {Level: event.CapLevelFullySupported},
			event.StateTopic.Type:    {Level: event.CapLevelFullySupported},
		}
	}
}
