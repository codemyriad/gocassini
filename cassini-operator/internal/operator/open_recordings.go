package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
)

// The recordings a migration left readable by everyone, and what could be done
// about them (D-769).
//
// Switching an archive into the access-controlled model copies every recording
// into the Team folder and leaves each one world-readable — publicRecordingACLRules,
// by decision rather than omission, because the first pass had no way to know a
// past meeting's audience. The app said so once, in the page that performed the
// switch, and never again: "the operator records no per-recording audience, so
// there is nothing to read back on a later load" (recordingAccess.ts).
//
// Now there is. The roster is captured while the meeting runs
// (talk_room_audience.go), so the question can be asked properly:
//
//	which recordings are readable by everyone, which of those SHOULD be, and
//	for the rest, who would get them instead?
//
// Nothing here is stored. The list is recomputed from the archive and the jobs
// table on every read, which is what makes it survive a reload without anybody
// maintaining it — and what makes a restricted recording leave the list by
// simply no longer qualifying. The single exception is the ignore set, the one
// decision that cannot be re-derived from anything.

// Why a recording is listed but cannot be narrowed. Machine-readable, because
// the panel renders a different sentence for each and a UI that string-matched
// prose would break on a copy edit.
const (
	// openRecordingNoJob: nothing in the jobs table owns this leaf. A non-Talk
	// job, a recording from an archive that was carried in, or one whose job
	// row predates the operator keeping them.
	openRecordingNoJob = "no_job"
	// openRecordingNoRoster: there is a job, but no roster was ever captured —
	// it predates D-769, or every tier of the lookup failed at record time.
	// Unrepairable: the room it would have to ask has moved on.
	openRecordingNoRoster = "no_roster"
	// openRecordingNobodyGrantable: a roster WAS captured and it was empty.
	// Every attendee was a guest, an email invitee or federated, so there is no
	// local principal to grant. Narrowing would leave the recording readable by
	// its owner alone — invisible to the very people it belongs to, which is
	// the one outcome worse than leaving it open (selfHealLeafProtection
	// repairs a rule-less leaf to owner-only).
	openRecordingNobodyGrantable = "nobody_grantable"
)

