package operator

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The tag read surface (D-737): the vocabulary, and the `tag` parameter search
// and the meeting list narrow by. Both take the caller's visible set exactly as
// search does (resolveVisibleMeetings), so a tag on a meeting the caller cannot
// open never appears and never moves a count.

// maxTagParamRunes bounds `tag=`. No label or tag id is longer, so a longer
// value can match nothing — a 400, since an empty answer would read as "no
// meetings with that tag".
const maxTagParamRunes = 64

const (
	tagIndexUnavailableMessage = "the tag index is not available on this deployment; this is not an empty result"
	tagIndexUnreadableMessage  = "the tag index could not be read; this is not an empty result"
)

// parseTagParam reads `tag=`: a label, matched case-insensitively, or a tag id.
// Blank means no narrowing, as a blank `room` does.
func parseTagParam(query url.Values) (string, error) {
	tag := strings.TrimSpace(query.Get("tag"))
	if utf8.RuneCountInString(tag) > maxTagParamRunes {
		return "", &meetingsListBadRequest{fmt.Sprintf(
			"tag is at most %d characters — no label or tag id is longer, so this could never match", maxTagParamRunes)}
	}
	return tag, nil
}

// tagVocabularyResponse is GET annotations/tags.
type tagVocabularyResponse struct {
	Tags []tagVocabularyEntry `json:"tags"`
	// Meetings is each visible meeting with a resolved mark, by catalog id.
	Meetings []meetingTagsEntry `json:"meetings"`
	// Coverage: Indexed below Visible means some meetings' marks could not be
	// read, and the counts are partial.
	Coverage annotationCoverage `json:"coverage"`
}

type meetingTagsEntry struct {
	MeetingID string            `json:"meetingId"`
	Tags      []meetingTagMarks `json:"tags"`
}

// serveTags answers GET annotations/tags: the tag vocabulary across the caller's
// visible meetings, with coverage.
func (s *annotationService) serveTags(w http.ResponseWriter, r *http.Request, caller string) {
	// Search's budget: each answer costs Nextcloud a PROPFIND and a catalog GET.
	if allowed, wait := s.rt.searchLimiter.allow(caller); !allowed {
		seconds := int(wait.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeJSONError(w, http.StatusTooManyRequests,
			"too many requests; each one asks Nextcloud what you may read, so they are rate limited — retry shortly")
		return
	}
	store := s.rt.annotationReads()
	if store == nil {
		// Before Nextcloud is asked anything.
		writeJSONError(w, http.StatusServiceUnavailable, tagIndexUnavailableMessage)
		return
	}
	entries, ok := s.exapp.resolveVisibleMeetings(r.Context(), w, s.client, caller, s.logger, "annotations tags")
	if !ok {
		return
	}
	visible := visibleOpusNames(entries)

	tags, err := store.Vocabulary(r.Context(), visible)
	if err != nil {
		s.logf("annotations tags: vocabulary caller=%s: %v", caller, err)
		writeJSONError(w, http.StatusBadGateway, tagIndexUnreadableMessage)
		return
	}
	coverage, err := store.Coverage(r.Context(), visible)
	if err != nil {
		s.logf("annotations tags: coverage caller=%s: %v", caller, err)
		writeJSONError(w, http.StatusBadGateway, tagIndexUnreadableMessage)
		return
	}
	byMeeting, err := store.meetingTags(r.Context(), visible)
	if err != nil {
		s.logf("annotations tags: meeting tags caller=%s: %v", caller, err)
		writeJSONError(w, http.StatusBadGateway, tagIndexUnreadableMessage)
		return
	}
	meetings := []meetingTagsEntry{}
	for _, entry := range entries {
		if marks := byMeeting[entry.opusName]; len(marks) > 0 {
			meetings = append(meetings, meetingTagsEntry{MeetingID: entry.id, Tags: marks})
		}
	}
	s.withStyles(tags)
	writeJSON(w, http.StatusOK, tagVocabularyResponse{Tags: tags, Meetings: meetings, Coverage: coverage})
}
