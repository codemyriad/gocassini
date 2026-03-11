package cassini

import "strings"

const (
	bundleStatePreparing = "preparing"
	bundleStateReady     = "ready"
	bundleStateFailed    = "failed"
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
