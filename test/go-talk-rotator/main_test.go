package main

import (
	"sort"
	"testing"
)

func TestHandleSignalingEventRoomAudience(t *testing.T) {
	b := newBot(&botConfig{Index: 1})
	b.signalingSessionID = "self-session"

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
	b.signalingSessionID = "self-session"
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

func TestResolveCallSID(t *testing.T) {
	tests := []struct {
		name        string
		msgType     string
		msgSID      string
		activeSID   string
		wantSID     string
		wantAccept  bool
		wantChanged bool
	}{
		{
			name:        "offer switches sid",
			msgType:     "offer",
			msgSID:      "sid-new",
			activeSID:   "sid-old",
			wantSID:     "sid-new",
			wantAccept:  true,
			wantChanged: true,
		},
		{
			name:        "offer with empty sid keeps active sid",
			msgType:     "offer",
			msgSID:      "",
			activeSID:   "sid-old",
			wantSID:     "sid-old",
			wantAccept:  true,
			wantChanged: false,
		},
		{
			name:        "candidate stale sid rejected",
			msgType:     "candidate",
			msgSID:      "sid-new",
			activeSID:   "sid-old",
			wantSID:     "sid-old",
			wantAccept:  false,
			wantChanged: false,
		},
		{
			name:        "candidate no sid uses active",
			msgType:     "candidate",
			msgSID:      "",
			activeSID:   "sid-old",
			wantSID:     "sid-old",
			wantAccept:  true,
			wantChanged: false,
		},
		{
			name:        "answer with no active sid accepts msg sid",
			msgType:     "answer",
			msgSID:      "sid-msg",
			activeSID:   "",
			wantSID:     "sid-msg",
			wantAccept:  true,
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSID, gotAccept, gotChanged := resolveCallSID(tt.msgType, tt.msgSID, tt.activeSID)
			if gotSID != tt.wantSID {
				t.Fatalf("sid mismatch: got=%q want=%q", gotSID, tt.wantSID)
			}
			if gotAccept != tt.wantAccept {
				t.Fatalf("accept mismatch: got=%t want=%t", gotAccept, tt.wantAccept)
			}
			if gotChanged != tt.wantChanged {
				t.Fatalf("changed mismatch: got=%t want=%t", gotChanged, tt.wantChanged)
			}
		})
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
