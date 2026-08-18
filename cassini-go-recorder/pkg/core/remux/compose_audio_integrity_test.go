package remux

// These tests cover an audio-integrity contract the elapsed-time-only
// TestComposeSingleTrackMKVPreservesSparseAudioTimeline check does not:
// when RTP packets land with a real wall-clock gap between speaking
// turns (e.g. a Talk-rotator bot is muted between its slots), the
// decoded PCM from the composed MKV must still contain the actual audio
// content at the correct wall-clock positions, with silence (not
// dropped frames, not stretched samples, not "Invalid audio PTS" jumps)
// in between.
//
// The previous bug surfaced like this: per-participant tracks of a real
// Talk recording transcribed to 0 words even though the spectrogram
// showed speech-like patterns, and `mpv recording.mkv` printed
// `Invalid audio PTS: 5.185 -> 29.563` and reset playback at every
// turn-taking gap. A test that asserted only elapsed-time did not catch
// it because the envelope was technically correct — the audio inside
// the envelope was misplaced.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"

	"gocassini/pkg/core/depacket"
	"gocassini/pkg/core/store"
)

// TestComposeSparseAudioKeepsToneAtCorrectTimelinePositions exercises the
// full per-participant pipeline that builds and decodes recording.mkv
// streams (writeSparseRTPLog → WriteElementaryFromRTPLog →
// composeSingleTrackMKV → ffmpeg decode with the same
// aresample=async=1:first_pts=0 filter that ExtractSpeakerFloats uses)
// and asserts that two bursts of real Opus tone separated by a 4-second
// silent gap survive intact: the tone is loud at 0–1s and at 5–7s, and
// silent at 2–5s, on the decoded WAV's wall-clock timeline.
func TestComposeSparseAudioKeepsToneAtCorrectTimelinePositions(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}

	tmp := t.TempDir()

	// 1. Generate real Opus content (a 440Hz tone) we can verify in the
	//    decoded WAV. Use voip-tuned 20ms frames so each RTP packet
	//    advances by exactly 960 samples at 48kHz, matching what pion's
	//    oggwriter expects on the other side of WriteElementaryFromRTPLog.
	toneOggPath := filepath.Join(tmp, "tone.ogg")
	mustEncodeTone(t, toneOggPath, 3.0)
	tonePackets := mustReadOpusPackets(t, toneOggPath)
	if len(tonePackets) < 100 {
		t.Fatalf("expected >=100 opus packets for 3s tone, got %d", len(tonePackets))
	}

	// 2. Stage two RTP bursts with an explicit recv-time gap.
	//    Burst A: 50 packets covering 0–1s of the tone, recv 0–1s.
	//    Gap:     4s of wall-clock with no RTP traffic.
	//    Burst B: 100 packets covering the next 2s of the tone, recv 5–7s.
	//    This mirrors the bot-rotation pattern: the bot's outgoing track
	//    stops emitting samples during its muted slot, so the recorder
	//    sees clean opus frames bracketing a real-time gap.
	logPath := filepath.Join(tmp, "audio.rtplog")
	writer, err := store.NewWriter(logPath, store.StreamHeader{
		StreamID:    "s_000001",
		Codec:       "audio/opus",
		ClockRate:   48_000,
		Direction:   "recvonly",
		StartMonoNS: 0,
		PT:          111,
	})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	const frameTicks = 960 // 20ms at 48kHz
	const frameNS = uint64(20_000_000)
	burstAFrames := 50
	burstBFrames := 100
	if burstBFrames > len(tonePackets)-burstAFrames {
		burstBFrames = len(tonePackets) - burstAFrames
	}

	var (
		ssrc       uint32 = 77
		seq        uint16 = 1
		rtpTS      uint32 = 0
		baseRecvNS uint64 = 1_000_000_000
	)

	writeBurst := func(packets [][]byte, recvStartNS uint64) {
		t.Helper()
		for _, payload := range packets {
			pkt := rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    111,
					SequenceNumber: seq,
					Timestamp:      rtpTS,
					SSRC:           ssrc,
					Marker:         seq == 1,
				},
				Payload: payload,
			}
			raw, err := pkt.Marshal()
			if err != nil {
				t.Fatalf("marshal rtp: %v", err)
			}
			if err := writer.Write(store.Record{
				RecvMonoNS: recvStartNS,
				Kind:       store.KindRTP,
				WireBytes:  raw,
			}); err != nil {
				t.Fatalf("write rtp record: %v", err)
			}
			seq++
			rtpTS += frameTicks
			recvStartNS += frameNS
		}
	}

	writeBurst(tonePackets[:burstAFrames], baseRecvNS)
	// Burst B starts 5s after Burst A's start (4s silent gap after Burst A ends at 1s).
	writeBurst(tonePackets[burstAFrames:burstAFrames+burstBFrames], baseRecvNS+5_000_000_000)
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	// 3. Run the recorder's compose pipeline.
	oggPath := filepath.Join(tmp, "audio.ogg")
	if _, err := depacket.WriteElementaryFromRTPLog(logPath, "audio/opus", 48_000, oggPath); err != nil {
		t.Fatalf("write elementary: %v", err)
	}
	mkvPath := filepath.Join(tmp, "audio.mkv")
	if err := composeSingleTrackMKV(oggPath, mkvPath, "audio"); err != nil {
		t.Fatalf("compose mkv: %v", err)
	}

	// 4. Decode the MKV with the exact filter chain ExtractSpeakerFloats
	//    uses so a test failure points at the same path the recognizer
	//    sees in production.
	wavPath := filepath.Join(tmp, "decoded.wav")
	runFFmpegOrFatal(t,
		"-y", "-v", "error",
		"-i", mkvPath,
		"-af", "aresample=async=1:first_pts=0",
		"-ac", "1", "-ar", "16000",
		"-c:a", "pcm_s16le",
		wavPath,
	)

	// 5. Verify content at the expected wall-clock positions on the
	//    decoded WAV. The two bursts must remain audibly loud and the
	//    gap between them must remain audibly silent. Generous dB
	//    margins below — speech-recognition cares about the timeline
	//    placement, not exact levels.
	const (
		loudThresholdDB  = -30.0
		silentCeilingDB  = -50.0
		samplesPerSecond = 16_000
	)
	samples, err := readWAVMono(wavPath)
	if err != nil {
		t.Fatalf("read decoded wav: %v", err)
	}
	durSec := float64(len(samples)) / samplesPerSecond
	if durSec < 6.5 || durSec > 8.0 {
		t.Fatalf("decoded duration %.2fs not in 6.5-8.0s; the gap collapsed or stretched", durSec)
	}

	burstADB := rmsDB(samples, 0.1, 0.9) // skip 100ms of opus pre-skip ramp at each edge
	gapDB := rmsDB(samples, 2.5, 4.5)    // mid-gap, well away from boundary artifacts
	burstBDB := rmsDB(samples, 5.2, 6.8)

	if burstADB <= loudThresholdDB {
		t.Errorf("burst A RMS %.1f dB <= %.1f dB; tone audio lost or shifted off 0-1s window", burstADB, loudThresholdDB)
	}
	if burstBDB <= loudThresholdDB {
		t.Errorf("burst B RMS %.1f dB <= %.1f dB; tone audio lost or shifted off 5-7s window — gap probably collapsed and burst B landed on top of burst A", burstBDB, loudThresholdDB)
	}
	if gapDB >= silentCeilingDB {
		t.Errorf("gap RMS %.1f dB >= %.1f dB; expected silence between 2.5-4.5s, got energy — burst B leaked into the gap or aresample is stretching frames across it", gapDB, silentCeilingDB)
	}

	t.Logf("decoded duration=%.2fs burstA_dB=%.1f gap_dB=%.1f burstB_dB=%.1f", durSec, burstADB, gapDB, burstBDB)
}

