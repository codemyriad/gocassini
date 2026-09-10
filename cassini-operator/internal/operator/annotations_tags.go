package operator

import "net/http"

// serveTags answers GET annotations/tags: the tag vocabulary across the caller's
// visible meetings, with coverage. Unit 4 of D-737 replaces this stub, together
// with the store behind rt.annotations — design doc §3 and §4.
func (s *annotationService) serveTags(w http.ResponseWriter, r *http.Request, caller string) {
	writeJSONError(w, http.StatusNotImplemented, "not implemented yet")
}
