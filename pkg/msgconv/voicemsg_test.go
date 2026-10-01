package msgconv

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

// renderTestAttachment converts an attachment and returns the content as the homeserver gets it.
func renderTestAttachment(t *testing.T, att *discordgo.MessageAttachment) map[string]any {
	t.Helper()
	mc := &MessageConverter{
		Bridge:      &bridgev2.Bridge{Matrix: fakeMatrix{}},
		DirectMedia: true,
	}
	mediaInfo := discordid.NewMediaInfoV1("900000000000000099", "900000000000000002", "900000000000000001", att.ID)
	part := mc.renderDiscordAttachment(context.Background(), att, &mediaInfo)
	if part == nil {
		t.Fatal("the attachment was dropped")
	}
	data, err := json.Marshal(&event.Content{Parsed: part.Content, Raw: part.Extra})
	if err != nil {
		t.Fatal(err)
	}
	// The homeserver refuses an event with a number that isn't an integer in it.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if num, ok := tok.(json.Number); ok && strings.ContainsAny(num.String(), ".eE") {
			t.Errorf("%s is not an integer in %s", num, data)
		}
	}
	var content map[string]any
	if err = json.Unmarshal(data, &content); err != nil {
		t.Fatal(err)
	}
	return content
}

func TestVoiceMessageToMatrix(t *testing.T) {
	content := renderTestAttachment(t, &discordgo.MessageAttachment{
		ID:          "900000000000000010",
		Filename:    "voice-message.ogg",
		URL:         "https://cdn.example.test/voice-message.ogg",
		ContentType: "audio/ogg",
		Size:        4096,
		// 1.001 * 1000 is 1000.9999999999999 in floating point.
		DurationSeconds: 1.001,
		Waveform:        []byte{0, 1, 64, 128, 254, 255},
	})

	if content["msgtype"] != "m.audio" {
		t.Errorf("msgtype is %v", content["msgtype"])
	}
	if _, ok := content["org.matrix.msc3245.voice"].(map[string]any); !ok {
		t.Errorf("not marked as a voice message: %v", content["org.matrix.msc3245.voice"])
	}
	audio, ok := content["org.matrix.msc1767.audio"].(map[string]any)
	if !ok {
		t.Fatalf("no audio details: %v", content["org.matrix.msc1767.audio"])
	}
	if audio["duration"] != float64(1001) {
		t.Errorf("duration is %v ms, want 1001", audio["duration"])
	}
	if info, _ := content["info"].(map[string]any); info["duration"] != float64(1001) {
		t.Errorf("info.duration is %v ms, want 1001", info["duration"])
	}
	// Discord's samples go up to 255, Matrix's up to 1024, so four times as far.
	rawWaveform, _ := audio["waveform"].([]any)
	waveform := make([]int, len(rawWaveform))
	for i, v := range rawWaveform {
		waveform[i] = int(v.(float64))
	}
	if want := []int{0, 4, 256, 512, 1016, 1020}; !slices.Equal(waveform, want) {
		t.Errorf("waveform is %v, want %v", waveform, want)
	}
}

func TestVoiceMessageWaveformRoundTrip(t *testing.T) {
	// A voice message that goes to Matrix and comes back, as a forward does, keeps its waveform.
	original := make([]byte, 256)
	for i := range original {
		original[i] = byte(i)
	}
	back := matrixWaveformToDiscord(discordWaveformToMatrix(original), 60_000)
	if !bytes.Equal(back, original) {
		t.Errorf("waveform changed on the way back:\n got %v\nwant %v", back, original)
	}
}

func TestPlainAudioIsNotVoiceMessage(t *testing.T) {
	content := renderTestAttachment(t, &discordgo.MessageAttachment{
		ID:          "900000000000000011",
		Filename:    "song.mp3",
		URL:         "https://cdn.example.test/song.mp3",
		ContentType: "audio/mpeg",
		Size:        4096,
	})
	if content["msgtype"] != "m.audio" {
		t.Errorf("msgtype is %v", content["msgtype"])
	}
	for _, key := range []string{"org.matrix.msc3245.voice", "org.matrix.msc1767.audio"} {
		if _, ok := content[key]; ok {
			t.Errorf("an uploaded audio file has %s", key)
		}
	}
}
