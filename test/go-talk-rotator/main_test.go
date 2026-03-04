package main

import (
	"sort"
	"testing"
)

func TestHandleSignalingEventRoomAudience(t *testing.T) {
	b := newBot(&botConfig{Index: 1})
	b.setSignalingSessionID("self-session")

	b.handleSignalingEvent(map[string]any{
		"event": map[string]any{
			"target": "room",
			"join": []any{
				map[string]any{"sessionid": "self-session"},
				map[string]any{"sessionid": "peer-a"},
			},
			"change": []any{
				map[string]any{"sessionId": "peer-b"},
			},
			"leave": []any{"peer-a"},
		},
	})

	got := b.audienceSnapshot()
	sort.Strings(got)

	want := []string{"peer-b"}
	if !equalStringSlices(got, want) {
		t.Fatalf("audience mismatch: got=%v want=%v", got, want)
	}
}

func TestHandleSignalingEventParticipantsAudience(t *testing.T) {
	b := newBot(&botConfig{Index: 1})
	b.setSignalingSessionID("self-session")
	b.addAudienceSession("stale")

	b.handleSignalingEvent(map[string]any{
		"event": map[string]any{
			"target": "participants",
			"update": map[string]any{
				"users": []any{
					map[string]any{"sessionId": "peer-a", "inCall": 7},
					map[string]any{"sessionId": "peer-b", "inCall": "5"},
					map[string]any{"sessionId": "peer-c", "inCall": 0},
					map[string]any{"sessionId": "peer-internal", "internal": true, "inCall": 7},
					map[string]any{"sessionId": "self-session", "inCall": 7},
				},
			},
		},
	})

	got := b.audienceSnapshot()
	sort.Strings(got)
	want := []string{"peer-a", "peer-b", "stale"}
	if !equalStringSlices(got, want) {
		t.Fatalf("audience mismatch after users update: got=%v want=%v", got, want)
	}

	b.handleSignalingEvent(map[string]any{
		"event": map[string]any{
			"target": "participants",
			"update": map[string]any{
				"all":    true,
				"incall": 0,
			},
		},
	})

	got = b.audienceSnapshot()
	if len(got) != 0 {
		t.Fatalf("expected cleared audience on all/incall=0, got=%v", got)
	}
}

func TestCollectExternalAudienceSessionsExcludesBotSessions(t *testing.T) {
	b1 := newBot(&botConfig{Index: 1})
	b2 := newBot(&botConfig{Index: 2})
	b1.setSignalingSessionID("bot-session-1")
	b2.setSignalingSessionID("bot-session-2")

	b1.addAudienceSession("bot-session-2")
	b1.addAudienceSession("viewer-a")
	b2.addAudienceSession("bot-session-1")
	b2.addAudienceSession("viewer-a")
	b2.addAudienceSession("viewer-b")

	external, knownBots := collectExternalAudienceSessions([]*bot{b1, b2})
	if knownBots != 2 {
		t.Fatalf("known bot session count mismatch: got=%d want=2", knownBots)
	}
	if len(external) != 2 {
		t.Fatalf("external audience count mismatch: got=%d want=2", len(external))
	}
	if _, ok := external["viewer-a"]; !ok {
		t.Fatalf("missing viewer-a in external audience: %+v", external)
	}
	if _, ok := external["viewer-b"]; !ok {
		t.Fatalf("missing viewer-b in external audience: %+v", external)
	}
	if _, ok := external["bot-session-1"]; ok {
		t.Fatalf("bot session should be filtered from external audience")
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
