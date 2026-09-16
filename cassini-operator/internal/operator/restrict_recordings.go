package operator

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// Narrowing a world-readable recording to the people who were in the call
// (D-769). The one write this feature performs.

// What one recording's narrowing did. Machine-readable, because the panel
// renders each differently and three of the four are not failures.
const (
	// restrictOutcomeRestricted: the audience was written. `Grants` counts the
	// principals now able to read it.
	restrictOutcomeRestricted = "restricted"
	// restrictOutcomeStale: the audience is no longer the one the
	// administrator confirmed. Nothing was written — re-read the list and ask
	// again, so that what was approved and what is applied stay the same thing.
	restrictOutcomeStale = "refused_stale"
	// restrictOutcomeEmpty: the roster resolved to nobody grantable. Writing it
	// would leave the recording readable by its owner alone, which is not a
	// narrower audience but an absent one — see selfHealLeafProtection, which
	// repairs a rule-less leaf to exactly that.
	restrictOutcomeEmpty = "refused_empty"
	// restrictOutcomeNotOpen: the recording is not on the open list any more.
	// Somebody restricted it, ignored it, or it was a public conversation all
	// along. Not an error; the panel re-derives and it is gone.
	restrictOutcomeNotOpen = "refused_not_open"
	// restrictOutcomeFailed: Nextcloud refused the write. The recording is
	// unchanged and the detail says why.
	restrictOutcomeFailed = "failed"
)

// restrictMeetingRequest is one row an administrator ticked.
//
// The digest, not the principals: the audience is not editable in Cassini, so
// the browser has no business authoring an ACL. What it sends back is a
// fingerprint of what it displayed, and the operator re-reads the audience from
// its own records. That keeps "what was confirmed" and "what was written" the
// same set without trusting the client for either.
type restrictMeetingRequest struct {
	ID             string `json:"id"`
	AudienceDigest string `json:"audience_digest"`
}

type restrictMeetingResult struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome"`
	Grants  int    `json:"grants,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// restrictOpenRecordings applies each requested recording's captured audience.
//
// It re-derives the whole open list first and treats that as the authority for
// every decision — is this recording still open, what is its audience, does the
// digest still match. One PROPFIND covers the batch, and more importantly there
// is exactly one implementation of "what should this recording be narrowed to",
// shared with the list the administrator was looking at.
//
// Held under provisionMu for the same reason a delivery is: the last step of a
// storage-mode switch empties a root, and an ACL written into a tree that is
// about to be cleared is work thrown away at best.
func (c ExAppConfig) restrictOpenRecordings(ctx context.Context, store *Store, requests []restrictMeetingRequest, logger *log.Logger) ([]restrictMeetingResult, error) {
	if !c.appAPIActive() {
		return nil, fmt.Errorf("recordings can only be limited in a Nextcloud (AppAPI) deployment")
	}
	provisionMu.Lock()
	defer provisionMu.Unlock()

	if !ncStorage.migrationClean() {
		// An unfinished switch means the other root still holds a copy, and
		// which tree is authoritative is exactly what is unsettled. Writing
		// permissions into one of them now would be guessing.
		return nil, fmt.Errorf("a storage migration has not finished; finish it before limiting recordings")
	}

	open, err := c.listOpenRecordings(ctx, store, logger)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]openRecording, len(open.Recordings))
	for _, entry := range open.Recordings {
		byID[entry.ID] = entry
	}

	client := &http.Client{Timeout: ncFilesACLTimeout}
	results := make([]restrictMeetingResult, 0, len(requests))
	for _, request := range requests {
		id := strings.TrimSpace(request.ID)
		if id == "" {
			continue
		}
		entry, listed := byID[id]
		switch {
		case !listed:
			results = append(results, restrictMeetingResult{ID: id, Outcome: restrictOutcomeNotOpen})
			continue
		case !entry.Narrowable:
			results = append(results, restrictMeetingResult{ID: id, Outcome: restrictOutcomeEmpty, Detail: entry.Reason})
			continue
		case entry.AudienceDigest != strings.TrimSpace(request.AudienceDigest):
			results = append(results, restrictMeetingResult{ID: id, Outcome: restrictOutcomeStale})
			continue
		}

		mappings := make([]aclMapping, 0, len(entry.Audience))
		for _, principal := range entry.Audience {
			mappings = append(mappings, aclMapping{Type: principal.Type, ID: principal.ID})
		}
		// public=false: this is the narrowing. The everyone group is denied at
		// the leaf and each captured principal is granted read.
		relPath := ncACLRecordingsRoot + "/meetings/" + id + ".opus"
		if err := c.davProppatchACL(ctx, client, ncRecordingsOwner, relPath, mappings, false); err != nil {
			logger.Printf("restrict recordings: id=%s failed: %v", id, err)
			results = append(results, restrictMeetingResult{ID: id, Outcome: restrictOutcomeFailed, Detail: err.Error()})
			continue
		}
		logger.Printf("restrict recordings: id=%s limited to %d principal(s)", id, len(mappings))
		results = append(results, restrictMeetingResult{ID: id, Outcome: restrictOutcomeRestricted, Grants: len(mappings)})
	}
	return results, nil
}
