package transcribe

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"gocassini/pkg/core/remux"
	"gocassini/pkg/core/session"
	"gocassini/pkg/core/store"
)

type captureBurst struct{ start, end float64 }
type captureTone struct {
	pid       string
	frequency int
	bursts    []captureBurst
}

// Compare the actual source remux, recognizer decode and published Opus mix.
// Removing an earlier camera changes one shared origin, never speaker offsets.
func TestCaptureModesPreserveDecodedSoundPlacement(t *testing.T) {
	requireFFMediaTools(t)
	cases := []struct {
		name  string
		tones []captureTone
	}{
		{"overlap-gap-rejoin", []captureTone{
			{"alice", 440, []captureBurst{{1, 1.6}, {4, 4.8}}},
			{"bob", 880, []captureBurst{{2.4, 3}, {4.2, 5}, {9.4, 10}}},
			{"alice", 440, []captureBurst{{8, 8.6}}},
		}},
		{"single-segment", []captureTone{{"alice", 440, []captureBurst{{1, 1.6}, {4, 4.8}}}}},
		{"sparse-95-seconds", []captureTone{
			{"alice", 440, []captureBurst{{1, 1.6}, {45, 45.6}, {94, 94.6}}},
			{"bob", 880, []captureBurst{{2.4, 3}, {47, 47.6}, {95, 95.6}}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			controlTracks := map[int][]captureBurst{}
			controlMix := map[int][]captureBurst{}
			for _, video := range []bool{true, false} {
				t.Run(fmt.Sprintf("video=%t", video), func(t *testing.T) {
					dir := t.TempDir()
					mkv := buildCaptureTimingSource(t, dir, tc.tones, video)
					streams, _, err := ProbeMKV(mkv)
					if err != nil {
						t.Fatal(err)
					}
					if len(streams) != len(tc.tones) {
						t.Fatalf("audio tracks=%d want %d", len(streams), len(tc.tones))
					}
					origin := 1.0
					if video {
						origin = 0
					}
					// Use metadata to identify independent speakers and the rotated stream.
					for i, s := range streams {
						if s.ParticipantID != tc.tones[i].pid {
							t.Fatalf("participant metadata=%q want %q", s.ParticipantID, tc.tones[i].pid)
						}
						samples, err := ExtractSpeakerFloats(mkv, s)
						if err != nil {
							t.Fatal(err)
						}
						got := captureToneWindows(samples, tc.tones[i].frequency)
						if err := checkCaptureWindowsTolerance(got, tc.tones[i].bursts, origin, 0.1); err != nil {
							t.Fatalf("speaker %d: %v", i, err)
						}
						if video {
							controlTracks[i] = got
						} else if err := checkCaptureWindows(got, controlTracks[i], origin); err != nil {
							t.Fatalf("speaker %d A/B: %v", i, err)
						}
					}
					mix := filepath.Join(dir, "meeting.webm")
					enc, err := ChooseMeetingAudioEncode(mkv, streams, "")
					if err != nil {
						t.Fatal(err)
					}
					if err := MixDownToWebM(mkv, streams, mix, enc); err != nil {
						t.Fatal(err)
					}
					mixed, err := ExtractMixedFloats(mix)
					if err != nil {
						t.Fatal(err)
					}
					for _, frequency := range []int{440, 880} {
						var want []captureBurst
						for _, tone := range tc.tones {
							if tone.frequency == frequency {
								want = append(want, tone.bursts...)
							}
						}
						if len(want) == 0 {
							continue
						}
						got := captureToneWindows(mixed, frequency)
						if err := checkCaptureWindowsTolerance(got, want, origin, 0.1); err != nil {
							t.Fatalf("published %dHz: %v", frequency, err)
						}
						if video {
							controlMix[frequency] = got
						} else if err := checkCaptureWindows(got, controlMix[frequency], origin); err != nil {
							t.Fatalf("published %dHz A/B: %v", frequency, err)
						}
					}
				})
			}
		})
	}
}

// The sound oracle must reject the errors duration/packet-count checks miss.
func TestCaptureSoundOracleRejectsTimelineMutations(t *testing.T) {
	want := []captureBurst{{1, 1.6}, {45, 45.6}, {94, 94.6}}
	for name, got := range map[string][]captureBurst{
		"per-track-offset": {{1.2, 1.8}, {45.2, 45.8}, {94.2, 94.8}},
		"collapsed-gap":    {{1, 1.6}, {1.6, 2.2}, {2.2, 2.8}},
		"scaled-drift":     {{1, 1.6}, {45.45, 46.05}, {94.94, 95.54}},
	} {
		t.Run(name, func(t *testing.T) {
			if checkCaptureWindows(got, want, 0) == nil {
				t.Fatal("mutation survived")
			}
		})
	}
}

func checkCaptureWindows(got, want []captureBurst, origin float64) error {
	return checkCaptureWindowsTolerance(got, want, origin, 0.04)
}

// Reconstructed Opus pre-skip varies with the media-tool version; bound
// fixture placement to 100ms, then compare decoded modes within 40ms.
func checkCaptureWindowsTolerance(got, want []captureBurst, origin, tolerance float64) error {
	if len(got) != len(want) {
		return fmt.Errorf("tone windows=%v want=%v shared origin=%.3f", got, want, origin)
	}
	// Rotated tracks are returned in stream order; align by actual wall time.
	want = append([]captureBurst(nil), want...)
	for i := range want {
		for j := i + 1; j < len(want); j++ {
			if want[j].start < want[i].start {
				want[i], want[j] = want[j], want[i]
			}
		}
	}
	for i := range got {
		if math.Abs(got[i].start-(want[i].start-origin)) > tolerance+0.000001 || math.Abs(got[i].end-(want[i].end-origin)) > tolerance+0.000001 {
			return fmt.Errorf("tone windows=%v want=%v shared origin=%.3f tolerance=%.3fs", got, want, origin, tolerance)
		}
	}
	return nil
}

// Frequency-selective 20ms windows detect both speakers even during overlap.
func captureToneWindows(samples []float32, frequency int) []captureBurst {
	const frame = 320
	var windows []captureBurst
	active := false
	for pos := 0; pos+frame <= len(samples); pos += frame {
		var re, im float64
		for j, s := range samples[pos : pos+frame] {
			phase := 2 * math.Pi * float64(frequency*j) / 16000
			re += float64(s) * math.Cos(phase)
			im += float64(s) * math.Sin(phase)
		}
		present := 2*math.Hypot(re, im)/frame > 0.015
		sec := float64(pos) / 16000
		if present && !active {
			windows = append(windows, captureBurst{start: sec})
		}
		if !present && active {
			windows[len(windows)-1].end = sec
		}
		active = present
	}
	if active {
		windows[len(windows)-1].end = float64(len(samples)/frame*frame) / 16000
	}
	return windows
}

func buildCaptureTimingSource(t *testing.T, dir string, tones []captureTone, video bool) string {
	t.Helper()
	const base = uint64(10_000_000_000)
	streamsDir := filepath.Join(dir, "streams")
	if err := os.MkdirAll(streamsDir, 0755); err != nil {
		t.Fatal(err)
	}
	sess := session.Session{Version: session.SchemaVersion, SessionID: "timing", StartedMonoNS: base, CaptureMode: "audio-only"}
	write := func(kind, pid, codec string, clock uint32, bursts []captureBurst, payloads [][]byte) {
		id := fmt.Sprintf("s_%06d", len(sess.PacketStreams)+1)
		ltid := pid + ":" + kind
		stream := session.PacketStream{StreamID: id, LTID: ltid, Codec: codec, ClockRate: clock, PT: 111, PrimarySSRC: uint32(len(sess.PacketStreams) + 1), StartMonoNS: base + uint64(bursts[0].start*1e9)}
		rotated := false
		for _, previous := range sess.PacketStreams {
			if previous.LTID == ltid {
				rotated = true
			}
		}
		if rotated {
			stream.PT = 112
		}
		sess.PacketStreams = append(sess.PacketStreams, stream)
		if !rotated {
			sess.LogicalTracks = append(sess.LogicalTracks, session.LogicalTrack{LTID: ltid, Kind: kind, ParticipantID: pid})
			sess.Participants = append(sess.Participants, session.Participant{PID: pid, Display: pid})
		}
		w, err := store.NewWriter(filepath.Join(streamsDir, id+".rtplog"), store.StreamHeader{StreamID: id, Codec: codec, ClockRate: clock, StartMonoNS: stream.StartMonoNS, PT: stream.PT})
		if err != nil {
			t.Fatal(err)
		}
		seq := uint16(65520)
		ts := uint32(0xffff0000)
		for _, b := range bursts {
			step := 0.02
			if kind == "video" {
				step = 0.1
			}
			for i := 0; i < int(math.Round((b.end-b.start)/step)); i++ {
				raw, err := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: stream.PT, SequenceNumber: seq, Timestamp: ts, SSRC: stream.PrimarySSRC, Marker: true}, Payload: payloads[i%len(payloads)]}).Marshal()
				if err != nil {
					t.Fatal(err)
				}
				if err := w.Write(store.Record{RecvMonoNS: base + uint64((b.start+float64(i)*step)*1e9), Kind: store.KindRTP, WireBytes: raw}); err != nil {
					t.Fatal(err)
				}
				seq++
				ts += uint32(float64(clock) * step)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, tone := range tones {
		ogg := filepath.Join(dir, fmt.Sprintf("tone-%d.ogg", tone.frequency))
		if err := runMediaCommand("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:duration=1:sample_rate=48000", tone.frequency), "-c:a", "libopus", "-application", "voip", "-frame_duration", "20", "-ac", "1", ogg); err != nil {
			t.Fatal(err)
		}
		write("audio", tone.pid, "audio/opus", 48000, tone.bursts, captureOpusPackets(t, ogg))
	}
	if video {
		sess.CaptureMode = "audio-video"
		ivf := filepath.Join(dir, "camera.ivf")
		if err := runMediaCommand("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "color=size=32x32:rate=10:duration=0.5", "-c:v", "libvpx", "-g", "1", "-f", "ivf", ivf); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(ivf)
		if err != nil {
			t.Fatal(err)
		}
		var payloads [][]byte
		payloader := codecs.VP8Payloader{}
		for pos := 32; pos+12 <= len(data); {
			size := int(binary.LittleEndian.Uint32(data[pos : pos+4]))
			pos += 12
			pkts := payloader.Payload(1200, data[pos:pos+size])
			if len(pkts) != 1 {
				t.Fatal("fixture frame must fit in one RTP packet")
			}
			payloads = append(payloads, pkts[0])
			pos += size
		}
		write("video", "camera", "video/vp8", 90000, []captureBurst{{0, 0.5}}, payloads)
	}
	body, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(dir, "session.json")
	if err := os.WriteFile(index, body, 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "recording.mkv")
	if _, err := remux.BuildFromSession(index, out, remux.BuildOptions{StrictCodecs: true}); err != nil {
		t.Fatal(err)
	}
	return out
}

func captureOpusPackets(t *testing.T, path string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var packets [][]byte
	var partial []byte
	for pos := 0; pos+27 <= len(data); {
		n := int(data[pos+26])
		cursor := pos + 27 + n
		for _, size := range data[pos+27 : cursor] {
			partial = append(partial, data[cursor:cursor+int(size)]...)
			cursor += int(size)
			if size < 255 {
				if !bytes.HasPrefix(partial, []byte("OpusHead")) && !bytes.HasPrefix(partial, []byte("OpusTags")) {
					packets = append(packets, bytes.Clone(partial))
				}
				partial = partial[:0]
			}
		}
		pos = cursor
	}
	return packets
}