// mustEncodeTone generates a mono 48kHz Opus stream of `durationSec`
// seconds of a 440Hz sine wave with 20ms frame duration, written as an
// Opus-in-Ogg file. The voip application + 32kbps bitrate match what
// the harness bot streamer pushes through Talk in practice.
func mustEncodeTone(t *testing.T, outPath string, durationSec float64) {
	t.Helper()
	runFFmpegOrFatal(t,
		"-y", "-v", "error",
		"-f", "lavfi",
		"-i", fmt.Sprintf("sine=frequency=440:duration=%g:sample_rate=48000", durationSec),
		"-c:a", "libopus",
		"-b:a", "32k",
		"-application", "voip",
		"-frame_duration", "20",
		"-ac", "1",
		"-ar", "48000",
		outPath,
	)
}

// mustReadOpusPackets returns each Opus packet payload from an
// Opus-in-Ogg file. libopus packs many 20ms frames into each Ogg page
// via the segment table, so we parse the segment table manually to
// recover individual packets — pion's oggreader.ParseNextPage only
// surfaces concatenated page payloads.
func mustReadOpusPackets(t *testing.T, path string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ogg: %v", err)
	}
	var packets [][]byte
	var partial []byte
	pos := 0
	for pos+27 <= len(data) {
		if string(data[pos:pos+4]) != "OggS" {
			t.Fatalf("bad ogg signature at offset %d", pos)
		}
		segmentsCount := int(data[pos+26])
		segTableEnd := pos + 27 + segmentsCount
		if segTableEnd > len(data) {
			t.Fatalf("truncated ogg segment table at offset %d", pos)
		}
		segments := data[pos+27 : segTableEnd]
		payloadStart := segTableEnd
		payloadCursor := payloadStart
		for _, segSize := range segments {
			seg := data[payloadCursor : payloadCursor+int(segSize)]
			partial = append(partial, seg...)
			payloadCursor += int(segSize)
			if segSize < 255 {
				// Last segment of a packet — flush.
				if !bytes.HasPrefix(partial, []byte("OpusHead")) && !bytes.HasPrefix(partial, []byte("OpusTags")) {
					out := make([]byte, len(partial))
					copy(out, partial)
					packets = append(packets, out)
				}
				partial = partial[:0]
			}
		}
		pos = payloadCursor
	}
	return packets
}

