package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Backfilling from the published archive, when the operator's own copy is gone.
//
// WHY THIS EXISTS, MEASURED RATHER THAN ANTICIPATED
//
// Backfill's first source is current/<job>.meeting, which carries the
// producer's segments and needs no download. On the demo archive that indexed
// 4 meetings out of 30: the volume held 5 bundles while Nextcloud held 30
// recordings, and 25 meetings had no job row at all
// ("read job: sql: no rows in result set").
//
// That is not a bug, it is the durability asymmetry the D-631 assessment
// documented: Nextcloud Files is the store of record and the operator's volume
// is not. Re-registering the ExApp against a different daemon gives it a new
// empty volume while the archive stays exactly where it was. Any backfill that
// reads only local state therefore covers whatever survived the last such
// event, which on a real deployment can be a small minority.
//
// So when the local copy cannot be used, the recording itself is read. It is
// the one artifact that is definitely there — the caller can play it.
//
// WHAT IS LOST, AND WHY THAT IS THE RIGHT TRADE
//
// The published .opus carries the raw word transcript and no segmentation, so
// rows come from the wall-clock windowing rather than the producer's own
// utterances. Coarser references, recorded as row_source='words' so an answer
// never implies a precision it does not have.
//
// The earlier reasoning — "gigabytes of transfer to recover a worse index than
// the one already on local disk" — had the comparison wrong. It is not a worse
// index versus a better one. For 25 of 30 meetings it is a worse index versus
// NO index, and a coarse reference to a real moment beats a meeting that
// search silently cannot see.

// searchArchiveCopy is what reading one published recording yields.
type searchArchiveCopy struct {
	Words []searchTranscriptWord
	// Digest is the sha256 of the bytes read: the container as it stands in the
	// archive, which every mark commit changes.
	Digest string
	// AudioDigest is the copy's own integrity.opusAudioSha256 — identity, which
	// no mark commit changes (D-737). Empty when the CLI could not report it, in
	// which case nothing is verified by it.
	AudioDigest string
}

// searchArchiveReader reads one published recording.
//
// A function rather than a concrete type so backfill stays testable without a
// Nextcloud, and so a deployment with no archive access simply passes nil.
type searchArchiveReader func(ctx context.Context, opusName string) (searchArchiveCopy, error)

// searchDeliveredStateReader reports what the archive itself holds for one
// recording: whether the published leaf exists, and the sha256 the delivery
// stamped onto it. An empty digest with exists=true is a recording uploaded
// before deliveries carried checksums — present, but with no recorded digest
// to verify a local copy against.
type searchDeliveredStateReader func(ctx context.Context, opusName string) (digest string, exists bool, err error)

// archiveDeliveredState reads the published leaf's own checksum in one Depth-0
// PROPFIND, as the recordings owner.
//
// The archive's record, and deliberately NOT the job database's: the job's
// artifact_opus_sha256 is written by the seal stage in the same step that
// promotes current/, so the two sides could only ever agree — a rerun that
// sealed and then failed to publish moves the file and the digest to the new
// attempt together, and comparing them would "verify" the exact divergence
// the check exists to catch. The upload path stamps OC-Checksum onto every
// delivered file (publish_sink_nextcloud.go), and Nextcloud serves it back
// here, so this digest describes the bytes callers can actually play.
func (c ExAppConfig) archiveDeliveredState() searchDeliveredStateReader {
	client := &http.Client{Timeout: ncFilesUploadTimeout}
	return func(ctx context.Context, opusName string) (string, bool, error) {
		state, err := c.davPropfindLeafState(ctx, client, ncRecordingsOwner, ncArchiveRoot()+"/meetings/"+opusName)
		if err != nil {
			return "", false, err
		}
		if !state.Exists {
			return "", false, nil
		}
		return strings.ToLower(strings.TrimSpace(state.Checksum)), true, nil
	}
}