// openRecordingPrincipal is one account, group or circle that would be granted
// read. The display name is deliberately absent: Cassini stores ids, and
// resolving them to names would mean a Nextcloud round trip per principal on a
// page an administrator may simply be passing through.
type openRecordingPrincipal struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// openRecording is one world-readable recording as the panel renders it.
type openRecording struct {
	// ID is the meeting id — the job id, and the .opus basename.
	ID string `json:"id"`
	// RoomName and CreatedAt come from the job row, for a row a human can
	// recognise. Both may be empty; neither is load-bearing.
	RoomName  string `json:"room_name,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	// Narrowable says this recording has a captured roster that could be
	// applied. False makes the row inert in the UI: no checkbox, no action.
	Narrowable bool `json:"narrowable"`
	// Reason names why it is not narrowable. Empty when it is.
	Reason string `json:"reason,omitempty"`
	// Audience is exactly who would be granted, and is what the confirmation
	// shows. Empty unless Narrowable.
	Audience []openRecordingPrincipal `json:"audience,omitempty"`
	// AudienceDigest fingerprints that audience. The apply step refuses a
	// recording whose digest no longer matches, so what an administrator
	// confirmed and what gets written cannot diverge.
	AudienceDigest string `json:"audience_digest,omitempty"`
}

// openRecordingsResult is the answer to "what is readable by everyone".
type openRecordingsResult struct {
	// Recordings are the ones needing attention: world-readable, not from a
	// public conversation, not ignored.
	Recordings []openRecording `json:"recordings"`
	// Ignored are the world-readable recordings an administrator has dismissed.
	// Returned rather than merely counted so the panel can offer to undo one
	// without a second round trip.
	Ignored []openRecording `json:"ignored"`
	// Narrowable is how many of Recordings could actually be acted on — the
	// number the button counts.
	Narrowable int `json:"narrowable"`
}

// listOpenRecordings walks the archive and says which recordings are readable
// by every account, which of those could be narrowed, and to whom.
//
// It reads: one Depth-1 PROPFIND of the Team folder's meetings collection for
// the ACL rows, the jobs table for publicness and rosters, and the ignore set.
// It writes nothing and touches no recording.
func (c ExAppConfig) listOpenRecordings(ctx context.Context, store *Store, logger *log.Logger) (openRecordingsResult, error) {
	result := openRecordingsResult{Recordings: []openRecording{}, Ignored: []openRecording{}}
	if !c.appAPIActive() {
		return result, fmt.Errorf("open recordings can only be listed in a Nextcloud (AppAPI) deployment")
	}
	if accessControlled, resolved := ncStorage.mode(); !resolved || !accessControlled {
		// In the default model every recording is readable by everyone by
		// design, and no leaf carries rules at all — there is no question here
		// to answer, and answering "none" would read as reassurance.
		return result, fmt.Errorf("recordings are only limited to their participants in the access-controlled storage model")
	}

	client := &http.Client{Timeout: ncFilesACLTimeout}
	acls, err := c.davPropfindACLLists(ctx, client, ncRecordingsOwner, ncACLRecordingsRoot+"/meetings")
	if err != nil {
		return result, fmt.Errorf("could not read the recordings' permissions: %w", err)
	}

	jobs, err := store.ListJobs(ctx)
	if err != nil {
		return result, fmt.Errorf("could not read the recording history: %w", err)
	}
	byID := make(map[string]Job, len(jobs))
	for _, job := range jobs {
		byID[job.ID] = job
	}

	ignored, err := store.ListIgnoredRecordings(ctx)
	if err != nil {
		return result, fmt.Errorf("could not read the ignored recordings: %w", err)
	}

	for base, rules := range acls {
		if !strings.HasSuffix(base, ".opus") {
			continue
		}
		// everyoneRuleGovernsRead, not hasExplicitEveryoneGroupRule: a rule
		// whose mask omits READ decides nothing and the leaf keeps the
		// container's grant, so it is world-readable with an ACL that looks
		// like a restriction. That is the shape Files' own dialog produces
		// when somebody sets `everyone` back to "inherit".
		if everyoneRuleGovernsRead(rules) && !grantsEveryoneRead(rules) {
			continue
		}
		id := strings.TrimSuffix(base, ".opus")
		entry := openRecording{ID: id}
		job, haveJob := byID[id]
		if haveJob {
			if job.RoomName != nil {
				entry.RoomName = strings.TrimSpace(*job.RoomName)
			}
			entry.CreatedAt = job.CreatedAt
		}

		if ignored[id] {
			result.Ignored = append(result.Ignored, entry)
			continue
		}

		switch {
		case !haveJob:
			entry.Reason = openRecordingNoJob
		case recordingRoomWasPublic(job):
			// A public conversation's recording is readable by everyone
			// because it should be. Not a problem, so not a row.
			continue
		default:
			audience, captured, decodeErr := jobRoomAudienceFrom(job)
			switch {
			case decodeErr != nil:
				logger.Printf("open recordings: id=%s has an unreadable room audience: %v", id, decodeErr)
				entry.Reason = openRecordingNoRoster
			case !captured:
				entry.Reason = openRecordingNoRoster
			case len(audience) == 0:
				entry.Reason = openRecordingNobodyGrantable
			default:
				entry.Narrowable = true
				entry.Audience = principalsFor(audience)
				entry.AudienceDigest = audienceDigest(audience)
			}
		}
		result.Recordings = append(result.Recordings, entry)
	}

	sortOpenRecordings(result.Recordings)
	sortOpenRecordings(result.Ignored)
	for _, entry := range result.Recordings {
		if entry.Narrowable {
			result.Narrowable++
		}
	}
	return result, nil
}

// grantsEveryoneRead reports that the leaf's broad-group rule actually allows
// read — the positive half of everyoneRuleGovernsRead, which only says the rule
// decides the bit rather than which way it decided.
func grantsEveryoneRead(rules []aclRule) bool {
	for _, rule := range rules {
		if rule.Type == "group" && rule.ID == ncRecordingsEveryoneGroup && rule.Mask&aclPermRead != 0 {
			return rule.Permissions&aclPermRead != 0
		}
	}
	return false
}

// recordingRoomWasPublic reads the publicness frozen onto the job at record
// time. An undecodable binding reads as NOT public, which is the conservative
// direction here: the recording stays on the list as something to look at,
// rather than being silently excused as "it was meant to be open".
func recordingRoomWasPublic(job Job) bool {
	if job.TalkBinding == nil {
		return false
	}
	state, err := decodeTalkBinding(*job.TalkBinding)
	if err != nil {
		return false
	}
	return state.RoomPublic
}

// jobRoomAudienceFrom reads a roster off an already-loaded job row, so listing
// the whole archive costs one query rather than one per recording.
func jobRoomAudienceFrom(job Job) (mappings []aclMapping, captured bool, err error) {
	if job.RoomAudienceAt == nil || strings.TrimSpace(*job.RoomAudienceAt) == "" {
		return nil, false, nil
	}
	raw := ""
	if job.RoomAudience != nil {
		raw = *job.RoomAudience
	}
	decoded, err := decodeRoomAudience(raw)
	if err != nil {
		return nil, true, err
	}
	return decoded, true, nil
}

func principalsFor(mappings []aclMapping) []openRecordingPrincipal {
	out := make([]openRecordingPrincipal, 0, len(mappings))
	for _, mapping := range mappings {
		out = append(out, openRecordingPrincipal{Type: mapping.Type, ID: mapping.ID})
	}
	return out
}

// audienceDigest fingerprints exactly the principals that would be granted.
//
// It hashes the canonical encoding rather than the raw column, so a roster
// re-serialised by a later release — same principals, different byte order —
// still matches the digest the panel was shown. What it must catch is the
// audience CHANGING between the list and the apply, not the storage layer
// rewriting it.
func audienceDigest(mappings []aclMapping) string {
	encoded, err := encodeRoomAudience(mappings)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(sum[:])
}

// sortOpenRecordings puts the newest first, which is the order an
// administrator recognises recordings in. Ties break on id so the list is
// stable across reloads — a list that reshuffles under a cursor is one people
// misclick.
func sortOpenRecordings(entries []openRecording) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CreatedAt != entries[j].CreatedAt {
			return entries[i].CreatedAt > entries[j].CreatedAt
		}
		return entries[i].ID < entries[j].ID
	})
}
