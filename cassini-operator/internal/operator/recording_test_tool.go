package operator

// The test recording row.
//
// This tool was unreachable on every Nextcloud install: it demanded a test room
// be chosen first, and the row that offered that form sat behind checks that
// themselves needed the room. So the one check that exercises the whole path —
// Talk hands a call to Cassini, Cassini records it, the result publishes, a
// person plays it — could not be run at all.
//
// It is reachable now because Cassini creates its own room, and it appears only
// when a test can actually succeed: every link in the chain this exercises is
// a declared prerequisite, so the row waits instead of inviting an
// administrator to run a test that cannot work yet.
func (rt *Runtime) testRecordingRow(test readinessTest) readinessCheck {
	row := readinessCheck{ID: "test", Action: "test_recording"}
	switch {
	case test.PlaybackVerifiedAt != "":
		// Historical by nature, and passed on purpose: this is the only
		// evidence in the panel that a recording reached a listener. Holding it
		// at amber forever would make a healthy install permanently amber,
		// which is how people learn to ignore a colour.
		row.State, row.Code = "passed", "test_playback"
		row.Message = "A recording started in Talk was published, and its audio was confirmed by playing it."
		row.CheckedAt = test.PlaybackVerifiedAt
	case test.State == "failed":
		row.State, row.Code = "needs_action", "test_failed"
		// No "open it to see where it stopped": a failed job has no published
		// result to open, and the panel offers that link only once there is one.
		row.Message = "The test recording did not finish."
		if test.Stage != "" {
			row.Message = "The test recording did not finish; it stopped at " + test.Stage + "."
		}
		row.Steps = []readinessStep{{Label: "Cassini's log records why the recording stopped; the other checks here cover the causes it can detect"}}
	case test.Published:
		row.State, row.Code = "not_verified", "test_awaiting_playback"
		row.Message = "The test recording was published. Playing it is what confirms the audio arrived."
	case test.StartedAt != "":
		row.State, row.Code = "not_verified", "test_in_progress"
		row.Message = "Cassini is waiting for a recording started in the test room."
		if test.JobID != "" {
			row.Message = "A test recording is in progress."
			if test.Stage != "" {
				row.Message = "A test recording is in progress: " + test.Stage + "."
			}
		}
	default:
		// not_verified, not needs_action: with every prerequisite passing,
		// recording is very likely fine. This row offers proof, it does not
		// report a fault.
		row.State, row.Code = "not_verified", "test_not_run"
		row.Message = "Nothing has been recorded through Talk yet. A short test recording is what proves the whole path, from a call to audio you can play."
	}
	return row
}
