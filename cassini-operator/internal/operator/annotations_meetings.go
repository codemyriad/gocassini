package operator

import "net/http"

// readMeeting answers GET annotations/meetings/<id>: the meeting's annotations,
// read AS THE CALLER. Unit 3 of D-737 replaces this stub — design doc §3.
func (s *annotationService) readMeeting(w http.ResponseWriter, r *http.Request, caller, meetingID string) {
	writeJSONError(w, http.StatusNotImplemented, "not implemented yet")
}

// writeMeeting answers POST annotations/meetings/<id>: apply one batch of ops
// with a conditional write, retrying on 412. Unit 3 of D-737 replaces this stub.
func (s *annotationService) writeMeeting(w http.ResponseWriter, r *http.Request, caller, meetingID string) {
	writeJSONError(w, http.StatusNotImplemented, "not implemented yet")
}
