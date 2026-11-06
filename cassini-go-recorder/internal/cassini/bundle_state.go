package cassini

import "strings"

const (
	bundleStatePreparing = "preparing"
	bundleStateReady     = "ready"
	bundleStateFailed    = "failed"
	// bundleStateEmpty is a run that completed but captured no audio (every
	// participant muted or silent). It has no recording, so there is nothing
	// to build, transcribe or publish; the operator finishes the job as a
	// succeeded empty run. It is deliberately not "ready": a ready bundle
	// promises a recording to rerun from.
	bundleStateEmpty = "empty"
)

func bundleIsReady(state string) bool {
	state = strings.TrimSpace(strings.ToLower(state))
	return state == "" || state == bundleStateReady
}

func bundleStateSummary(state string, stage string) string {
	state = strings.TrimSpace(state)
	stage = strings.TrimSpace(stage)
	switch {
	case state == "" && stage == "":
		return ""
	case stage == "":
		return state
	case state == "":
		return stage
	default:
		return state + ":" + stage
	}
}
