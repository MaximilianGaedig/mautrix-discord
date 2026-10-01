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

package msgconv

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"go.mau.fi/util/variationselector"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

func parseAllowedLinkPreviews(raw map[string]any) []string {
	if raw == nil {
		return nil
	}
	linkPreviews, ok := raw["com.beeper.linkpreviews"].([]any)
	if !ok {
		return nil
	}
	allowedLinkPreviews := make([]string, 0, len(linkPreviews))
	for _, preview := range linkPreviews {
		previewMap, ok := preview.(map[string]any)
		if !ok {
			continue
		}
		matchedURL, _ := previewMap["matched_url"].(string)
		if matchedURL != "" {
			allowedLinkPreviews = append(allowedLinkPreviews, matchedURL)
		}
	}
	return allowedLinkPreviews
}

func uploadDiscordAttachment(cli *http.Client, url string, data []byte) error {
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return err
	}

	for key, value := range discordgo.DroidBaseHeaders {
		req.Header.Set(key, value)
	}
	// The first-party web client does not set a content type, so we shouldn't
	// either.
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Referer", "https://discord.com/")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode > 300 {
		respData, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, respData)
	}
	return nil
}

// ToDiscord converts a Matrix message into a discordgo.MessageSend that is appropriate
// for bridging the message to Discord.
func (mc *MessageConverter) ToDiscord(
	ctx context.Context,
	session *discordgo.Session,
	msg *bridgev2.MatrixMessage,
	channelID string,
	refererOpt discordgo.RequestOption,
) (*discordgo.MessageSend, error) {
	ctx = context.WithValue(ctx, contextKeyPortal, msg.Portal)
	ctx = context.WithValue(ctx, contextKeyDiscordClient, session)
	var req discordgo.MessageSend
	if msg.InputTransactionID != "" {
		req.Nonce = string(msg.InputTransactionID)
	} else {
		req.Nonce = discordid.GenerateNonce()
	}
	log := zerolog.Ctx(ctx)

	if msg.ReplyTo != nil {
		req.Reference = &discordgo.MessageReference{
			ChannelID: discordid.ParseChannelPortalID(msg.ReplyTo.Room.ID),
			MessageID: discordid.ParseMessageID(msg.ReplyTo.ID),
		}
		if mc.replyIsSilent(ctx, msg) {
			// What Discord's own client sends with the reply's ping switched
			// off: every kind of mention still parsed, as it is without
			// allowed_mentions, and only the replied-to user left out.
			req.AllowedMentions = &discordgo.MessageAllowedMentions{
				Parse: []discordgo.AllowedMentionType{
					discordgo.AllowedMentionTypeUsers,
					discordgo.AllowedMentionTypeRoles,
					discordgo.AllowedMentionTypeEveryone,
				},
				RepliedUser: false,
			}
		}
	}

	content := msg.Content

	convertMatrix := func() {
		// NOTE: Real users should never send allowed_mentions (except for
		// silent replies, which are handled above).
		//
		// Since we only support real users at the moment, always ignore the
		// returned allowed mentions.
		req.Content, _ = mc.ConvertMatrixMessageContent(ctx, msg.Portal, content, parseAllowedLinkPreviews(msg.Event.Content.Raw))
		if content.MsgType == event.MsgEmote {
			req.Content = fmt.Sprintf("_%s_", req.Content)
		}
	}

	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		convertMatrix()
	case event.MsgAudio, event.MsgFile, event.MsgImage, event.MsgVideo:
		mediaData, err := mc.Bridge.Bot.DownloadMedia(ctx, content.URL, content.File)
		if err != nil {
			log.Err(err).Msg("Failed to download Matrix attachment for bridging")
			return nil, bridgev2.ErrMediaDownloadFailed
		}

		filename := content.Body
		hasCaption := content.FileName != "" && content.FileName != content.Body
		if content.FileName != "" {
			filename = content.FileName
		}
		isSpoiler := msg.Event.Content.Raw["page.codeberg.everypizza.msc4193.spoiler"] == true

		var voiceMeta *discordVoiceMetadata
		if content.MsgType == event.MsgAudio && !hasCaption && !isSpoiler {
			voiceMeta = getDiscordVoiceMetadata(content)
		}

		if hasCaption {
			convertMatrix()
		}
		if isSpoiler {
			filename = "SPOILER_" + filename
		}

		// TODO: Support attachments for relay/webhook. (A branch was removed here.)
		att := &discordgo.MessageAttachment{
			ID:       "0",
			Filename: filename,
		}
		if content.Info != nil {
			att.OriginalContentType = content.Info.MimeType
		}
		if voiceMeta != nil {
			req.Flags = ptr.Ptr(discordgo.MessageFlagsIsVoiceMessage)
			att.ContentType = voiceMeta.ContentType
			att.DurationSeconds = voiceMeta.DurationSeconds
			att.Waveform = voiceMeta.Waveform
			// Enforce the presence of a filename extension, or else Discord
			// gets angry and returns 50160 "Voice messages must have a single
			// audio attachment".
			att.Filename = "voice-message" + voiceAttachmentExtension(voiceMeta.ContentType)
		}

		uploadID := mc.NextDiscordUploadID()
		log.Debug().Str("upload_id", uploadID).Msg("Preparing attachment")
		filePrep := &discordgo.FilePrepare{
			Size:                len(mediaData),
			Name:                att.Filename,
			ID:                  uploadID,
			OriginalContentType: att.OriginalContentType,
		}
		prep, err := session.ChannelAttachmentCreate(channelID, &discordgo.ReqPrepareAttachments{
			Files: []*discordgo.FilePrepare{filePrep},
		}, refererOpt)

		if err != nil {
			log.Err(err).Msg("Failed to create attachment in preparation for attachment reupload")
			return nil, bridgev2.ErrMediaReuploadFailed
		}

		prepared := prep.Attachments[0]
		att.UploadedFilename = prepared.UploadFilename

		err = uploadDiscordAttachment(session.Client, prepared.UploadURL, mediaData)
		if err != nil {
			log.Err(err).Msg("Failed to reupload Discord attachment after preparing")
			return nil, bridgev2.ErrMediaReuploadFailed
		}

		req.Attachments = append(req.Attachments, att)
	}

	return &req, nil
}

