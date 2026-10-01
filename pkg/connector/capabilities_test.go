package connector

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

func TestLimitsFor(t *testing.T) {
	const mib = 1024 * 1024
	for _, tc := range []struct {
		name     string
		premium  discordgo.UserPremiumType
		tier     discordgo.PremiumTier
		wantText int
		wantFile int64
	}{
		{"no Nitro, DM or unboosted server", discordgo.UserPremiumTypeNone, discordgo.PremiumTierNone, 2000, 10 * mib},
		{"no Nitro, level 1 server", discordgo.UserPremiumTypeNone, discordgo.PremiumTier1, 2000, 10 * mib},
		{"no Nitro, level 2 server", discordgo.UserPremiumTypeNone, discordgo.PremiumTier2, 2000, 50 * mib},
		{"no Nitro, level 3 server", discordgo.UserPremiumTypeNone, discordgo.PremiumTier3, 2000, 100 * mib},
		{"Nitro Basic, DM", discordgo.UserPremiumTypeNitroBasic, discordgo.PremiumTierNone, 2000, 50 * mib},
		{"Nitro Basic, level 3 server", discordgo.UserPremiumTypeNitroBasic, discordgo.PremiumTier3, 2000, 100 * mib},
		{"Nitro Classic, DM", discordgo.UserPremiumTypeNitroClassic, discordgo.PremiumTierNone, 2000, 50 * mib},
		{"Nitro Classic, level 2 server", discordgo.UserPremiumTypeNitroClassic, discordgo.PremiumTier2, 2000, 50 * mib},
		{"Nitro, DM", discordgo.UserPremiumTypeNitro, discordgo.PremiumTierNone, 4000, 500 * mib},
		{"Nitro, level 3 server", discordgo.UserPremiumTypeNitro, discordgo.PremiumTier3, 4000, 500 * mib},
		{"a plan and a level added later", discordgo.UserPremiumType(9), discordgo.PremiumTier(9), 2000, 10 * mib},
	} {
		text, file := limitsFor(tc.premium, tc.tier)
		if text != tc.wantText || file != tc.wantFile {
			t.Errorf("%s: got %d characters and %d bytes, want %d and %d", tc.name, text, file, tc.wantText, tc.wantFile)
		}
	}
}

func TestPortalCapsLimits(t *testing.T) {
	channelType := discordgo.ChannelTypeGuildText
	meta := &discordid.PortalMetadata{GuildID: "1", ChannelType: &channelType}

	plain := portalCaps(meta, discordgo.UserPremiumTypeNone, discordgo.PremiumTierNone)
	if plain.MaxTextLength != 2000 || plain.File[event.MsgFile].MaxSize != 10*1024*1024 {
		t.Errorf("without Nitro: %d characters, %d bytes", plain.MaxTextLength, plain.File[event.MsgFile].MaxSize)
	}

	nitro := portalCaps(meta, discordgo.UserPremiumTypeNitro, discordgo.PremiumTierNone)
	if nitro.MaxTextLength != 4000 {
		t.Errorf("Nitro text length = %d", nitro.MaxTextLength)
	}
	for msgType, file := range nitro.File {
		if file.MaxSize != 500*1024*1024 || file.MaxCaptionLength != 4000 {
			t.Errorf("Nitro %s: %d bytes, %d character caption", msgType, file.MaxSize, file.MaxCaptionLength)
		}
	}
	boosted := portalCaps(meta, discordgo.UserPremiumTypeNone, discordgo.PremiumTier3)
	if boosted.File[event.MsgImage].MaxSize != 100*1024*1024 || boosted.MaxTextLength != 2000 {
		t.Errorf("level 3 server: %d bytes, %d characters", boosted.File[event.MsgImage].MaxSize, boosted.MaxTextLength)
	}

	// Rooms only get their capabilities again when the ID changes.
	if nitro.ID == plain.ID || boosted.ID == plain.ID || nitro.ID == boosted.ID {
		t.Errorf("capability IDs don't tell the limits apart: %q, %q, %q", plain.ID, nitro.ID, boosted.ID)
	}
	// The shared defaults must not have been changed through the clone.
	if discordCaps.MaxTextLength != 2000 || discordCaps.File[event.MsgFile].MaxSize != 10*1024*1024 {
		t.Error("the default capabilities were modified")
	}
}
