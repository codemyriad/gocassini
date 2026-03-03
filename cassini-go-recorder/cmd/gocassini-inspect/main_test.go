package main

import (
	"os"
	"path/filepath"
	"testing"

	"gocassini/pkg/core/session"
)

func TestSummarizeSegmentChurn(t *testing.T) {
	entries := []inspectedSessionStream{
		{
			packet: session.PacketStream{
				StreamID:    "s_1",
				LTID:        "p:alice:video:mid1",
				PrimarySSRC: 1001,
				PT:          96,
				StartMonoNS: 1_000_000_000,
			},
			summary: streamSummary{first: 1_000_000_000, last: 2_000_000_000},
		},
		{
			packet: session.PacketStream{
				StreamID:    "s_2",
				LTID:        "p:alice:video:mid1",
				PrimarySSRC: 1002,
				PT:          96,
				StartMonoNS: 2_200_000_000,
			},
			summary: streamSummary{first: 2_200_000_000, last: 3_200_000_000},
		},
		{
			packet: session.PacketStream{
				StreamID:    "s_3",
				LTID:        "p:alice:video:mid1",
				PrimarySSRC: 1002,
				PT:          97,
				StartMonoNS: 3_300_000_000,
			},
			summary: streamSummary{first: 3_300_000_000, last: 3_900_000_000},
		},
	}

	got := summarizeSegmentChurn(entries)
	if got.Segments != 3 {
		t.Fatalf("segments: got=%d want=3", got.Segments)
	}
	if got.SSRCChanges != 1 {
		t.Fatalf("ssrc changes: got=%d want=1", got.SSRCChanges)
	}
	if got.PTChanges != 1 {
		t.Fatalf("pt changes: got=%d want=1", got.PTChanges)
	}
	if got.MaxGapNS != 200_000_000 {
		t.Fatalf("max gap: got=%d want=%d", got.MaxGapNS, uint64(200_000_000))
	}
	if got.FirstNS != 1_000_000_000 || got.LastNS != 3_900_000_000 {
		t.Fatalf("time bounds mismatch: first=%d last=%d", got.FirstNS, got.LastNS)
	}
}

func TestStreamCloseReasons(t *testing.T) {
	tmp := t.TempDir()
	eventsPath := filepath.Join(tmp, "events.ndjson")
	body := []byte(
		`{"type":"stream_opened","stream_id":"s_1"}` + "\n" +
			`{"type":"stream_closed","stream_id":"s_1","reason":"segment-rotate:ssrc:1->2"}` + "\n" +
			`{"type":"stream_closed","stream_id":"s_2","reason":"eof"}` + "\n" +
			`{"type":"stream_closed","stream_id":"s_3","reason":"eof"}` + "\n",
	)
	if err := os.WriteFile(eventsPath, body, 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	reasons, err := streamCloseReasons(eventsPath)
	if err != nil {
		t.Fatalf("streamCloseReasons: %v", err)
	}
	if reasons["eof"] != 2 {
		t.Fatalf("expected eof=2, got=%d", reasons["eof"])
	}
	if reasons["segment-rotate:ssrc:1->2"] != 1 {
		t.Fatalf("expected rotate=1, got=%d", reasons["segment-rotate:ssrc:1->2"])
	}
}
