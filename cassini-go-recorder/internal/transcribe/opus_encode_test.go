package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/oggwriter"
)

// The fixed-64k policy exists so meetings published before D-850 rebuild to
// the same Opus packets, and keep their marks. Its encoder arguments are the
// ones MixDownToWebM hard-coded then; this list must never change.
func TestFixed64kEncodeIsThePreD850Encode(t *testing.T) {
	enc, err := ChooseAudioEncode(AudioEncodeFixed64k, []SourceAudio{{Codec: "aac", SampleRate: 16000, SpeechBitrateBps: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-c:a", "libopus", "-b:a", "64k", "-vbr", "on", "-compression_level", "10", "-application", "voip"}
	if got := enc.libopusArgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("fixed-64k encoder args = %q, want the pre-D-850 %q", got, want)
	}
}

func TestMatchSourceSizesTheEncodeFromTheSources(t *testing.T) {
	opusTrack := func(bps int) SourceAudio { return SourceAudio{Codec: "opus", SampleRate: 48000, SpeechBitrateBps: bps} }
	tests := []struct {
		name        string
		sources     []SourceAudio
		wantBitrate int
		wantCutoff  int
	}{
		{
			// The case that motivated D-850: a 16 kHz phone recording at
			// 24 kb/s used to publish at 64 kb/s fullband.
			name:        "16 kHz AAC phone import is wideband at a third over its rate",
			sources:     []SourceAudio{{Codec: "aac", SampleRate: 16000, SpeechBitrateBps: 24000}},
			wantBitrate: 32000, wantCutoff: 8000,
		},
		{
			name:        "lossless 16 kHz source stops at the wideband ceiling",
			sources:     []SourceAudio{{Codec: "flac", SampleRate: 16000, SpeechBitrateBps: 156000}},
			wantBitrate: 32000, wantCutoff: 8000,
		},
		{
			name:        "Talk tracks at 32 kb/s follow the highest-rate track, fullband",
			sources:     []SourceAudio{opusTrack(30900), opusTrack(32900), opusTrack(32000)},
			wantBitrate: 44000, wantCutoff: 20000,
		},
		{
			name:        "high-rate Opus sources keep the old 64k ceiling",
			sources:     []SourceAudio{opusTrack(96000), opusTrack(107000)},
			wantBitrate: 64000, wantCutoff: 20000,
		},
		{
			name:        "very low-rate sources are lifted to the band floor",
			sources:     []SourceAudio{opusTrack(9000)},
			wantBitrate: 24000, wantCutoff: 20000,
		},
		{
			name:        "one fullband track makes the whole mix fullband",
			sources:     []SourceAudio{{Codec: "aac", SampleRate: 16000, SpeechBitrateBps: 24000}, opusTrack(20000)},
			wantBitrate: 32000, wantCutoff: 20000,
		},
		{
			name:        "8 kHz telephone audio is narrowband",
			sources:     []SourceAudio{{Codec: "pcm_mulaw", SampleRate: 8000, SpeechBitrateBps: 64000}},
			wantBitrate: 16000, wantCutoff: 4000,
		},
		{
			// A source whose rate could not be measured says nothing about
			// what the mix needs; the band ceiling is never worse than before.
			name:        "an unmeasured source gets the band ceiling",
			sources:     []SourceAudio{opusTrack(32000), opusTrack(0)},
			wantBitrate: 64000, wantCutoff: 20000,
		},
		{
			name:        "no sources at all is the old fullband 64k",
			sources:     nil,
			wantBitrate: 64000, wantCutoff: 20000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc, err := ChooseAudioEncode(AudioEncodeMatchSource, tt.sources)
			if err != nil {
				t.Fatal(err)
			}
			if enc.Policy != AudioEncodeMatchSource || enc.BitrateBps != tt.wantBitrate || enc.CutoffHz != tt.wantCutoff {
				t.Fatalf("encode = %+v, want match-source at %d bps, cutoff %d Hz", enc, tt.wantBitrate, tt.wantCutoff)
			}
		})
	}
}

func TestChooseAudioEncodeRejectsUnknownPolicies(t *testing.T) {
	if ValidAudioEncodePolicy("no-such-policy") {
		t.Fatal("an unknown policy name was accepted")
	}
	if _, err := ChooseAudioEncode("no-such-policy", nil); err == nil {
		t.Fatal("ChooseAudioEncode accepted an unknown policy")
	}
	enc, err := ChooseAudioEncode("", []SourceAudio{{Codec: "aac", SampleRate: 16000, SpeechBitrateBps: 24000}})
	if err != nil || enc.Policy != DefaultAudioEncodePolicy {
		t.Fatalf("empty policy = %+v (%v), want the default %s", enc, err, DefaultAudioEncodePolicy)
	}
}

func TestSpeechBitrateFromHistogramIgnoresSilence(t *testing.T) {
	tests := []struct {
		name string
		hist map[int]int
		want int
	}{
		{
			// A Talk participant who speaks a sixth of the time: VBR silence
			// frames dominate the packet count but not the speech rate.
			name: "mostly silent VBR Opus track reads its speech rate",
			hist: map[int]int{12800: 8000, 30000: 600, 32000: 900, 34000: 500},
			want: 31900, // the mean of the three speech buckets
		},
		{
			name: "DTX frames do not drag the estimate down",
			hist: map[int]int{1200: 7000, 96000: 2000},
			want: 96000,
		},
		{
			name: "constant-rate source reads its average",
			hist: map[int]int{20000: 100, 24000: 800, 28000: 100},
			want: 24000,
		},
		{
			name: "too few packets is unknown",
			hist: map[int]int{32000: minSpeechPackets - 1},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := speechBitrateFromHistogram(tt.hist); got != tt.want {
				t.Fatalf("speech bitrate = %d, want %d", got, tt.want)
			}
		})
	}
}

