package remux

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gocassini/pkg/core/depacket"
	"gocassini/pkg/core/store"

	"github.com/pion/rtp"
)

func TestComposeSingleTrackMKVPreservesSparseAudioTimeline(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}

	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "audio.rtplog")
	oggPath := filepath.Join(tmp, "audio.ogg")
	mkvPath := filepath.Join(tmp, "audio.mkv")

	if err := writeSparseOpusLog(logPath); err != nil {
		t.Fatalf("write sparse opus log: %v", err)
	}
	if _, err := depacket.WriteElementaryFromRTPLog(logPath, "audio/opus", 48_000, oggPath); err != nil {
		t.Fatalf("write elementary ogg: %v", err)
	}
	if err := composeSingleTrackMKV(oggPath, mkvPath, "audio"); err != nil {
		t.Fatalf("compose single-track mkv: %v", err)
	}

	elapsed, err := probePacketElapsedSeconds(mkvPath)
	if err != nil {
		t.Fatalf("probe packet elapsed: %v", err)
	}
	if elapsed < 19.0 || elapsed > 21.0 {
		t.Fatalf("sparse timeline collapsed: elapsed=%.3fs want~=20s", elapsed)
	}
}

func writeSparseOpusLog(path string) error {
	writer, err := store.NewWriter(path, store.StreamHeader{
		StreamID:    "s_000001",
		Codec:       "audio/opus",
		ClockRate:   48_000,
		Direction:   "recvonly",
		StartMonoNS: 1_000_000_000,
		PT:          111,
	})
	if err != nil {
		return err
	}
	defer func() {
		_ = writer.Close()
	}()

	recvTimes := []uint64{
		1_000_000_000,
		11_000_000_000,
		21_000_000_000,
		31_000_000_000,
	}
	timestamps := []uint32{960, 1_920, 2_880, 3_840}
	seq := uint16(1)
	for idx := range recvTimes {
		packet := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    111,
				SequenceNumber: seq,
				Timestamp:      timestamps[idx],
				SSRC:           77,
				Marker:         true,
			},
			Payload: []byte{0xf8, 0xff, 0xfe},
		}
		seq++
		raw, err := packet.Marshal()
		if err != nil {
			return err
		}
		if err := writer.Write(store.Record{
			RecvMonoNS: recvTimes[idx],
			Kind:       store.KindRTP,
			WireBytes:  raw,
		}); err != nil {
			return err
		}
	}
	return writer.Close()
}

func probePacketElapsedSeconds(path string) (float64, error) {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-show_entries", "packet=pts_time",
		"-of", "csv=p=0",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return 0, fmt.Errorf("no packet timestamps")
	}

	parse := func(raw string) (float64, error) {
		value := strings.TrimSpace(strings.Split(raw, ",")[0])
		return strconv.ParseFloat(value, 64)
	}

	first, err := parse(lines[0])
	if err != nil {
		return 0, fmt.Errorf("parse first pts: %w", err)
	}
	last, err := parse(lines[len(lines)-1])
	if err != nil {
		return 0, fmt.Errorf("parse last pts: %w", err)
	}
	return last - first, nil
}
