package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"
)

// This evidence travels with the existing admin-only job detail endpoint.
// Receiving bytes, accepting a capture, and using it are distinct facts.
type sourceAudioUpload struct {
	CaptureID       string     `json:"capture_id,omitempty"`
	SessionIDs      []string   `json:"session_ids,omitempty"`
	CallStartMS     int64      `json:"call_start_ms"`
	CallEndMS       int64      `json:"call_end_ms"`
	ExclusionReason string     `json:"exclusion_reason,omitempty"`
	Owner           string     `json:"owner"`
	Segments        int        `json:"segments"`
	Bytes           int64      `json:"bytes"`
	Complete        bool       `json:"complete"`
	ReceivedAt      *time.Time `json:"received_at,omitempty"`
}

type sourceAudioUse struct {
	SessionID        string   `json:"session_id,omitempty"`
	Owner            string   `json:"owner"`
	Segments         int      `json:"segments"`
	Placed           int      `json:"placed"`
	Skipped          int      `json:"skipped"`
	SplicedMS        int64    `json:"spliced_ms"`
	MixSpliced       bool     `json:"mix_spliced"`
	TranscriptSource string   `json:"transcript_source,omitempty"`
	MixSkipReason    string   `json:"mix_skip_reason,omitempty"`
	Rejections       []string `json:"rejections,omitempty"`
}

type sourceAudioAttempt struct {
	Attempt      int              `json:"attempt"`
	Available    bool             `json:"available"`
	Error        string           `json:"error,omitempty"`
	Participants []sourceAudioUse `json:"participants"`
}

type jobSourceAudio struct {
	Expected          []captureExpectation `json:"expected"`
	WaitUntil         *time.Time           `json:"wait_until,omitempty"`
	CollectionEnabled bool                 `json:"collection_enabled"`
	IngestEnabled     bool                 `json:"ingest_enabled"`
	Uploads           []sourceAudioUpload  `json:"uploads"`
	Receiving         []sourceAudioUpload  `json:"receiving"`
	Attempts          []sourceAudioAttempt `json:"attempts"`
	Error             string               `json:"error,omitempty"`
}

type captureTransfer struct {
	owner, room    string
	captureID      string
	startMS, endMS int64
	bytes          atomic.Int64
}

type captureCountingReader struct {
	io.Reader
	bytes *atomic.Int64
}

func (r *captureCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes.Add(int64(n))
	return n, err
}

func readSourceAudioAttempt(number int, meetingPath string) sourceAudioAttempt {
	result := sourceAudioAttempt{Attempt: number, Participants: []sourceAudioUse{}}
	if meetingPath == "" {
		return result
	}
	file, err := os.Open(filepath.Join(meetingPath, "manifest.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			result.Error = "Could not read this attempt's source-audio evidence."
		}
		return result
	}
	defer file.Close()
	var manifest struct {
		Provenance struct {
			SourceAudio []sourceAudioUse `json:"sourceAudio"`
		} `json:"provenance"`
	}
	if err := json.NewDecoder(io.LimitReader(file, 4<<20)).Decode(&manifest); err != nil {
		result.Error = "This attempt's source-audio evidence is unreadable."
		return result
	}
	result.Available = true
	if manifest.Provenance.SourceAudio != nil {
		result.Participants = manifest.Provenance.SourceAudio
	}
	return result
}

// Heavy attempt bundles are pruned after publishing. Keep the small evidence
// with the attempt logs, which survive retention and subsequent rebuilds.
func saveSourceAudioEvidence(workRoot, jobID string, number int, meetingPath string) error {
	evidence := readSourceAudioAttempt(number, meetingPath)
	if !evidence.Available {
		if evidence.Error != "" {
			return fmt.Errorf("%s", evidence.Error)
		}
		return nil
	}
	dir := attemptLogsDir(workRoot, jobID, number)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}
	body, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "source-audio.json"), body, 0640)
}

