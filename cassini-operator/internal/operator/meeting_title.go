package operator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The default meeting title is the Talk room's display name — which, for a
// one-to-one conversation, is just the other person: a Chris–Alex call ends
// up titled "Chris" (D-862). This file composes a better default for exactly
// those rooms: both participants' names, read from the run bundle's
// session.json, the one capture that lists everyone seen on the call rather
// than only those who spoke. Composition is best-effort all the way down —
// any doubt (unknown room type, missing session, not exactly two people)
// falls back to the room name, never to a guessed pairing.

// talkRoomTypeForJob returns the spreed conversation type recorded for a
// job, preferring the live in-memory state and falling back to the persisted
// Talk binding, exactly like talkRoomForJob. Zero means "never resolved".
func (rt *Runtime) talkRoomTypeForJob(jobID string) int {
	if state, ok := rt.lookupTalkJobState(jobID); ok && state.RoomType != 0 {
		return state.RoomType
	}
	job, err := rt.store.GetJob(context.Background(), jobID)
	if err != nil || job.TalkBinding == nil {
		return 0
	}
	state, err := decodeTalkBinding(*job.TalkBinding)
	if err != nil {
		return 0
	}
	return state.RoomType
}

// defaultMeetingTitle picks the title a meeting is sealed with when nobody
// has named it: both participants for a one-to-one room, the room name for
// everything else. runPath is the job's run bundle (session.json lives in
// its session/ directory).
func (rt *Runtime) defaultMeetingTitle(jobID, runPath, roomName string) string {
	if !isOneToOneTalkRoom(rt.talkRoomTypeForJob(jobID)) {
		return roomName
	}
	names := oneToOneParticipantNames(runPath)
	if len(names) != 2 {
		return roomName
	}
	return sanitizeTalkRoomName(names[0] + " & " + names[1])
}

// oneToOneParticipantNames reads the run bundle's session.json and returns
// the distinct human participant names, sorted, or nil when they cannot be
// trusted. Placeholder names (the recorder never learned who someone was)
// and the recorder's own call identity are dropped: a pairing that includes
// "participant-abc123" or "CassiniRecorder" is worse than the room name.
func oneToOneParticipantNames(runPath string) []string {
	if strings.TrimSpace(runPath) == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(runPath, "session", "session.json"))
	if err != nil {
		return nil
	}
	var meta struct {
		Platform struct {
			RecorderIdentity struct {
				Display string `json:"display"`
			} `json:"recorder_identity"`
		} `json:"platform"`
		Participants []struct {
			PID     string `json:"pid"`
			Display string `json:"display"`
		} `json:"participants"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil
	}
	recorder := strings.TrimSpace(meta.Platform.RecorderIdentity.Display)
	seen := map[string]bool{}
	var names []string
	for _, p := range meta.Participants {
		display := strings.TrimSpace(p.Display)
		if display == "" || strings.HasPrefix(display, "participant-") {
			continue
		}
		if display == strings.TrimSpace(p.PID) {
			continue
		}
		if recorder != "" && display == recorder {
			continue
		}
		if seen[display] {
			continue
		}
		seen[display] = true
		names = append(names, display)
	}
	sort.Strings(names)
	return names
}
