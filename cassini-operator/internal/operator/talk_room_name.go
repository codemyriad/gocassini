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
	"unicode/utf8"
)

// Talk recordings are keyed by the operator's ULID job id, which carries no
// human-readable name — spreed's recording-backend start payload has the room
// token but not the room name. This file closes that gap: when the operator
// runs as an AppAPI ExApp it fetches the room's display name from the Talk OCS
// API on behalf of the recording owner (they started the recording, so they
// are a participant and the lookup always succeeds), persists it on the job's
// Talk binding, and the build flow embeds it as the packed meeting's title
// (D-462). Every step is best-effort: a missing name only means the viewer
// falls back to "Untitled meeting".

// talkRoomInfo is what the operator needs from a Talk conversation: its
// display name (for the meeting title) and whether it is public, which decides
// the recording's audience (D-552).
type talkRoomInfo struct {
	Name string
	// Public is spreed's conversation type 3. A public room is one anyone with
	// the link can join, so its recording is not participant-private.
	Public bool
}

// talkRoomNameFetcher resolves a Talk room as seen by a Nextcloud user. Nil
// when the operator has no AppAPI credentials.
type talkRoomNameFetcher func(ctx context.Context, owner, roomToken string) (talkRoomInfo, error)

const (
	talkRoomNameTimeout  = 15 * time.Second
	talkRoomNameAttempts = 3
	// talkRoomNameRetryGap is the default for Runtime.talkRoomNameRetryGap.
	talkRoomNameRetryGap = 10 * time.Second
	// talkRoomNameMaxLen keeps a pathological room name from bloating
	// OpusTags and catalog entries.
	talkRoomNameMaxLen = 200
	// talkRoomTypePublic is spreed's ROOM_TYPE_PUBLIC. The others (1 one-to-one,
	// 2 group, 4 changelog, 5 former one-to-one, 6 note-to-self) are all
	// invitation-scoped, so only this one widens a recording's audience.
	talkRoomTypePublic = 3
)