// replyIsSilent says whether a reply should leave its target's author
// unpinged. Discord pings the author of the replied-to message unless told
// otherwise; on Matrix a reply only notifies the people in its m.mentions,
// where clients put the author by default and take them out when the user
// asks for a quiet reply.
//
// A message without m.mentions comes from a client that predates intentional
// mentions and so says nothing either way. It pings, as a reply on Discord does.
func (mc *MessageConverter) replyIsSilent(ctx context.Context, msg *bridgev2.MatrixMessage) bool {
	if msg.ReplyTo == nil || msg.Content == nil || msg.Content.Mentions == nil {
		return false
	}
	for _, mentioned := range msg.Content.Mentions.UserIDs {
		if msg.ReplyTo.SenderMXID != "" && mentioned == msg.ReplyTo.SenderMXID {
			return false
		}
		// The replied-to message may have been sent by another Matrix account
		// than the one mentioned (the author's ghost rather than their own
		// account, or the other way around), so compare who they are on
		// Discord as well.
		discordUserID, err := mc.resolveMentionedDiscordUserID(ctx, msg.Portal, mentioned)
		if err != nil {
			zerolog.Ctx(ctx).Debug().Err(err).
				Stringer("mentioned_mxid", mentioned).
				Msg("Failed to resolve a mentioned user while checking whether a reply pings its target")
			continue
		}
		if discordUserID != "" && discordid.MakeUserID(discordUserID) == msg.ReplyTo.SenderID {
			return false
		}
	}
	return true
}

// PollToDiscord converts a Matrix poll start into a message that carries a
// Discord poll.
func (mc *MessageConverter) PollToDiscord(msg *bridgev2.MatrixPollStart) (*discordgo.MessageSend, error) {
	poll, err := PollFromMatrix(msg.Content, PollDurationFromContent(msg.Event.Content.Raw))
	if err != nil {
		return nil, err
	}
	req := &discordgo.MessageSend{Poll: poll}
	if msg.InputTransactionID != "" {
		req.Nonce = string(msg.InputTransactionID)
	} else {
		req.Nonce = discordid.GenerateNonce()
	}
	if msg.ReplyTo != nil {
		req.Reference = &discordgo.MessageReference{
			ChannelID: discordid.ParseChannelPortalID(msg.ReplyTo.Room.ID),
			MessageID: discordid.ParseMessageID(msg.ReplyTo.ID),
		}
	}
	return req, nil
}

func (mc *MessageConverter) ConvertMatrixMessageContent(ctx context.Context, portal *bridgev2.Portal, content *event.MessageEventContent, allowedLinkPreviews []string) (string, *discordgo.MessageAllowedMentions) {
	allowedMentions := &discordgo.MessageAllowedMentions{
		Parse:       []discordgo.AllowedMentionType{},
		Users:       []string{},
		RepliedUser: true,
	}

	if content.Format == event.FormatHTML && len(content.FormattedBody) > 0 {
		ctx := format.NewContext(ctx)
		ctx.ReturnData[formatterContextInputAllowedLinkPreviewsKey] = allowedLinkPreviews
		ctx.ReturnData[formatterContextPortalKey] = portal
		ctx.ReturnData[formatterContextAllowedMentionsKey] = allowedMentions
		if content.Mentions != nil {
			ctx.ReturnData[formatterContextInputAllowedMentionsKey] = content.Mentions.UserIDs
		}
		return variationselector.FullyQualify(mc.HTMLParser.Parse(content.FormattedBody, ctx)), allowedMentions
	} else {
		return variationselector.FullyQualify(escapeDiscordMarkdown(content.Body)), allowedMentions
	}
}