// archiveOpusReader reads a recording out of Nextcloud Files as the recordings
// owner and asks the cassini CLI for its transcript.
//
// Owner rather than caller: this is an administrative rebuild of an index whose
// per-request answers are filtered by the caller's own visibility. Reading as
// the owner here is what the publish path already does for the catalog, and the
// index it fills grants nobody anything.
func (c ExAppConfig) archiveOpusReader(cassiniBin, workDir string) searchArchiveReader {
	client := &http.Client{Timeout: archiveOpusReadTimeout}
	return func(ctx context.Context, opusName string) (searchArchiveCopy, error) {
		tmp, err := os.MkdirTemp(workDir, "search-backfill-")
		if err != nil {
			return searchArchiveCopy{}, fmt.Errorf("temp dir: %w", err)
		}
		defer os.RemoveAll(tmp)

		local := filepath.Join(tmp, opusName)
		digest, err := c.downloadArchiveOpus(ctx, client, opusName, local)
		if err != nil {
			return searchArchiveCopy{}, err
		}
		words, err := transcriptWordsFromOpus(ctx, cassiniBin, local)
		if err != nil {
			return searchArchiveCopy{}, err
		}
		read := searchArchiveCopy{Words: words, Digest: digest}
		// The audio digest is what lets a local bundle be verified against an
		// archive copy that has since been marked (D-737): the marks moved the
		// container digest and left the audio alone. A copy whose audio digest
		// cannot be read is simply not verified by it — the words above still
		// index it, exactly as before marks existed.
		if shown, err := runAnnotateShow(ctx, cassiniBin, local); err == nil {
			read.AudioDigest = strings.ToLower(strings.TrimSpace(shown.AudioOpusSHA256))
		}
		return read, nil
	}
}

// archiveOpusReadTimeout bounds one recording's download. Generous: a long
// meeting over a slow link is normal, and this runs in a hand-started command
// with somebody watching.
const archiveOpusReadTimeout = 10 * time.Minute

// downloadArchiveOpus streams one recording to disk, digesting as it goes.
//
// Streamed rather than read into memory: a long meeting is tens of megabytes,
// and a backfill walks the whole archive. The digest is computed from the same
// bytes that are written, so what gets recorded is the artifact that was
// actually indexed rather than one the caller was told about.
func (c ExAppConfig) downloadArchiveOpus(ctx context.Context, client *http.Client, opusName, destPath string) (string, error) {
	digest, status, err := c.davDownloadFile(ctx, client, ncRecordingsOwner, ncArchiveRoot()+"/meetings/"+opusName, destPath)
	// Branch on STATUS first: absence is a statement about the archive, and
	// reading it off the error text would report a missing recording as a
	// transport failure the operator would retry forever.
	if status == http.StatusNotFound {
		return "", fmt.Errorf("%s is not in the archive", opusName)
	}
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", opusName, err)
	}
	return digest, nil
}

// transcriptWordsFromOpus asks the cassini CLI to read the transcript back out
// of a published recording.
//
// Shelling out rather than reimplementing: `cassini inspect --transcript` is
// the inverse of the transcript packer and already owns the OpusTags layout,
// the chunked payload reassembly and the gzip decode. A second decoder in the
// operator would be a second thing to keep in step with the format.
//
// Its output groups words into runs by speaker. Those runs are NOT used as
// segments — they are a re-derivation, and a speaker who talks for two minutes
// uninterrupted becomes one row. Only the words are taken, and the windowing
// turns them into rows the same way it does anywhere else.
func transcriptWordsFromOpus(ctx context.Context, cassiniBin, path string) ([]searchTranscriptWord, error) {
	if strings.TrimSpace(cassiniBin) == "" {
		return nil, fmt.Errorf("no cassini binary configured to read %s", filepath.Base(path))
	}
	cmd := exec.CommandContext(ctx, cassiniBin, "inspect", "--transcript", path)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("read transcript from %s: %s", filepath.Base(path), detail)
	}

	var doc struct {
		Segments []struct {
			Speaker string `json:"speaker"`
			Words   []struct {
				Text    string `json:"text"`
				StartMS int64  `json:"startMs"`
				EndMS   int64  `json:"endMs"`
			} `json:"words"`
		} `json:"segments"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &doc); err != nil {
		return nil, fmt.Errorf("parse transcript from %s: %w", filepath.Base(path), err)
	}
	var words []searchTranscriptWord
	for _, segment := range doc.Segments {
		for _, word := range segment.Words {
			words = append(words, searchTranscriptWord{
				SpeakerID: segment.Speaker,
				StartMS:   word.StartMS,
				EndMS:     word.EndMS,
				Text:      word.Text,
			})
		}
	}
	return words, nil
}
