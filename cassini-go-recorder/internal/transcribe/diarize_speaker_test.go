package transcribe

import (
	"bytes"
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

// A participant with one stream is diarized from that stream's own decoded
// buffer. Copying it into a mix first held the whole track twice while the
// model ran — about 460 MB more for a two-hour meeting.
func TestDiarizeSpeakerDiarizesASingleStreamWithoutCopyingIt(t *testing.T) {
	mkvPath := buildParticipantMetadataMeeting(t, t.TempDir())
	streams, _, err := ProbeMKV(mkvPath)
	if err != nil {
		t.Fatalf("ProbeMKV() error = %v", err)
	}
	speakerID := streams[0].SpeakerID

	decoded := make([]float32, 16000)
	origAdd, origDiarize := addSpeakerFloatsFn, diarizeFn
	t.Cleanup(func() { addSpeakerFloatsFn, diarizeFn = origAdd, origDiarize })
	addSpeakerFloatsFn = func(_ string, _ AudioStream, mix []float32) ([]float32, error) {
		if mix != nil {
			t.Fatal("the first stream was handed a mix")
		}
		return decoded, nil
	}
	var got []float32
	diarizeFn = func(_ DiarizationModel, samples []float32, _, _ int) ([]SpeakerTurn, error) {
		got = samples
		return []SpeakerTurn{{StartMS: 0, EndMS: 500, Speaker: 0}}, nil
	}

	set, err := DiarizeSpeaker(context.Background(), mkvPath, speakerID, DiarizationModel{Name: "m", SHA256: "x"}, DefaultDiarizationThreads)
	if err != nil {
		t.Fatalf("DiarizeSpeaker() error = %v", err)
	}
	if len(set.Streams) != 1 || len(got) != len(decoded) {
		t.Fatalf("streams %v, diarized %d samples; want one stream of %d", set.Streams, len(got), len(decoded))
	}
	if &got[0] != &decoded[0] {
		t.Fatal("the single stream was copied before diarizing; it must be diarized in place")
	}
}

// A participant who reconnected has a stream per connection. Each later one
// is decoded straight into the running mix: decoding it into a buffer of its
// own first held two meeting-length tracks at once, which the operator's
// memory admission (one track) does not allow for — 3.5 GB for eight hours.
func TestDiarizeSpeakerMixesAReconnectWithoutASecondTrackBuffer(t *testing.T) {
	const seconds = 60
	mkvPath := filepath.Join(t.TempDir(), "reconnect.mkv")
	if err := runMediaCommand("ffmpeg", "-y", "-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=500:sample_rate=48000:duration=60",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=60",
		"-map", "0:a:0", "-map", "1:a:0",
		"-metadata:s:a:0", "participant_id=user-alice",
		"-metadata:s:a:1", "participant_id=user-alice",
		"-c:a", "libopus", mkvPath,
	); err != nil {
		t.Fatalf("create reconnect meeting: %v", err)
	}
	streams, _, err := ProbeMKV(mkvPath)
	if err != nil || len(streams) != 2 || streams[0].SpeakerID != streams[1].SpeakerID {
		t.Fatalf("ProbeMKV() = %+v, %v; want two streams of one participant", streams, err)
	}

	origDiarize := diarizeFn
	t.Cleanup(func() { diarizeFn = origDiarize })
	var diarized []float32
	diarizeFn = func(_ DiarizationModel, samples []float32, _, _ int) ([]SpeakerTurn, error) {
		diarized = samples
		return nil, nil
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	set, err := DiarizeSpeaker(context.Background(), mkvPath, streams[0].SpeakerID, DiarizationModel{Name: "m", SHA256: "x"}, DefaultDiarizationThreads)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("DiarizeSpeaker() error = %v", err)
	}
	if len(set.Streams) != 2 {
		t.Fatalf("streams = %v, want both connections", set.Streams)
	}
	track := uint64(seconds * 16000 * 4)
	if len(diarized) < seconds*16000*99/100 {
		t.Fatalf("diarized %d samples, want about %d", len(diarized), seconds*16000)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > track*3/2 {
		t.Fatalf("diarizing a reconnecting participant allocated %d bytes; one %ds track is %d: a later stream got a buffer of its own", got, seconds, track)
	}
}

// The decode adds into the mix it is given, in place, and grows it only
// where the new stream runs past its end.
func TestAddPCM16LEFloatsSumsIntoTheGivenMix(t *testing.T) {
	mix := make([]float32, 2, 4)
	mix[0], mix[1] = 0.25, 0.25
	raw := []byte{
		0x00, 0x20, // 0.25
		0x00, 0xe0, // -0.25
		0x00, 0x40, // 0.5
	}
	got, err := addPCM16LEFloats(bytes.NewReader(raw), mix, 0)
	if err != nil {
		t.Fatalf("addPCM16LEFloats() error = %v", err)
	}
	if want := []float32{0.5, 0, 0.5}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("mix = %v, want %v", got, want)
	}
	if &got[0] != &mix[0] {
		t.Fatal("the mix was copied; a later stream must be added in place")
	}
}
