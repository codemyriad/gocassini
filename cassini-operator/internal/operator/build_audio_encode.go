package operator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Audio encode policies of `cassini build --audio-encode` (D-850). The operator
// only ever repeats a policy a previous build recorded; it never picks one.
const (
	// audioEncodeLegacy is what every bundle without an audioEncode record was
	// built with: the fixed 64 kb/s encode from before D-850.
	audioEncodeLegacy = "fixed-64k"
)

// rebuildAudioEncodePolicy names the audio encode a rerun of jobID must use, or
// "" for a first build, which takes the current default.
//
// Marks people make on a published meeting are bound to its Opus packets, and
// a rerun re-encodes the audio from the same capture. Same capture plus same
// encode gives the same packets, so the marks carry over to the rebuilt file;
// a different encode gives different packets and the publish discards them
// ("different audio"). A rerun therefore repeats the encode of the meeting it
// replaces, which current/<job>.meeting — never pruned, and only ever replaced
// by a successful build — records in its manifest.
func (rt *Runtime) rebuildAudioEncodePolicy(jobID string) string {
	meetingPath := canonicalMeetingPath(rt.cfg.WorkRoot, jobID)
	if _, err := os.Stat(meetingPath); os.IsNotExist(err) {
		return "" // nothing was built (so nothing published) yet
	}
	manifestPath := filepath.Join(meetingPath, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if os.IsNotExist(err) {
		// Every build since the audio encode became a choice writes a
		// manifest; a meeting without one predates it.
		return audioEncodeLegacy
	}
	if err != nil {
		rt.logger.Printf("rerun audio encode: cannot read %s (%v); rebuilding with the default encode, which may not carry this meeting's marks", manifestPath, err)
		return ""
	}
	var manifest struct {
		AudioEncode *struct {
			Policy string `json:"policy"`
		} `json:"audioEncode"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		rt.logger.Printf("rerun audio encode: cannot parse %s (%v); rebuilding with the default encode, which may not carry this meeting's marks", manifestPath, err)
		return ""
	}
	if manifest.AudioEncode == nil {
		return audioEncodeLegacy
	}
	return strings.TrimSpace(manifest.AudioEncode.Policy)
}
