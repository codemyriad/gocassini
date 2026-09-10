package transcribe

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Two simultaneous microphones belong to one person but require independent
// floors. Distinct tones make overwriting, double playback and cross-session
// contamination measurable in both the published mix and transcription input.
func TestSameOwnerSessionsSpliceIndependently(t *testing.T) {
	requireFFMediaTools(t)
	for _, both := range []bool{false, true} {
		t.Run(strconv.FormatBool(both), func(t *testing.T) {
			specs := twoSpeakerSpecs()
			specs[0].sessionID = "browser-a"
			specs[1].participantID = "alice"
			specs[1].sessionID = "browser-b"
			mkv, streams := spliceFixture(t, specs)
			if streams[0].RemoteSessionID != "browser-a" || streams[1].RemoteSessionID != "browser-b" {
				t.Fatalf("probe lost session identity: %+v", streams)
			}
			if streams[0].SpeakerID != streams[1].SpeakerID {
				t.Fatal("session split changed user attribution")
			}
			root := t.TempDir()
			writeSession := func(sid string, frequency int) {
				segment := syntheticSegmentDelayed(10_000, 10, 1000, 0, 0)
				segment.AudioName = "segment-0.webm"
				segment.SessionID = sid
				writeCaptureAt(t, root, "room1", "alice", segment, 10, frequency)
				dir := filepath.Join(root, "room1", "alice", strconv.FormatInt(segment.StartWallMS, 10))
				if err := os.Rename(dir, dir+"_"+sid); err != nil {
					t.Fatal(err)
				}
			}
			writeSession("browser-a", 1100)
			if both {
				writeSession("browser-b", 1700)
			}
			webm, reports, log := runSplicedBuild(t, mkv, streams, root, "room1", t.TempDir())
			want := 1
			if both {
				want = 2
			}
			if len(reports) != want {
				t.Fatalf("reports=%+v log=%s", reports, log)
			}
			for _, report := range reports {
				if !report.MixSpliced || report.Owner != "alice" || report.SessionID == "" || report.Placed != 1 {
					t.Fatalf("bad evidence: %+v", report)
				}
			}
			const hz = 16000
			inside := sliceSeconds(decodePCM(t, webm, hz), hz, 10.2, 19.8)
			a := tonePower(inside, hz, 1100)
			if dbRatio(a, tonePower(inside, hz, 300)) < 20 {
				t.Fatal("first session's recorded audio was not replaced")
			}
			bFrequency := 900
			if both {
				bFrequency = 1700
			}
			if dbRatio(tonePower(inside, hz, float64(bFrequency)), a) < -15 {
				t.Fatal("second session disappeared from mix")
			}
			first := sliceSeconds(decodePCM(t, streams[0].SourceAudioPath, hz), hz, 10.2, 19.8)
			if dbRatio(tonePower(first, hz, 1100), tonePower(first, hz, float64(bFrequency))) < 20 {
				t.Fatal("first transcription input contains other session")
			}
			if streams[1].SuppressTranscription {
				t.Fatal("first session suppressed the other browser")
			}
			if both {
				if streams[0].SourceAudioPath == streams[1].SourceAudioPath {
					t.Fatal("session renders overwrite each other")
				}
				second := sliceSeconds(decodePCM(t, streams[1].SourceAudioPath, hz), hz, 10.2, 19.8)
				if dbRatio(tonePower(second, hz, 1700), tonePower(second, hz, 1100)) < 20 {
					t.Fatal("second transcription input contains first session")
				}
			} else if streams[1].SourceAudioPath != "" {
				t.Fatal("missing upload replaced the second session")
			}
		})
	}
}

func TestSourceSessionDiscoveryPreservesAdoptedSegments(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "room", "alice", "capture")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeSidecar(t, dir, SourceSidecar{Format: SourceCaptureFormat, OwnerUserID: "alice", RoomToken: "room", CallStartWallMS: 100000, CallEndWallMS: 120000,
		Segments: []SourceSegment{{SessionID: "before-reload", StartWallMS: 100000, StopWallMS: 109000}, {SessionID: "after-reload", StartWallMS: 110000, StopWallMS: 120000}}})
	found, refused, err := DiscoverSourceCaptures(root, "room", 100000, 120000)
	if err != nil || len(refused) != 0 || len(found) != 2 {
		t.Fatalf("discovery=%+v refusals=%+v err=%v", found, refused, err)
	}
	for _, sid := range []string{"before-reload", "after-reload"} {
		if len(found[sourceSessionKey("alice", sid)]) != 1 {
			t.Fatalf("lost %s", sid)
		}
	}
}