func runFFmpegOrFatal(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg %v failed: %v\n%s", args, err, string(out))
	}
}

// readWAVMono returns mono float32 samples in [-1, 1] from a 16kHz
// pcm_s16le WAV file produced by the ffmpeg invocation above. Only
// supports the WAV variant emitted by that pipeline (no chunk hunting).
func readWAVMono(path string) ([]float32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, fmt.Errorf("not a WAV: %x", data[:min(16, len(data))])
	}
	// Walk the chunks to find "data".
	pos := 12
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		pos += 8
		if id == "data" {
			if pos+size > len(data) {
				size = len(data) - pos
			}
			samples := make([]float32, size/2)
			for i := range samples {
				s := int16(binary.LittleEndian.Uint16(data[pos+i*2 : pos+i*2+2]))
				samples[i] = float32(s) / 32768.0
			}
			return samples, nil
		}
		pos += size
	}
	return nil, fmt.Errorf("no data chunk found in WAV")
}

// rmsDB returns the RMS amplitude in dBFS of the samples between
// startSec and endSec on a 16kHz mono timeline. Returns -inf-equivalent
// (-200) when the window has no energy.
func rmsDB(samples []float32, startSec, endSec float64) float64 {
	const sampleRate = 16000
	startIdx := int(startSec * sampleRate)
	endIdx := int(endSec * sampleRate)
	if startIdx < 0 {
		startIdx = 0
	}
	if endIdx > len(samples) {
		endIdx = len(samples)
	}
	if endIdx <= startIdx {
		return -200
	}
	var sumSq float64
	for _, s := range samples[startIdx:endIdx] {
		sumSq += float64(s) * float64(s)
	}
	mean := sumSq / float64(endIdx-startIdx)
	if mean <= 0 {
		return -200
	}
	return 10 * math.Log10(mean)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
