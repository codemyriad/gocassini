package operator

import (
	"encoding/json"
	"fmt"
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
// replaces, which current/<job>.meeting (never pruned, and only ever replaced
// by a successful build) records in its manifest. When that record cannot be
// read the rerun fails: guessing an encode could silently discard the marks.
func (rt *Runtime) rebuildAudioEncodePolicy(jobID string) (string, error) {
	meetingPath := canonicalMeetingPath(rt.cfg.WorkRoot, jobID)
	if _, err := os.Stat(meetingPath); os.IsNotExist(err) {
		return "", nil // nothing was built (so nothing published) yet
	}
	manifestPath := filepath.Join(meetingPath, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if os.IsNotExist(err) {
		// Every build since the audio encode became a choice writes a
		// manifest; a meeting without one predates it.
		return audioEncodeLegacy, nil
	}
	if err != nil {
		return "", fmt.Errorf("read the audio encode of the published meeting, which a rerun must repeat to keep its marks: %w", err)
	}
	var manifest struct {
		AudioEncode *struct {
			Policy string `json:"policy"`
		} `json:"audioEncode"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", fmt.Errorf("read the audio encode of the published meeting, which a rerun must repeat to keep its marks: parse %s: %w", manifestPath, err)
	}
	if manifest.AudioEncode == nil {
		return audioEncodeLegacy, nil
	}
	policy := strings.TrimSpace(manifest.AudioEncode.Policy)
	if policy == "" {
		return "", fmt.Errorf("read the audio encode of the published meeting, which a rerun must repeat to keep its marks: %s records an audio encode without a policy", manifestPath)
	}
	return policy, nil
}
