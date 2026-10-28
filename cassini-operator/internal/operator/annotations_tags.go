package operator

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
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
	return validateTagParam(query.Get("tag"))
}

// validateTagParam is parseTagParam for a value that did not arrive in a query
// string.
func validateTagParam(raw string) (string, error) {
	tag := strings.TrimSpace(raw)
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
	vocabulary, err := s.tagVocabulary(r.Context(), caller)
	if err != nil {
		writeReadFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, vocabulary)
}

// tagVocabulary is the tag vocabulary across the caller's visible meetings,
// with coverage. Every failure is a *readFailure.
func (s *annotationService) tagVocabulary(ctx context.Context, caller string) (tagVocabularyResponse, error) {
	// Search's budget: each answer costs Nextcloud a PROPFIND and a catalog GET.
	if allowed, wait := s.rt.searchLimiter.allow(caller); !allowed {
		return tagVocabularyResponse{}, readRateLimited(wait,
			"too many requests; each one asks Nextcloud what you may read, so they are rate limited — retry shortly")
	}
	store := s.rt.annotationReads()
	if store == nil {
		// Before Nextcloud is asked anything.
		return tagVocabularyResponse{}, readFail(http.StatusServiceUnavailable, tagIndexUnavailableMessage)
	}
	entries, err := s.exapp.visibleMeetings(ctx, s.client, caller, s.logger, "annotations tags")
	if err != nil {
		return tagVocabularyResponse{}, err
	}
	visible := visibleOpusNames(entries)

	tags, err := store.Vocabulary(ctx, visible)
	if err != nil {
		s.logf("annotations tags: vocabulary caller=%s: %v", caller, err)
		return tagVocabularyResponse{}, readFail(http.StatusBadGateway, tagIndexUnreadableMessage)
	}
	coverage, err := store.Coverage(ctx, visible)
	if err != nil {
		s.logf("annotations tags: coverage caller=%s: %v", caller, err)
		return tagVocabularyResponse{}, readFail(http.StatusBadGateway, tagIndexUnreadableMessage)
	}
	byMeeting, err := store.meetingTags(ctx, visible)
	if err != nil {
		s.logf("annotations tags: meeting tags caller=%s: %v", caller, err)
		return tagVocabularyResponse{}, readFail(http.StatusBadGateway, tagIndexUnreadableMessage)
	}
	meetings := []meetingTagsEntry{}
	for _, entry := range entries {
		if marks := byMeeting[entry.opusName]; len(marks) > 0 {
			meetings = append(meetings, meetingTagsEntry{MeetingID: entry.id, Tags: marks})
		}
	}
	return tagVocabularyResponse{Tags: tags, Meetings: meetings, Coverage: coverage}, nil
}
