package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
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

func TestSourceV1SizesTheEncodeFromTheSources(t *testing.T) {
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
			name:        "Talk tracks at 32 kb/s follow the richest track, fullband",
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
			enc, err := ChooseAudioEncode(AudioEncodeSourceV1, tt.sources)
			if err != nil {
				t.Fatal(err)
			}
			if enc.Policy != AudioEncodeSourceV1 || enc.BitrateBps != tt.wantBitrate || enc.CutoffHz != tt.wantCutoff {
				t.Fatalf("encode = %+v, want source-v1 at %d bps, cutoff %d Hz", enc, tt.wantBitrate, tt.wantCutoff)
			}
		})
	}
}

func TestChooseAudioEncodeRejectsUnknownPolicies(t *testing.T) {
	if ValidAudioEncodePolicy("source-v0") {
		t.Fatal("an unknown policy name was accepted")
	}
	if _, err := ChooseAudioEncode("source-v0", nil); err == nil {
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

// A 16 kHz source is published at its own bandwidth and a fraction of the old
// fixed rate; the chosen encode is recorded in manifest.json; and a rebuild
// asking for the recorded policy reproduces the same audio, which is what
// keeps marks bound across a rerun. The fixed-64k rebuild of the same source
// is the pre-D-850 file: larger, and different audio.
func TestBuildMeetingArtifactSizesTheMixFromTheSource(t *testing.T) {
	requireFFMediaTools(t)
	src := filepath.Join("..", "..", "..", "harness", "media", "parakeet-smoke.mkv") // 16 kHz FLAC
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("public smoke fixture missing: %v", err)
	}
	type built struct {
		enc  AudioEncode
		size int64
		pcm  string
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
		pcm, _, err := PCMsha256FromWebM(webm)
		if err != nil {
			t.Fatal(err)
		}
		return built{enc: *manifest.AudioEncode, size: info.Size(), pcm: pcm}
	}

	first := build("")
	if first.enc.Policy != AudioEncodeSourceV1 || first.enc.CutoffHz != 8000 || first.enc.BitrateBps != 32000 || first.enc.Application != "audio" {
		t.Fatalf("16 kHz FLAC source encoded as %+v, want source-v1 wideband (8000 Hz) at the 32 kb/s ceiling", first.enc)
	}
	if first.enc.SourceBandwidthHz != 8000 || first.enc.SourceSpeechBitrateBps <= 0 {
		t.Fatalf("source measurements not recorded: %+v", first.enc)
	}

	rerun := build(first.enc.Policy)
	if rerun.pcm != first.pcm {
		t.Fatalf("rebuild with the recorded policy changed the audio: %s != %s", rerun.pcm, first.pcm)
	}

	legacy := build(AudioEncodeFixed64k)
	if legacy.enc != (AudioEncode{Policy: AudioEncodeFixed64k, BitrateBps: 64000, Application: "voip"}) {
		t.Fatalf("fixed-64k recorded as %+v", legacy.enc)
	}
	if legacy.pcm == first.pcm {
		t.Fatal("fixed-64k and source-v1 produced the same audio; the policy did not reach the encoder")
	}
	if first.size*3 > legacy.size*2 {
		t.Fatalf("source-sized mix is %d bytes, not clearly smaller than the fixed-64k %d bytes", first.size, legacy.size)
	}
}
