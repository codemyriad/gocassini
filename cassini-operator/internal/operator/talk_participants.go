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

// A recording's share audience comes from Talk participants. The service
// account reads the room's participant list and keeps local users, groups and
// Teams. Guests, email invitees and federated users have no local Files share.

// sharePrincipal names one recipient of a Nextcloud Files share.
type sharePrincipal struct {
	Type string
	ID   string
}

// talkParticipantsFetcher resolves local share principals for a Talk
// room, acting as the given owner. Nil when the operator has no AppAPI
// credentials.
type talkParticipantsFetcher func(ctx context.Context, owner, roomToken string) ([]sharePrincipal, error)

const talkParticipantsTimeout = 15 * time.Second

// talkParticipantsFetcher returns a fetcher backed by the Talk OCS participants
// API, or nil when the ExApp environment is absent.
func (c ExAppConfig) talkParticipantsFetcher() talkParticipantsFetcher {
	return c.talkPrincipalFetcher(false)
}

// talkAttendeesFetcher returns the same participants, narrowed to the people
// Talk reports as in the call (the in-call bit of the participant's inCall
// flags). The room's participants API lists members whether they are on the
// call or not; this is the option that shares with the call rather than the
// channel.
func (c ExAppConfig) talkAttendeesFetcher() talkParticipantsFetcher {
	return c.talkPrincipalFetcher(true)
}

func (c ExAppConfig) talkPrincipalFetcher(attendeesOnly bool) talkParticipantsFetcher {
	if !c.appAPIActive() {
		return nil
	}
	client := &http.Client{Timeout: talkParticipantsTimeout}
	return func(ctx context.Context, owner, roomToken string) ([]sharePrincipal, error) {
		rows, err := c.fetchTalkParticipantRows(ctx, client, owner, roomToken)
		if err != nil {
			return nil, err
		}
		return participantMappings(rows, attendeesOnly), nil
	}
}

// Share discovery and title snapshots read the same membership endpoint, but
// select their own fields and actors. Titles must never change share policy.
func (c ExAppConfig) fetchTalkParticipantRows(ctx context.Context, client *http.Client, owner, roomToken string) ([]participantRow, error) {
	owner = strings.TrimSpace(owner)
	roomToken = strings.TrimSpace(roomToken)
	if owner == "" || roomToken == "" {
		return nil, fmt.Errorf("owner and room token are required")
	}
	partURL := strings.TrimRight(c.NextcloudURL, "/") + "/ocs/v2.php/apps/spreed/api/v4/room/" + url.PathEscape(roomToken) + "/participants"
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read participants response: %w", err)
	}
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
	return payload.OCS.Data, nil
}

// participantACTORs are Talk actor types that map to a local share principal. Everything else (guests, emails, federated_users,
// phones) has no local Nextcloud account/group to grant read on.
var participantACTORs = map[string]string{
	"users":   "user",
	"groups":  "group",
	"circles": "circle",
}

type participantRow struct {
	ActorType   string `json:"actorType"`
	ActorID     string `json:"actorId"`
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	// InCall is Talk's in-call flag bitmask: bit 1 means the participant is in
	// the call, while the higher bits only describe published audio/video. The
	// attendee option keeps bit 1; the room option ignores the field.
	InCall int `json:"inCall"`
}

// participantInCallFlag is the in-call bit of Talk's inCall flags.
const participantInCallFlag = 1

// participantMappings deduplicates the locally shareable Talk actors.
//
// attendeesOnly keeps only the actors Talk reports as in the call. A group or
// Team row is a room grant, not a person on the call, so it is filtered out
// with the room-presence rows; the starter is added by the caller regardless.
func participantMappings(rows []participantRow, attendeesOnly bool) []sharePrincipal {
	seen := map[string]bool{}
	out := make([]sharePrincipal, 0, len(rows))
	for _, row := range rows {
		mapType, ok := participantACTORs[row.ActorType]
		if !ok {
			continue
		}
		// Attendees are people in the call: a group or Team is a room-level
		// grant, never a person on the call, so it is left out with the
		// room-presence rows.
		if attendeesOnly && (row.InCall&participantInCallFlag == 0 || mapType != "user") {
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
		out = append(out, sharePrincipal{Type: mapType, ID: id})
	}
	return out
}

// talkBindingForJob recovers a Talk job's owner and room token, preferring the
// live in-memory room state and falling back to the persisted Talk binding
// (reruns / post-restart publishes). ok is false for non-Talk jobs, which have
// no participants to freeze.
func (rt *Runtime) talkBindingForJob(jobID string) (binding talkRecordingBinding, ok bool) {
	if state, live := rt.lookupTalkJobState(jobID); live {
		if b, valid := talkRecordingBindingFrom(state); valid {
			return b, true
		}
	}
	job, err := rt.store.GetJob(context.Background(), jobID)
	if err != nil || job.TalkBinding == nil {
		return talkRecordingBinding{}, false
	}
	state, err := decodeTalkBinding(*job.TalkBinding)
	if err != nil {
		return talkRecordingBinding{}, false
	}
	return talkRecordingBindingFrom(state)
}

// talkRecordingBinding is what publish needs from a Talk recording: who to ask
// about the room, which room, and whether it was public when it was recorded.
type talkRecordingBinding struct {
	Owner     string
	RoomToken string
	Public    bool
}

func talkRecordingBindingFrom(state talkRoomState) (talkRecordingBinding, bool) {
	owner := strings.TrimSpace(state.Owner)
	roomToken := strings.TrimSpace(state.RoomToken)
	if owner == "" || roomToken == "" {
		return talkRecordingBinding{}, false
	}
	return talkRecordingBinding{Owner: owner, RoomToken: roomToken, Public: state.RoomPublic}, true
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
func (rt *Runtime) resolveRecordingAudience(ctx context.Context, jobID, starter, roomToken string) ([]sharePrincipal, string, error) {
	return rt.resolveRecordingAudienceWith(ctx, jobID, starter, roomToken, rt.fetchTalkParticipants)
}

// resolveRecordingAudienceWith is the resolver with the fetcher chosen by the
// caller: the room's participants, or the in-call attendees when the operator's
// "Share recordings with" setting asks for the call rather than the channel.
func (rt *Runtime) resolveRecordingAudienceWith(ctx context.Context, jobID, starter, roomToken string, fetch talkParticipantsFetcher) ([]sharePrincipal, string, error) {
	if fetch == nil {
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
		mappings, err := fetch(ctx, starter, roomToken)
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
		mappings, err := fetch(ctx, ncRecordingsOwner, roomToken)
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
