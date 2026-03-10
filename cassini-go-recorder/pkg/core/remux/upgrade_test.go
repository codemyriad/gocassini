package remux

import "testing"

func TestBuildLegacyUpgradeSessionSynthesizesPortableSession(t *testing.T) {
	streams := []probedMeetingStream{
		{
			Index:     0,
			CodecType: "video",
			Tags: map[string]string{
				"STREAM_ID":        "s_000001",
				"PARTICIPANT_ID":   "alice",
				"PARTICIPANT_NAME": "Alice",
				"title":            "Alice",
			},
		},
		{
			Index:     1,
			CodecType: "audio",
			Tags: map[string]string{
				"STREAM_ID":        "s_000002",
				"PARTICIPANT_ID":   "alice",
				"PARTICIPANT_NAME": "Alice",
				"title":            "Alice",
			},
		},
	}
	plans := map[string]StreamPlan{
		"s_000001": {
			StreamID:         "s_000001",
			LTID:             "p:alice:video:janus",
			Kind:             "video",
			Codec:            "video/vp8",
			ParticipantID:    "alice",
			ParticipantName:  "Alice",
			ClockRate:        90000,
			PT:               96,
			FirstRecvNS:      100,
			TimelineAdjustNS: 250000000,
		},
		"s_000002": {
			StreamID:         "s_000002",
			LTID:             "p:alice:audio:janus",
			Kind:             "audio",
			Codec:            "audio/opus",
			ParticipantID:    "alice",
			ParticipantName:  "Alice",
			ClockRate:        48000,
			PT:               111,
			FirstRecvNS:      80,
			TimelineAdjustNS: 0,
		},
	}

	upgraded, sess, err := buildLegacyUpgradeSession(streams, plans, "sess-1", "2026-03-10T12:00:00Z", "recorder")
	if err != nil {
		t.Fatalf("buildLegacyUpgradeSession: %v", err)
	}
	if len(upgraded) != 2 {
		t.Fatalf("upgraded streams len: got=%d want=2", len(upgraded))
	}
	if sess.SessionID != "sess-1" {
		t.Fatalf("session id mismatch: got=%q", sess.SessionID)
	}
	if sess.StartedMonoNS != 80 {
		t.Fatalf("started mono ns mismatch: got=%d want=80", sess.StartedMonoNS)
	}
	if len(sess.Participants) != 1 || sess.Participants[0].PID != "alice" {
		t.Fatalf("participants mismatch: %+v", sess.Participants)
	}
	if len(sess.LogicalTracks) != 2 {
		t.Fatalf("logical track count mismatch: got=%d want=2", len(sess.LogicalTracks))
	}
	if len(sess.PacketStreams) != 2 {
		t.Fatalf("packet stream count mismatch: got=%d want=2", len(sess.PacketStreams))
	}
	if upgraded[0].sourceIndex != 0 || upgraded[1].sourceIndex != 1 {
		t.Fatalf("source indices mismatch: %+v", upgraded)
	}
	if upgraded[0].kindSpecifier() != "v" || upgraded[1].kindSpecifier() != "a" {
		t.Fatalf("kind specifiers mismatch: %+v", upgraded)
	}
}

func TestBuildLegacyUpgradeSessionRejectsMissingPlan(t *testing.T) {
	streams := []probedMeetingStream{
		{
			Index:     0,
			CodecType: "video",
			Tags: map[string]string{
				"STREAM_ID": "s_000001",
			},
		},
	}

	_, _, err := buildLegacyUpgradeSession(streams, map[string]StreamPlan{}, "sess-1", "", "")
	if err == nil {
		t.Fatalf("expected missing plan error")
	}
}
