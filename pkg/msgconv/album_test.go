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
	"encoding/json"
	"reflect"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

func testPart(msgType event.MessageType) *bridgev2.ConvertedMessagePart {
	return &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: &event.MessageEventContent{MsgType: msgType}}
}

func TestTagAlbum(t *testing.T) {
	img, failed, vid, audio := testPart(event.MsgImage), testPart(event.MsgNotice), testPart(event.MsgVideo), testPart(event.MsgAudio)
	img.Extra = map[string]any{"page.codeberg.everypizza.msc4193.spoiler": true}
	tagAlbum([]*bridgev2.ConvertedMessagePart{img, failed, vid, audio}, discordAlbumID("123"))
	expect := map[*bridgev2.ConvertedMessagePart]*AlbumInfo{
		img:    {ID: "discord:123", Index: 0, Count: 4},
		failed: nil,
		vid:    {ID: "discord:123", Index: 2, Count: 4},
		audio:  {ID: "discord:123", Index: 3, Count: 4},
	}
	for part, expected := range expect {
		got, _ := part.Extra[AlbumFieldKey].(*AlbumInfo)
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("%s: expected %+v, got %+v", part.Content.MsgType, expected, got)
		}
	}
	if img.Extra["page.codeberg.everypizza.msc4193.spoiler"] != true {
		t.Error("existing extra fields must be kept")
	}
	data, _ := json.Marshal(img.Extra[AlbumFieldKey])
	if string(data) != `{"id":"discord:123","index":0,"count":4}` {
		t.Errorf("unexpected JSON %s", data)
	}
}

func TestTagAlbumSingle(t *testing.T) {
	img := testPart(event.MsgImage)
	tagAlbum([]*bridgev2.ConvertedMessagePart{img}, "discord:1")
	if img.Extra != nil {
		t.Errorf("single attachment must not be tagged: %v", img.Extra)
	}
	tagAlbum(nil, "discord:1")
}

func TestTagAlbumSkipsNonMedia(t *testing.T) {
	// Text and sticker parts are never album items.
	text := testPart(event.MsgText)
	sticker := &bridgev2.ConvertedMessagePart{Type: event.EventSticker, Content: &event.MessageEventContent{}}
	tagAlbum([]*bridgev2.ConvertedMessagePart{sticker, text}, "discord:2")
	if text.Extra != nil || sticker.Extra != nil {
		t.Error("non-media parts must not be tagged")
	}
}
