package transcribe

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// DiarizeSpeaker runs the model with the threads it is given and records them
// in the turn set, so a turn set says how it was measured.
func TestDiarizeSpeakerRunsWithTheThreadsItIsGiven(t *testing.T) {
	mkvPath := buildParticipantMetadataMeeting(t, t.TempDir())
	streams, _, err := ProbeMKV(mkvPath)
	if err != nil {
		t.Fatalf("ProbeMKV() error = %v", err)
	}
	origDiarize := diarizeFn
	t.Cleanup(func() { diarizeFn = origDiarize })
	var gotThreads int
	diarizeFn = func(_ DiarizationModel, _ []float32, _, threads int) ([]SpeakerTurn, error) {
		gotThreads = threads
		return []SpeakerTurn{{StartMS: 0, EndMS: 500, Speaker: 0}}, nil
	}

	set, err := DiarizeSpeaker(context.Background(), mkvPath, streams[0].SpeakerID, DiarizationModel{Name: "m", SHA256: "x"}, 5)

	if err != nil {
		t.Fatalf("DiarizeSpeaker() error = %v", err)
	}
	if gotThreads != 5 || set.Params.Threads != 5 {
		t.Errorf("model ran with %d threads, turn set records %d; want 5 and 5", gotThreads, set.Params.Threads)
	}
}

// The showcase meeting's committed tracks of two synthetic voices, Mira and
// Leo, each on its own track with silence where the other speaks.
var showcaseVoiceTracks = []string{
	"../../../harness/media/processed/showcase-lantern-festival-v1/mira.ogg",
	"../../../harness/media/processed/showcase-lantern-festival-v1/leo.ogg",
}

// The real model on real speech: two people mixed onto one participant's
// track, as around a shared laptop, come out as two voices. It runs only
// where the Nemotron runtime and a model are present ($CASSINI_DIARIZATION_MODEL,
// or a model store at $CASSINI_CACHE_ROOT with the diarizer installed) and
// the showcase tracks were fetched from LFS; the image smoke
// (harness/bin/ci-transcribe-smoke-exapp.sh) does the same in CI.
func TestDiarizeSpeakerSeparatesTwoRealVoicesOnOneTrack(t *testing.T) {
	requireFFMediaTools(t)
	if !HasDiarizationRuntime() {
		t.Skip("native runtime without Nemotron diarization")
	}
	model, err := ResolveDiarizationModel(os.Getenv("CASSINI_CACHE_ROOT"))
	if errors.Is(err, ErrDiarizationUnavailable) {
		t.Skipf("no diarization model: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, track := range showcaseVoiceTracks {
		head := make([]byte, 4)
		f, err := os.Open(track)
		if err != nil {
			t.Skipf("showcase track missing: %v", err)
		}
		_, _ = f.Read(head)
		f.Close()
		if !bytes.Equal(head, []byte("OggS")) {
			t.Skipf("%s is a Git LFS pointer; run git lfs pull", filepath.Base(track))
		}
	}
	// The first 45 s: Mira speaks at 0.9 s and 33.8 s, Leo at 5.0 s and 39.9 s.
	mkvPath := filepath.Join(t.TempDir(), "shared-laptop.mkv")
	if err := runMediaCommand("ffmpeg", "-nostdin", "-v", "error", "-y",
		"-i", showcaseVoiceTracks[0], "-i", showcaseVoiceTracks[1],
		"-filter_complex", "[0:a][1:a]amix=inputs=2:duration=longest:normalize=0,atrim=0:45",
		"-ac", "1", "-ar", "16000", "-c:a", "flac",
		"-metadata:s:a:0", "participant_id=room-laptop", "-metadata:s:a:0", "participant_name=Room laptop",
		mkvPath); err != nil {
		t.Fatal(err)
	}
	streams, _, err := ProbeMKV(mkvPath)
	if err != nil || len(streams) != 1 {
		t.Fatalf("ProbeMKV() = %+v, %v", streams, err)
	}

	set, err := DiarizeSpeaker(context.Background(), mkvPath, streams[0].SpeakerID, model, DefaultDiarizationThreads)

	if err != nil {
		t.Fatalf("DiarizeSpeaker() error = %v", err)
	}
	if err := set.Check(); err != nil {
		t.Fatal(err)
	}
	voices := map[int]bool{}
	for _, turn := range set.Turns {
		voices[turn.Speaker] = true
	}
	if len(voices) < 2 {
		t.Fatalf("found %d voices in %+v, want at least 2", len(voices), set.Turns)
	}
	t.Logf("%d turns, %d voices, %d ms", len(set.Turns), len(voices), set.ElapsedMS)
}
