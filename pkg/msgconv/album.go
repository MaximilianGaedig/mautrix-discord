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
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

// AlbumFieldKey is the top-level content field that marks Matrix media events which were sent
// together as attachments of one Discord message.
//
// Every attachment is still bridged as its own m.room.message event, rather than a single gallery
// event, so that clients which do not know about albums keep rendering each attachment normally.
// Clients that do can group events sharing a fi.mau.album.id.
const AlbumFieldKey = "fi.mau.album"

// AlbumInfo is the value of the fi.mau.album field.
type AlbumInfo struct {
	// ID is a stable opaque identifier, unique within the portal.
	ID string `json:"id"`
	// Index is the 0-based position of the attachment within the message.
	Index int `json:"index"`
	// Count is the total number of attachments, omitted when unknown.
	Count int `json:"count,omitempty"`
}

// discordAlbumID derives the album id from the Discord message.
//
// Derived rather than generated, so that a backfill of the same message produces the same id: a
// random one would make the same album group differently depending on how it arrived.
func discordAlbumID(messageID string) string {
	return "discord:" + messageID
}

func isAlbumablePart(part *bridgev2.ConvertedMessagePart) bool {
	if part == nil || part.Type != event.EventMessage || part.Content == nil {
		return false
	}
	switch part.Content.MsgType {
	case event.MsgImage, event.MsgVideo, event.MsgFile, event.MsgAudio:
		return true
	default:
		return false
	}
}

// tagAlbum marks the attachment parts of one Discord message as an album.
//
// The position in items is the index and len(items) the count. Parts that failed to bridge keep
// their slot but are not tagged, so the indices still line up with what the sender posted. A
// message with a single attachment gets no field at all - an "album" of one is just a picture.
func tagAlbum(items []*bridgev2.ConvertedMessagePart, id string) {
	if len(items) < 2 {
		return
	}
	for i, part := range items {
		if !isAlbumablePart(part) {
			continue
		}
		if part.Extra == nil {
			part.Extra = make(map[string]any)
		}
		part.Extra[AlbumFieldKey] = &AlbumInfo{ID: id, Index: i, Count: len(items)}
	}
}
