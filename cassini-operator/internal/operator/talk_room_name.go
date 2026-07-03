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

// Talk recordings are keyed by the operator's ULID job id, which carries no
// human-readable name — spreed's recording-backend start payload has the room
// token but not the room name. This file closes that gap: when the operator
// runs as an AppAPI ExApp it fetches the room's display name from the Talk OCS
// API on behalf of the recording owner (they started the recording, so they
// are a participant and the lookup always succeeds), persists it on the job's
// Talk binding, and the build flow embeds it as the packed meeting's title
// (D-462). Every step is best-effort: a missing name only means the viewer
// falls back to "Untitled meeting".

// talkRoomNameFetcher resolves a Talk room's display name as seen by a
// Nextcloud user. Nil when the operator has no AppAPI credentials.
type talkRoomNameFetcher func(ctx context.Context, owner, roomToken string) (string, error)

const (
	talkRoomNameTimeout  = 15 * time.Second
	talkRoomNameAttempts = 3
	// talkRoomNameRetryGap is the default for Runtime.talkRoomNameRetryGap.
	talkRoomNameRetryGap = 10 * time.Second
	// talkRoomNameMaxLen keeps a pathological room name from bloating
	// OpusTags and catalog entries.
	talkRoomNameMaxLen = 200
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
	return func(ctx context.Context, owner, roomToken string) (string, error) {
		owner = strings.TrimSpace(owner)
		roomToken = strings.TrimSpace(roomToken)
		if owner == "" || roomToken == "" {
			return "", fmt.Errorf("owner and room token are required")
		}
		roomURL := base + "/ocs/v2.php/apps/spreed/api/v4/room/" + url.PathEscape(roomToken)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, roomURL, nil)
		if err != nil {
			return "", fmt.Errorf("build room request: %w", err)
		}
		c.setAppAPIOCSHeadersForUser(req, owner)
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("room request failed: %w", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode >= 300 {
			return "", fmt.Errorf("room request returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var payload struct {
			OCS struct {
				Data struct {
					DisplayName string `json:"displayName"`
					Name        string `json:"name"`
				} `json:"data"`
			} `json:"ocs"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return "", fmt.Errorf("decode room response: %w", err)
		}
		name := strings.TrimSpace(payload.OCS.Data.DisplayName)
		if name == "" {
			name = strings.TrimSpace(payload.OCS.Data.Name)
		}
		if name == "" {
			return "", fmt.Errorf("room response has no display name")
		}
		return clampTalkRoomName(name), nil
	}
}

func clampTalkRoomName(name string) string {
	runes := []rune(name)
	if len(runes) <= talkRoomNameMaxLen {
		return name
	}
	return string(runes[:talkRoomNameMaxLen])
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
		name, err := rt.fetchTalkRoomName(ctx, owner, roomToken)
		cancel()
		if err == nil {
			rt.storeTalkRoomName(jobID, name)
			return
		}
		lastErr = err
	}
	rt.logger.Printf("talk room name lookup failed id=%s room=%s: %v (meeting title falls back)", jobID, roomToken, lastErr)
}

// storeTalkRoomName records a resolved room name on the in-memory room state
// and re-persists the job's Talk binding so the name survives an operator
// restart until the build flow packs it into the meeting title.
func (rt *Runtime) storeTalkRoomName(jobID, name string) {
	rt.recordMu.Lock()
	state, ok := rt.talkJobs[jobID]
	if ok {
		state.RoomName = name
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

// talkRoomNameForJob returns the room name recorded for a job, preferring the
// live in-memory state and falling back to the persisted Talk binding (rerun
// or post-restart builds). Empty for non-Talk jobs or when resolution failed.
func (rt *Runtime) talkRoomNameForJob(jobID string) string {
	if state, ok := rt.lookupTalkJobState(jobID); ok && strings.TrimSpace(state.RoomName) != "" {
		return strings.TrimSpace(state.RoomName)
	}
	job, err := rt.store.GetJob(context.Background(), jobID)
	if err != nil || job.TalkBinding == nil {
		return ""
	}
	state, err := decodeTalkBinding(*job.TalkBinding)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(state.RoomName)
}
