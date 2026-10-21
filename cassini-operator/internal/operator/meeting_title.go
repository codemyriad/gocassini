package operator

import (
	"context"
	"sort"
	"strings"
)

// A 1:1 room is named after the counterpart from the recording owner's view.
// Its meeting title instead uses both conversation members, captured alongside
// the room metadata at recording start. Media tracks are not a membership
// roster: a silent or absent member may never appear in session.json.
type talkTitleParticipant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Account IDs establish identity; display names only label it. A name equal
// to its account ID (admin), or shared by two people, is perfectly valid.
// Guests such as the recorder are not members of the local-account pair.
func oneToOneTitleParticipants(rows []participantRow) []talkTitleParticipant {
	seen := map[string]string{}
	for _, row := range rows {
		if row.ActorType != "users" {
			continue
		}
		id := strings.TrimSpace(row.ActorID)
		if id == "" {
			id = strings.TrimSpace(row.UserID)
		}
		name := sanitizeTalkRoomName(row.DisplayName)
		if id == "" || name == "" {
			return nil
		}
		if previous, ok := seen[id]; ok && previous != name {
			return nil
		}
		seen[id] = name
	}
	if len(seen) != 2 {
		return nil
	}
	participants := make([]talkTitleParticipant, 0, 2)
	for id, name := range seen {
		participants = append(participants, talkTitleParticipant{ID: id, Name: name})
	}
	// Stable across API order and the recording owner; ignore case so admin
	// precedes Alice. Use identity to break ties without collapsing namesakes.
	sort.Slice(participants, func(i, j int) bool {
		a, b := strings.ToLower(participants[i].Name), strings.ToLower(participants[j].Name)
		if a != b {
			return a < b
		}
		return participants[i].ID < participants[j].ID
	})
	return participants
}

// Builds and reruns use the frozen snapshot, never a fresh membership lookup.
// Old jobs and unresolved lookups retain the room-name fallback.
func (rt *Runtime) defaultMeetingTitle(jobID, roomName string) string {
	state, ok := rt.lookupTalkJobState(jobID)
	if !ok || state.RoomType == 0 {
		job, err := rt.store.GetJob(context.Background(), jobID)
		if err != nil || job.TalkBinding == nil {
			return roomName
		}
		state, err = decodeTalkBinding(*job.TalkBinding)
		if err != nil {
			return roomName
		}
	}
	if !isOneToOneTalkRoom(state.RoomType) || len(state.TitleParticipants) != 2 {
		return roomName
	}
	return sanitizeTalkRoomName(state.TitleParticipants[0].Name + " & " + state.TitleParticipants[1].Name)
}
