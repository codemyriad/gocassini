package operator

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

// The tag read surface (D-737): the vocabulary, and the `tag` parameter search
// and the meeting list narrow by. Both answer from the projection
// (annotations_store.go), and both take the caller's visible set exactly as
// search takes it — resolveVisibleMeetings, the one access-control path — so a
// tag carried only by a meeting the caller cannot open never appears in any
// answer and never moves any count.
//
// The same discipline as search, and the same wording where the situation is
// the same: failure is loud, denial is empty, and coverage is part of every
// answer. A projection that is not open is 503 — "ask again later" — never an
// empty vocabulary, which would read as "nothing here is tagged".

// maxTagParamRunes bounds `tag=`. Neither a label (at most 64 characters) nor a
// tag id (at most 64) can be longer, so a longer value can match nothing — a
// 400 for the reason an inverted date range is one: an empty answer would read
// as "you have no meetings with that tag".
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
	// Coverage is how many of the caller's readable meetings the vocabulary
	// was counted over. Lower Indexed than Visible means some meetings' marks
	// could not be read, and the counts are partial.
	Coverage annotationCoverage `json:"coverage"`
}

// serveTags answers GET annotations/tags: the tag vocabulary across the caller's
// visible meetings, with coverage (design doc §3).
func (s *annotationService) serveTags(w http.ResponseWriter, r *http.Request, caller string) {
	store := s.rt.annotationReads()
	if store == nil {
		// Checked before Nextcloud is asked anything: a request that cannot be
		// answered must not first cost a PROPFIND and a catalog GET.
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
	writeJSON(w, http.StatusOK, tagVocabularyResponse{Tags: tags, Coverage: coverage})
}