// talkRoomNameFetcher returns a fetcher backed by the Talk OCS API, or nil
// when the ExApp environment (NEXTCLOUD_URL / APP_SECRET / APP_ID) is absent —
// standalone and dev deploys simply skip room-name resolution.
func (c ExAppConfig) talkRoomNameFetcher() talkRoomNameFetcher {
	if c.NextcloudURL == "" || c.AppSecret == "" || c.AppID == "" {
		return nil
	}
	base := strings.TrimRight(c.NextcloudURL, "/")
	client := &http.Client{Timeout: talkRoomNameTimeout}
	return func(ctx context.Context, owner, roomToken string) (talkRoomInfo, error) {
		owner = strings.TrimSpace(owner)
		roomToken = strings.TrimSpace(roomToken)
		if owner == "" || roomToken == "" {
			return talkRoomInfo{}, fmt.Errorf("owner and room token are required")
		}
		roomURL := base + "/ocs/v2.php/apps/spreed/api/v4/room/" + url.PathEscape(roomToken)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, roomURL, nil)
		if err != nil {
			return talkRoomInfo{}, fmt.Errorf("build room request: %w", err)
		}
		c.setAppAPIOCSHeadersForUser(req, owner)
		resp, err := client.Do(req)
		if err != nil {
			return talkRoomInfo{}, fmt.Errorf("room request failed: %w", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode >= 300 {
			snippet := strings.TrimSpace(string(body))
			if len(snippet) > 512 {
				cut := snippet[:512]
				// Byte-sliced; back off to a rune boundary before logging.
				for len(cut) > 0 && !utf8.ValidString(cut) {
					cut = cut[:len(cut)-1]
				}
				snippet = cut + "…"
			}
			return talkRoomInfo{}, fmt.Errorf("room request returned %d: %s", resp.StatusCode, snippet)
		}
		var payload struct {
			OCS struct {
				Data struct {
					DisplayName string `json:"displayName"`
					Name        string `json:"name"`
					// spreed's conversation type. Present on every room object;
					// absent only if Talk changes its API, in which case the
					// zero value reads as "not public" — fail closed.
					Type int `json:"type"`
				} `json:"data"`
			} `json:"ocs"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return talkRoomInfo{}, fmt.Errorf("decode room response: %w", err)
		}
		name := strings.TrimSpace(payload.OCS.Data.DisplayName)
		if name == "" {
			name = strings.TrimSpace(payload.OCS.Data.Name)
		}
		name = sanitizeTalkRoomName(name)
		if name == "" {
			return talkRoomInfo{}, fmt.Errorf("room response has no display name")
		}
		return talkRoomInfo{Name: name, Public: payload.OCS.Data.Type == talkRoomTypePublic}, nil
	}
}

// sanitizeTalkRoomName makes a room name safe to embed as a single-line
// title: control characters become spaces, runs of whitespace collapse, and
// pathological lengths are clamped.
func sanitizeTalkRoomName(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, name)
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	runes := []rune(cleaned)
	if len(runes) > talkRoomNameMaxLen {
		return string(runes[:talkRoomNameMaxLen])
	}
	return cleaned
}

// resolveTalkRoomName fetches and records the room name for a just-started
// Talk recording. Runs in its own goroutine off the start path: recording
// start must never wait on (or fail with) a room-name lookup. Retries a
// couple of times so a briefly unreachable Nextcloud does not cost the name.
func (rt *Runtime) resolveTalkRoomName(jobID, owner, roomToken string) {
	if rt.fetchTalkRoomName == nil {
		return
	}
	var lastErr error
	for attempt := 0; attempt < talkRoomNameAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-rt.ctx.Done():
				return
			case <-time.After(rt.talkRoomNameRetryGap):
			}
		}
		ctx, cancel := context.WithTimeout(rt.ctx, talkRoomNameTimeout)
		info, err := rt.fetchTalkRoomName(ctx, owner, roomToken)
		cancel()
		if err == nil {
			rt.storeTalkRoomInfo(jobID, info)
			return
		}
		lastErr = err
	}
	// The audience consequence is the one worth stating: without the room
	// object we do not know the conversation is public, so the recording keeps
	// the participant-only ACL. Fail closed — an over-restricted recording is
	// recoverable by a rerun; an over-shared one is not (D-552).
	rt.logger.Printf("talk room lookup failed id=%s room=%s: %v (meeting title falls back; recording treated as non-public)", jobID, roomToken, lastErr)
}

// storeTalkRoomInfo records the resolved room name and publicness on the
// in-memory room state and re-persists the job's Talk binding, so both survive
// an operator restart: the name until the build flow packs it into the meeting
// title, and the public flag until publish decides the recording's audience.
//
// Publicness is captured at record time on purpose. Deciding it at publish
// would let a room flipped public after the fact widen a recording made while
// it was private — and the reverse would silently narrow one people were told
// they could see.
func (rt *Runtime) storeTalkRoomInfo(jobID string, info talkRoomInfo) {
	rt.recordMu.Lock()
	state, ok := rt.talkJobs[jobID]
	if ok {
		state.RoomName = info.Name
		state.RoomPublic = info.Public
	}
	var snapshot talkRoomState
	if ok {
		snapshot = *state
	}
	rt.recordMu.Unlock()
	if !ok {
		// The job already finished and unbound; too late to matter.
		return
	}
	bindingJSON, err := encodeTalkBinding(&snapshot)
	if err != nil {
		rt.logger.Printf("talk room name binding encode failed id=%s: %v", jobID, err)
		return
	}
	if err := rt.store.SetJobTalkBinding(context.Background(), jobID, bindingJSON); err != nil {
		rt.logger.Printf("talk room name binding persist failed id=%s: %v", jobID, err)
	}
}

// talkRoomForJob returns the room a job was recorded in: its token and its
// display name. It prefers the live in-memory state and falls back to the
// persisted Talk binding (rerun or post-restart builds). Both are empty for a
// non-Talk job.
//
// The two are resolved together but are independently optional, and the token
// is the one that survives more failures. It is known synchronously when the
// recording starts, straight off the spreed request, while the name is fetched
// asynchronously and best-effort (resolveTalkRoomName). Reading the token from
// the binding rather than from the name fetcher is what keeps a failed name
// lookup from also costing the id — the case that decides whether a meeting can
// be grouped with the rest of its room at all (D-622).
func (rt *Runtime) talkRoomForJob(jobID string) (roomID, roomName string) {
	var public bool
	var seen bool
	if state, ok := rt.lookupTalkJobState(jobID); ok {
		seen = true
		roomID = strings.TrimSpace(state.RoomToken)
		roomName = strings.TrimSpace(state.RoomName)
		public = state.RoomPublic
	}
	// The in-memory state is dropped when the job unbinds, and a name resolved
	// after a restart lives only in the binding. Consult the binding whenever
	// either half is still missing, not only when both are.
	if !seen || roomID == "" || roomName == "" {
		if job, err := rt.store.GetJob(context.Background(), jobID); err == nil && job.TalkBinding != nil {
			if state, err := decodeTalkBinding(*job.TalkBinding); err == nil {
				if roomID == "" {
					roomID = strings.TrimSpace(state.RoomToken)
				}
				if roomName == "" {
					roomName = strings.TrimSpace(state.RoomName)
				}
				if !seen {
					public = state.RoomPublic
				}
			}
		}
	}
	// A PUBLIC conversation's token is withheld from the artifact.
	//
	// For a public room the publish ACL grants the virtual `everyone` group
	// READ on the recording (recordingACLRules, webdav_acl.go), so its catalog
	// entry is visible to every signed-in account — not only to people who were
	// told the room exists. And a Talk token is not merely an identifier for a
	// public conversation: https://<nextcloud>/call/<token> is its join link.
	// Publishing the token there would turn "may read a past recording" into
	// "may join the live conversation", which is a different permission and not
	// one publishing a recording was ever meant to grant.
	//
	// The name is still emitted, so these meetings group and filter by their
	// `name:` selector. Frozen-at-record-time publicness is what is consulted
	// (D-552), so a room flipped public afterwards cannot retroactively expose
	// a token already written, and one flipped private cannot un-withhold it.
	if public {
		return "", roomName
	}
	return roomID, roomName
}