func sourceAudioEvidence(workRoot, jobID string, number int, meetingPath, canonical string) sourceAudioAttempt {
	empty := sourceAudioAttempt{Attempt: number, Participants: []sourceAudioUse{}}
	file, err := os.Open(filepath.Join(attemptLogsDir(workRoot, jobID, number), "source-audio.json"))
	if err == nil {
		defer file.Close()
		var evidence sourceAudioAttempt
		if err := json.NewDecoder(io.LimitReader(file, 4<<20)).Decode(&evidence); err == nil && evidence.Attempt == number {
			return evidence
		}
		empty.Error = "Saved source-audio evidence is unreadable."
		return empty
	}
	if !os.IsNotExist(err) {
		empty.Error = "Could not read saved source-audio evidence."
		return empty
	}
	evidence := readSourceAudioAttempt(number, meetingPath)
	if evidence.Available || evidence.Error != "" || canonical == "" {
		return evidence
	}
	// Older builds predate the saved evidence. Only borrow the canonical bundle
	// when its own lineage identifies this exact job and attempt; never label a
	// newer build's audio as belonging to an older one. Recheck after reading in
	// case an atomic promotion replaced the canonical directory in the meantime.
	belongs := func() bool {
		manifest, ok, err := LoadMeetingBundleManifest(canonical)
		return err == nil && ok && manifest.JobID == jobID && manifest.AttemptNumber == number
	}
	if !belongs() {
		return evidence
	}
	evidence = readSourceAudioAttempt(number, canonical)
	if !belongs() {
		return empty
	}
	return evidence
}

func (rt *Runtime) sourceAudioForJob(ctx context.Context, job Job, attempts []JobAttempt) jobSourceAudio {
	result := jobSourceAudio{CollectionEnabled: sourceCaptureEnabled(), IngestEnabled: sourceAudioIngestEnabled(),
		Uploads: []sourceAudioUpload{}, Receiving: []sourceAudioUpload{}, Attempts: []sourceAudioAttempt{}}
	set, err := rt.sourceCaptureSetForJob(ctx, job.ID)
	if err != nil {
		result.Error = "Could not inspect stored source-audio uploads."
	} else if set.Uploads != nil {
		result.Uploads = set.Uploads
	}
	expected, deadline, expectedErr := rt.captureExpectationsForJob(ctx, job.ID, result.Uploads)
	result.Expected = expected
	if result.CollectionEnabled && result.IngestEnabled {
		result.WaitUntil = deadline
	}
	if expectedErr != nil {
		result.Error = "Could not inspect expected participant captures."
	}
	binding, bound := rt.talkBindingForJob(job.ID)
	window, windowErr := rt.store.RecordingWindowForJob(ctx, job.ID)
	if bound && windowErr == nil {
		rt.captureTransfers.Range(func(key, _ any) bool {
			upload := key.(*captureTransfer)
			if upload.room == binding.RoomToken && captureWindowFit(upload.startMS, upload.endMS, window.StartMS, window.EndMS) != captureWindowApart {
				result.Receiving = append(result.Receiving, sourceAudioUpload{Owner: upload.owner, CaptureID: upload.captureID, Bytes: upload.bytes.Load()})
			}
			return true
		})
	}
	sort.Slice(result.Receiving, func(i, j int) bool { return result.Receiving[i].Owner < result.Receiving[j].Owner })
	for _, attempt := range attempts {
		meeting := ""
		if attempt.ArtifactMeetingPath != nil {
			meeting = *attempt.ArtifactMeetingPath
		}
		canonical := ""
		if job.ArtifactMeetingPath != nil {
			canonical = *job.ArtifactMeetingPath
		}
		result.Attempts = append(result.Attempts, sourceAudioEvidence(rt.cfg.WorkRoot, job.ID, attempt.AttemptNumber, meeting, canonical))
	}
	return result
}
