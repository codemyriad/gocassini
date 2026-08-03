package operator

// sealTask is one job's seal work: pack this attempt's `.meeting` bundle into
// the attempt's immutable `.opus`. The meeting path travels with the task so a
// restart can rebuild it from the DB (ListQueuedSealTasks) rather than infer it.
type sealTask struct {
	JobID               string
	AttemptNumber       int
	ArtifactMeetingPath string
}