// ffprobe's packet listing as probeSpeechBitrates requests it. Each packet line
// is "stream_index,duration_time,size,data" and, with -show_data, is followed
// by a hex dump whose first byte is the Opus TOC byte: 0xfc is a stereo
// 20 ms fullband frame, 0xf8 the same frame coded mono.
func TestPacketRateHistogramsCountsStereoPacketsPerChannel(t *testing.T) {
	listing := strings.Join([]string{
		`0,N/A,19,"`, // priming packet: no duration, no rate
		`00000000: fcff fe                                ...`,
		`"`,
		`0,0.020000,163,"`, // 65.2 kb/s coded stereo: 32.6 kb/s per channel
		`00000000: fcdf 5119 6775 1acc 5fb0 04c1 0cc9 4625  ..Q.gu.._.....F%`,
		`00000010: 52f7 179d 9cc0 b942 1c69 351c beb7 c3a5  R,,....B.i5.....`,
		`00000020: 8bc9                                     .."`,
		`1,0.020000,75,"`, // 30 kb/s coded mono, in a container that says stereo
		`00000000: f8a1 0203 0405 0607 0809 0a0b 0c0d 0e0f  ................"`,
		`2,0.020000,0,""`, // an empty packet has no TOC byte: its stream's fallback
		`3,0.020000,160`,  // no dump (a mixed recording): its stream's fallback
	}, "\n") + "\n"
	hists, err := packetRateHistograms(strings.NewReader(listing), map[int]int{0: 1, 1: 1, 2: 1, 3: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]map[int]int{
		0: {32600: 1},
		1: {30000: 1},
		2: {0: 1},
		3: {32000: 1},
	}
	if !reflect.DeepEqual(hists, want) {
		t.Fatalf("per-channel packet rates = %v, want %v", hists, want)
	}
}

// The shape of nearly every recorded Talk call: one participant's client
// sends stereo at about 64 kb/s, the others mono at about 30, and the
// recording's container says two channels for every track (WebRTC always
// names Opus opus/48000/2, and the recorder writes that). The meeting mix is
// mono, so the stereo sender needs about what a 32 kb/s mono sender does; it
// must not size the whole meeting at 64 kb/s.
func TestMatchSourceCountsAStereoSenderPerChannel(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	stereo := filepath.Join(dir, "stereo.ogg")
	runFFmpegForTest(t, "-f", "lavfi", "-i", "anoisesrc=d=4:c=pink:seed=1", "-f", "lavfi", "-i", "anoisesrc=d=4:c=pink:seed=2",
		"-filter_complex", "[0][1]amerge=inputs=2", "-c:a", "libopus", "-b:a", "64k", "-ac", "2", stereo)
	inputs := []string{"-i", stereo}
	for i, seed := range []string{"3", "4"} {
		mono := filepath.Join(dir, "mono"+strconv.Itoa(i)+".ogg")
		runFFmpegForTest(t, "-f", "lavfi", "-i", "anoisesrc=d=4:c=pink:seed="+seed, "-c:a", "libopus", "-b:a", "30k", "-ac", "1", mono)
		talk := filepath.Join(dir, "talk"+strconv.Itoa(i)+".ogg")
		rewriteOpusAsTalkTrack(t, mono, talk)
		inputs = append(inputs, "-i", talk)
	}
	mkv := filepath.Join(dir, "recording.mkv")
	args := append([]string{}, inputs...)
	for i := range 3 {
		args = append(args, "-map", strconv.Itoa(i)+":a")
	}
	runFFmpegForTest(t, append(args, "-c", "copy", mkv)...)

	streams, _, err := ProbeMKV(mkv)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range streams {
		if s.Channels != 2 {
			t.Fatalf("stream %d says %d channels; the fixture must look like a Talk recording (2 for every track)", s.Index, s.Channels)
		}
	}
	rates, err := probeSpeechBitrates(mkv, streams)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range streams {
		if r := rates[s.Index]; r < 24000 || r > 40000 {
			t.Fatalf("stream %d reads %d bps per channel, want about 30 kb/s (rates %v)", s.Index, r, rates)
		}
	}
	enc, err := ChooseMeetingAudioEncode(mkv, streams, AudioEncodeMatchSource)
	if err != nil {
		t.Fatal(err)
	}
	if enc.BitrateBps > 48000 {
		t.Fatalf("encode = %+v; the stereo sender set the mono mix's rate from both of its channels", enc)
	}
}

// rewriteOpusAsTalkTrack copies a mono Opus file's packets into an Ogg file
// whose header says two channels, the way the recorder writes Talk tracks.
func rewriteOpusAsTalkTrack(t *testing.T, src, dst string) {
	t.Helper()
	sizes, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0", "-show_entries", "packet=size", "-of", "csv=p=0", src).Output()
	if err != nil {
		t.Fatalf("list packets of %s: %v", src, err)
	}
	payload, err := exec.Command("ffmpeg", "-v", "error", "-i", src, "-map", "0:a:0", "-c", "copy", "-f", "data", "-").Output()
	if err != nil {
		t.Fatalf("read packets of %s: %v", src, err)
	}
	w, err := oggwriter.New(dst, 48000, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Fields(string(sizes)) {
		field, _, _ := strings.Cut(line, ",") // a packet with side data gets an empty extra field
		n, err := strconv.Atoi(field)
		if err != nil || n > len(payload) {
			t.Fatalf("packet %d size %q of %s does not fit the remaining %d bytes", i, field, src, len(payload))
		}
		pkt := &rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(i), Timestamp: uint32(i * 960)}, Payload: payload[:n]}
		if err := w.WriteRTP(pkt); err != nil {
			t.Fatal(err)
		}
		payload = payload[n:]
	}
	if len(payload) != 0 {
		t.Fatalf("%d bytes of %s left after its listed packets", len(payload), src)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func runFFmpegForTest(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// A 16 kHz source is published at its own bandwidth and a fraction of the old
// fixed rate; the chosen encode is recorded in manifest.json; and a rebuild
// asking for the recorded policy reproduces the same Opus packets, which is
// what keeps marks bound across a rerun (they are bound to a digest of the
// packets, not of the decoded sound). The fixed-64k rebuild of the same source
// is the pre-D-850 file: larger, and different packets.
func TestBuildMeetingArtifactSizesTheMixFromTheSource(t *testing.T) {
	requireFFMediaTools(t)
	src := filepath.Join("..", "..", "..", "harness", "media", "parakeet-smoke.mkv") // 16 kHz FLAC
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("public smoke fixture missing: %v", err)
	}
	type built struct {
		enc     AudioEncode
		size    int64
		packets string
	}
	build := func(policy string) built {
		t.Helper()
		out := t.TempDir()
		var stdout bytes.Buffer
		cfg := BuildConfig{TranscriptionMode: "off", AudioEncodePolicy: policy}
		if err := BuildMeetingArtifact(context.Background(), src, out, cfg, &stdout); err != nil {
			t.Fatalf("build (%q): %v\n%s", policy, err, stdout.String())
		}
		raw, err := os.ReadFile(filepath.Join(out, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var manifest artifactManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		if manifest.AudioEncode == nil {
			t.Fatalf("manifest records no audio encode: %s", raw)
		}
		webm := filepath.Join(out, "meeting.webm")
		info, err := os.Stat(webm)
		if err != nil {
			t.Fatal(err)
		}
		return built{enc: *manifest.AudioEncode, size: info.Size(), packets: opusPacketsSHA256(t, webm)}
	}

	first := build("")
	if first.enc.Policy != AudioEncodeMatchSource || first.enc.CutoffHz != 8000 || first.enc.BitrateBps != 32000 || first.enc.Application != "audio" {
		t.Fatalf("16 kHz FLAC source encoded as %+v, want match-source wideband (8000 Hz) at the 32 kb/s ceiling", first.enc)
	}
	if first.enc.SourceBandwidthHz != 8000 || first.enc.SourceSpeechBitrateBps <= 0 {
		t.Fatalf("source measurements not recorded: %+v", first.enc)
	}

	rerun := build(first.enc.Policy)
	if rerun.packets != first.packets {
		t.Fatalf("rebuild with the recorded policy changed the Opus packets: %s != %s", rerun.packets, first.packets)
	}

	legacy := build(AudioEncodeFixed64k)
	if legacy.enc != (AudioEncode{Policy: AudioEncodeFixed64k, BitrateBps: 64000, Application: "voip"}) {
		t.Fatalf("fixed-64k recorded as %+v", legacy.enc)
	}
	if legacy.packets == first.packets {
		t.Fatal("fixed-64k and match-source produced the same Opus packets; the policy did not reach the encoder")
	}
	if first.size*3 > legacy.size*2 {
		t.Fatalf("source-sized mix is %d bytes, not clearly smaller than the fixed-64k %d bytes", first.size, legacy.size)
	}
}

// opusPacketsSHA256 hashes the compressed packets of the file's audio, without
// decoding them; the digest does not depend on the container.
func opusPacketsSHA256(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-map", "0:a:0", "-c:a", "copy", "-f", "hash", "-hash", "sha256", "-").Output()
	if err != nil {
		t.Fatalf("hash Opus packets of %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}
