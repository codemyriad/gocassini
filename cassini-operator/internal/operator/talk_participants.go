package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Nextcloud-native access control (D-534) freezes a recording's audience to the
// Talk room's participants at publish time. This file enumerates those
// participants: acting as the recording owner (who is a room participant, so
// the lookup always succeeds and is not gated by a lobby), it reads the spreed
// participants OCS endpoint and maps each *local* participant to an advanced-ACL
// mapping. Guests, email invitees and federated users have no local Nextcloud
// account to grant read on, so they are warned about and skipped.

// aclMapping is one advanced-ACL principal a recording is granted read on. Type
// is the groupfolders mapping type ("user", "group", "circle"); ID is the
// principal identifier (a Nextcloud user id, group id, or circle/team id).
type aclMapping struct {
	Type string
	ID   string
}

// talkParticipantsFetcher resolves the grantable ACL principals for a Talk
// room, acting as the given owner. Nil when the operator has no AppAPI
// credentials.
type talkParticipantsFetcher func(ctx context.Context, owner, roomToken string) ([]aclMapping, error)

const talkParticipantsTimeout = 15 * time.Second

// talkParticipantsFetcher returns a fetcher backed by the Talk OCS participants
// API, or nil when the ExApp environment is absent (standalone/dev deploys skip
// access control entirely).
func (c ExAppConfig) talkParticipantsFetcher() talkParticipantsFetcher {
	if !c.appAPIActive() {
		return nil
	}
	base := strings.TrimRight(c.NextcloudURL, "/")
	client := &http.Client{Timeout: talkParticipantsTimeout}
	return func(ctx context.Context, owner, roomToken string) ([]aclMapping, error) {
		owner = strings.TrimSpace(owner)
		roomToken = strings.TrimSpace(roomToken)
		if owner == "" || roomToken == "" {
			return nil, fmt.Errorf("owner and room token are required")
		}
		partURL := base + "/ocs/v2.php/apps/spreed/api/v4/room/" + url.PathEscape(roomToken) + "/participants"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, partURL, nil)
		if err != nil {
			return nil, fmt.Errorf("build participants request: %w", err)
		}
		c.setAppAPIOCSHeadersForUser(req, owner)
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("participants request failed: %w", err)
		}
		defer drainClose(resp.Body)
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("participants request returned %d", resp.StatusCode)
		}
		var payload struct {
			OCS struct {
				Data []participantRow `json:"data"`
			} `json:"ocs"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode participants response: %w", err)
		}
		return participantMappings(payload.OCS.Data), nil
	}
}

// participantACTORs are the spreed actor types that map to a locally grantable
// advanced-ACL principal. Everything else (guests, emails, federated_users,
// phones) has no local Nextcloud account/group to grant read on.
var participantACTORs = map[string]string{
	"users":   "user",
	"groups":  "group",
	"circles": "circle",
}

type participantRow struct {
	ActorType string `json:"actorType"`
	ActorID   string `json:"actorId"`
	UserID    string `json:"userId"`
}

// participantMappings converts a spreed participants list into deduplicated ACL
// mappings, keeping only the locally grantable actor types.
func participantMappings(rows []participantRow) []aclMapping {
	seen := map[string]bool{}
	out := make([]aclMapping, 0, len(rows))
	for _, row := range rows {
		mapType, ok := participantACTORs[row.ActorType]
		if !ok {
			continue
		}
		id := strings.TrimSpace(row.ActorID)
		if id == "" {
			id = strings.TrimSpace(row.UserID)
		}
		if id == "" {
			continue
		}
		key := mapType + "\x00" + id
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, aclMapping{Type: mapType, ID: id})
	}
	return out
}

// talkBindingForJob recovers a Talk job's owner and room token, preferring the
// live in-memory room state and falling back to the persisted Talk binding
// (reruns / post-restart publishes). ok is false for non-Talk jobs, which have
// no participants to freeze.
func (rt *Runtime) talkBindingForJob(jobID string) (owner, roomToken string, ok bool) {
	if state, live := rt.lookupTalkJobState(jobID); live {
		owner = strings.TrimSpace(state.Owner)
		roomToken = strings.TrimSpace(state.RoomToken)
		if owner != "" && roomToken != "" {
			return owner, roomToken, true
		}
	}
	job, err := rt.store.GetJob(context.Background(), jobID)
	if err != nil || job.TalkBinding == nil {
		return "", "", false
	}
	state, err := decodeTalkBinding(*job.TalkBinding)
	if err != nil {
		return "", "", false
	}
	owner = strings.TrimSpace(state.Owner)
	roomToken = strings.TrimSpace(state.RoomToken)
	if owner == "" || roomToken == "" {
		return "", "", false
	}
	return owner, roomToken, true
}
