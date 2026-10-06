package operator

import (
	"path"
	"strings"
)

// Legacy database columns named opus_* hold portable meeting artefacts in either
// format. Keep their schema names so acknowledged annotation state is preserved.
func isMeetingFile(name string) bool {
	return strings.HasSuffix(name, ".opus") || strings.HasSuffix(name, ".json")
}
func meetingStem(name string) string {
	return strings.TrimSuffix(strings.TrimSuffix(name, ".opus"), ".json")
}
func meetingContentType(name string) string {
	if strings.HasSuffix(name, ".json") {
		return "application/json"
	}
	return ncRecordingsContentType
}
func meetingPath(audio, document string) string {
	if document != "" {
		return document
	}
	return audio
}
func meetingExtension(name string) string { return path.Ext(name) }
