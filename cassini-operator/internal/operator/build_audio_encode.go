package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
// "" when no published audio has an encode to keep, so the build takes the
// current default.
//
// Marks people make on a published meeting are bound to its Opus packets, and
// a rerun re-encodes the audio from the same capture. Same capture plus same
// encode gives the same packets, so the marks carry over to the rebuilt file;
// a different encode gives different packets and the publish discards them
// ("different audio"). A rerun therefore repeats the encode of the meeting it
// replaces, which current/<job>.meeting records in its manifest: that bundle is
// replaced only when a publication succeeds. When the record cannot be read the
// rerun fails: guessing an encode could silently discard the marks.
//
// current/<job>.meeting can also be absent without anything being wrong:
// nothing has been published yet, or the publication carried no audio (a
// transcription-only `.json`, which is also what a job that deletes its source
// media publishes, and such a job is never rebuilt). Neither has packets to
// keep. A published audio meeting whose local copy is gone (the retention
// policy expired it) has no readable record, so its rerun is refused too.
func (rt *Runtime) rebuildAudioEncodePolicy(jobID string) (string, error) {
	meetingPath := canonicalMeetingPath(rt.cfg.WorkRoot, jobID)
	if _, err := os.Stat(meetingPath); os.IsNotExist(err) {
		return rt.audioEncodeWithoutPublishedMeeting(jobID)
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

// audioEncodeWithoutPublishedMeeting decides a rebuild of jobID when
// current/<job>.meeting does not exist (see rebuildAudioEncodePolicy).
func (rt *Runtime) audioEncodeWithoutPublishedMeeting(jobID string) (string, error) {
	var published int
	err := rt.store.db.QueryRow(`SELECT published_attempt FROM artifact_availability WHERE job_id=?`, jobID).Scan(&published)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && published == 0) {
		return "", nil // nothing published yet
	}
	if err != nil {
		return "", fmt.Errorf("read whether the meeting was published, which decides the audio encode a rerun must repeat: %w", err)
	}
	attempts, err := rt.store.ListJobAttempts(context.Background(), jobID)
	if err != nil {
		return "", fmt.Errorf("read the published attempt, which decides the audio encode a rerun must repeat: %w", err)
	}
	for _, a := range attempts {
		if a.AttemptNumber == published && a.ArtifactOpusPath != nil && meetingExtension(*a.ArtifactOpusPath) == ".json" {
			return "", nil // transcription-only: no published audio, no packets to keep
		}
	}
	return "", fmt.Errorf("read the audio encode of the published meeting, which a rerun must repeat to keep its marks: its local copy %s is gone (expired by the retention policy?)", canonicalMeetingPath(rt.cfg.WorkRoot, jobID))
}
