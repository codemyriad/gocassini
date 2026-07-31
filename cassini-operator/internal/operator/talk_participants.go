package operator

import (
	"context"
	"encoding/json"
	"errors"
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

// Resolving a recording's audience is now load-bearing: the nextcloud-files
// sink fails the publish when it cannot be determined (D-549). A single
// un-retried OCS call is too thin a thread to hang a recording on, so the
// lookup escalates through two tiers before giving up (D-553).
//
//	tier 1   as the recording's starter  ×talkAudienceAttempts, with a gap
//	   │        the room's own moderator; the identity Talk expects
//	   ▼        (transient 5xx / timeout / network blip retried here)
//	tier 2   as the recordings owner     ×1
//	            covers the case tier 1 structurally cannot: the starter left
//	            the room, was removed, or was disabled between record and
//	            publish — minutes to hours later, or on a rerun.
//	   │
//	   ▼
//	unresolved → the caller decides; the sink fails the publish.
const (
	// talkAudienceAttempts bounds tier 1, mirroring talkRoomNameAttempts.
	talkAudienceAttempts = 3
	// talkAudienceRetryGap is the default for Runtime.talkAudienceRetryGap.
	// Shorter than the room-name gap: a publish is waiting on this, where a
	// room name is cosmetic and resolved off the critical path.
	talkAudienceRetryGap = 3 * time.Second
)

// audienceSourceOwner and audienceSourceStarter name which identity produced
// the audience, so the log line says how it was obtained rather than only that
// it was.
const (
	audienceSourceStarter = "starter"
	audienceSourceOwner   = "recordings-owner"
)

// resolveRecordingAudience returns the grantable principals for a room, the
// identity that produced them, and an error only when every tier failed.
//
// An empty result with a nil error is a real answer, not a failure: a room
// whose attendees are all guests/email/federated has no local principal to
// grant. The caller distinguishes that from "we could not find out".
func (rt *Runtime) resolveRecordingAudience(ctx context.Context, jobID, starter, roomToken string) ([]aclMapping, string, error) {
	if rt.fetchTalkParticipants == nil {
		return nil, "", nil
	}

	var errs []error
	for attempt := 0; attempt < talkAudienceAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, "", errors.Join(append(errs, ctx.Err())...)
			case <-rt.ctx.Done():
				return nil, "", errors.Join(append(errs, rt.ctx.Err())...)
			case <-time.After(rt.audienceRetryGap()):
			}
		}
		mappings, err := rt.fetchTalkParticipants(ctx, starter, roomToken)
		if err == nil {
			return mappings, audienceSourceStarter, nil
		}
		errs = append(errs, fmt.Errorf("as %s (attempt %d): %w", starter, attempt+1, err))
	}

	// Tier 2. Not a retry of the same question — a different identity asking
	// it, which is the only thing that helps when the starter is no longer a
	// member. Skipped when they are the same principal.
	if strings.TrimSpace(starter) != ncRecordingsOwner {
		rt.logger.Printf("nc files access: participants lookup as %s exhausted id=%s — retrying as %s", starter, jobID, ncRecordingsOwner)
		mappings, err := rt.fetchTalkParticipants(ctx, ncRecordingsOwner, roomToken)
		if err == nil {
			return mappings, audienceSourceOwner, nil
		}
		errs = append(errs, fmt.Errorf("as %s: %w", ncRecordingsOwner, err))
	}
	return nil, "", errors.Join(errs...)
}

// audienceRetryGap lets tests shrink the wait without exporting the field's
// zero value as "no gap" to production.
func (rt *Runtime) audienceRetryGap() time.Duration {
	if rt.talkAudienceRetryGap > 0 {
		return rt.talkAudienceRetryGap
	}
	return talkAudienceRetryGap
}
