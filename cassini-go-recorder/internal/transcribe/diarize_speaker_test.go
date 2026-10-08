package transcribe

import (
	"context"
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
	origExtract, origDiarize := extractSpeakerFloatsFn, diarizeFn
	t.Cleanup(func() { extractSpeakerFloatsFn, diarizeFn = origExtract, origDiarize })
	extractSpeakerFloatsFn = func(string, AudioStream) ([]float32, error) { return decoded, nil }
	var got []float32
	diarizeFn = func(_ DiarizationModel, samples []float32, _ int) ([]SpeakerTurn, error) {
		got = samples
		return []SpeakerTurn{{StartMS: 0, EndMS: 500, Speaker: 0}}, nil
	}

	set, err := DiarizeSpeaker(context.Background(), mkvPath, speakerID, DiarizationModel{Name: "m", SHA256: "x"})
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
