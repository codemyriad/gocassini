package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/oklog/ulid/v2"
)

// recordedAtUtc is the manifest and catalog member that says when a meeting
// happened: the instant at playback position 0 of the file's audio, so a
// word's wall-clock time is recordedAtUtc + startMs. It is RFC 3339 UTC
// ending in Z. dateLabel stays the zone-less label old clients read; a reader
// that knows recordedAtUtc sorts, filters and groups on it instead.
const recordedAtUTCField = "recordedAtUtc"

var recordedAtUTCPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`)

// recordedAtUTCLayout is how the operator writes an instant it derived:
// millisecond precision, which is what every source it has carries.
const recordedAtUTCLayout = "2006-01-02T15:04:05.000Z"

// parseRecordedAtUTC reads a recordedAtUtc value, refusing anything that is
// not in the contract's shape (an offset, a missing Z, a bare date).
func parseRecordedAtUTC(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if !recordedAtUTCPattern.MatchString(value) {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return at.UTC(), true
}

func formatRecordedAtUTC(at time.Time) string {
	return at.UTC().Format(recordedAtUTCLayout)
}

// normalizedRecordedAtUTC is value in the operator's fixed layout, or "" when
// it is not a valid recordedAtUtc. The index column holds this form, so a
// string comparison of two columns is a comparison of instants.
func normalizedRecordedAtUTC(value string) string {
	at, ok := parseRecordedAtUTC(value)
	if !ok {
		return ""
	}
	return formatRecordedAtUTC(at)
}

// entryRecordedAtUTC is the valid recordedAtUtc a catalog entry carries, or "".
func entryRecordedAtUTC(entry json.RawMessage) string {
	var probe struct {
		RecordedAtUTC string `json:"recordedAtUtc"`
	}
	if json.Unmarshal(entry, &probe) != nil {
		return ""
	}
	if _, ok := parseRecordedAtUTC(probe.RecordedAtUTC); !ok {
		return ""
	}
	return strings.TrimSpace(probe.RecordedAtUTC)
}

// withEntryRecordedAtUTC is entry with recordedAtUtc set, every other member
// kept as stored.
func withEntryRecordedAtUTC(entry json.RawMessage, value string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(entry, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("catalog entry is not an object")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	fields[recordedAtUTCField] = encoded
	return json.Marshal(fields)
}

// meetingSortKey is the string the newest-first orderings compare. An entry
// with an instant sorts by it, written as a UTC wall clock so it lines up with
// the zone-less dateLabel of an entry without one (an ExApp label is UTC
// digits). The mixed order is stable and degrades to today's for old entries.
func meetingSortKey(dateLabel, recordedAtUTC string) string {
	if at, ok := parseRecordedAtUTC(recordedAtUTC); ok {
		return at.Format("2006-01-02 15:04:05.000")
	}
	return strings.TrimSpace(dateLabel)
}

// meetingMetadataSortKeySQL is meetingSortKey over meeting_metadata's columns;
// recorded_at_utc is always in recordedAtUTCLayout or empty.
const meetingMetadataSortKeySQL = `CASE WHEN recorded_at_utc <> '' THEN replace(substr(recorded_at_utc, 1, 23), 'T', ' ') ELSE date_label END`

// catalogEntryWhen places an entry in time for orderings: its instant when it
// has one, otherwise its dateLabel read as a wall clock with no zone applied
// (as every reader did before recordedAtUtc existed).
//
// Placing a label next to an instant needs a zone for the label, and the
// server has no reader. It reads the digits as UTC, which is what an ExApp
// label's digits are. The viewer and the CLI read them in the reader's zone,
// so in a set mixing the two kinds, entries within the reader's offset of each
// other can order differently there. The date filter does not use this: it
// compares a label against the bound's own digits (applyMeetingsListFilter).
func catalogEntryWhen(dateLabel, recordedAtUTC string) (time.Time, bool) {
	if at, ok := parseRecordedAtUTC(recordedAtUTC); ok {
		return at, true
	}
	return parseMeetingsListDateLabel(dateLabel)
}

// recordedAtSource names where a derived instant came from, best first.
type recordedAtSource string

const (
	// recordedAtFromRecording is the run bundle's recording.mkv: the wall
	// clock of the packet that sits at position 0 of the merged timeline.
	recordedAtFromRecording recordedAtSource = "recording"
	// recordedAtFromSession is session.json started_wall_utc: when the
	// recorder started, typically ~0.4-1.6 s before the first packet.
	recordedAtFromSession recordedAtSource = "session"
	// recordedAtFromJob is jobs.record_started_at: just before the recorder
	// process was launched.
	recordedAtFromJob recordedAtSource = "job"
	// recordedAtFromJobID is the job's ULID: when the recording was asked for.
	recordedAtFromJobID recordedAtSource = "job-id"
)

var recordedAtSources = []recordedAtSource{recordedAtFromRecording, recordedAtFromSession, recordedAtFromJob, recordedAtFromJobID}

// recordingAnchor is one stream of a run bundle's recording.mkv: when its
// first packet arrived (first_packet_wall_ms) and where that packet sits on
// the merged timeline (offset_seconds + source_start_seconds, as the remux
// planned it).
type recordingAnchor struct {
	FirstPacketWallMS int64
	TimelineSeconds   float64
}

// recordedAtEvidence is everything the operator still holds about when one
// of its recordings started.
type recordedAtEvidence struct {
	Anchors               []recordingAnchor
	SessionStartedWallUTC string
	RecordStartedAt       string
	JobID                 string
}

// earliestPlausibleRecording rejects zero and garbage clocks.
var earliestPlausibleRecording = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func plausibleRecordingInstant(at time.Time) bool {
	return !at.IsZero() && !at.Before(earliestPlausibleRecording)
}

// chooseRecordedAt picks the best proxy for playback position 0 of a past
// recording's audio, in a fixed precedence: the recording's own timeline
// anchors, then the recorder's session start, then the job's record start,
// then the job id's timestamp. Each later source is a coarser stand-in
// for the same instant; measured on production they are within seconds of
// each other, except where the first packet arrived late.
func chooseRecordedAt(ev recordedAtEvidence) (time.Time, recordedAtSource, bool) {
	if at, ok := recordingPositionZero(ev.Anchors); ok {
		return at, recordedAtFromRecording, true
	}
	if at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(ev.SessionStartedWallUTC)); err == nil && plausibleRecordingInstant(at) {
		return at.UTC().Truncate(time.Millisecond), recordedAtFromSession, true
	}
	if at := parseUTCTime(ev.RecordStartedAt); plausibleRecordingInstant(at) {
		return at.Truncate(time.Millisecond), recordedAtFromJob, true
	}
	if id, err := ulid.ParseStrict(strings.TrimSpace(ev.JobID)); err == nil {
		if at := ulid.Time(id.Time()).UTC(); plausibleRecordingInstant(at) {
			return at, recordedAtFromJobID, true
		}
	}
	return time.Time{}, "", false
}

// recordingPositionZero is the wall clock of the merged timeline's origin: the
// stream planned at the smallest timeline position, less that position.
func recordingPositionZero(anchors []recordingAnchor) (time.Time, bool) {
	best := -1
	for i, a := range anchors {
		if a.FirstPacketWallMS <= 0 || math.IsNaN(a.TimelineSeconds) || math.IsInf(a.TimelineSeconds, 0) || a.TimelineSeconds < 0 {
			continue
		}
		if best < 0 || a.TimelineSeconds < anchors[best].TimelineSeconds {
			best = i
		}
	}
	if best < 0 {
		return time.Time{}, false
	}
	a := anchors[best]
	at := time.UnixMilli(a.FirstPacketWallMS - int64(math.Round(a.TimelineSeconds*1000))).UTC()
	if !plausibleRecordingInstant(at) {
		return time.Time{}, false
	}
	return at, true
}

// recordingAnchorProbe reads the stream anchors of one recording.mkv.
type recordingAnchorProbe func(ctx context.Context, mkv string) ([]recordingAnchor, error)

const recordingAnchorProbeTimeout = 30 * time.Second

// ffprobeRecordingAnchors reads the stream tags the remux writes
// (pkg/core/remux/metadata.go): first_packet_wall_ms, offset_seconds and
// source_start_seconds.
func ffprobeRecordingAnchors(ctx context.Context, mkv string) ([]recordingAnchor, error) {
	ctx, cancel := context.WithTimeout(ctx, recordingAnchorProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-show_entries", "stream_tags=first_packet_wall_ms,offset_seconds,source_start_seconds",
		"-of", "json", mkv)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe %s: %w", filepath.Base(mkv), err)
	}
	return parseRecordingAnchors(out)
}

func parseRecordingAnchors(raw []byte) ([]recordingAnchor, error) {
	var probe struct {
		Streams []struct {
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	var anchors []recordingAnchor
	for _, s := range probe.Streams {
		tags := map[string]string{}
		for k, v := range s.Tags {
			tags[strings.ToLower(k)] = strings.TrimSpace(v)
		}
		wall, err := strconv.ParseInt(tags["first_packet_wall_ms"], 10, 64)
		if err != nil || wall <= 0 {
			continue
		}
		offset, err := strconv.ParseFloat(tags["offset_seconds"], 64)
		if err != nil {
			continue
		}
		start := 0.0
		if v := tags["source_start_seconds"]; v != "" {
			if start, err = strconv.ParseFloat(v, 64); err != nil {
				continue
			}
		}
		anchors = append(anchors, recordingAnchor{FirstPacketWallMS: wall, TimelineSeconds: offset + start})
	}
	return anchors, nil
}

// recordedAtEvidenceFor gathers what the run bundle and the jobs row say about
// one recording. Missing or unreadable pieces are simply absent: the chooser
// falls through to the next source.
func (rt *Runtime) recordedAtEvidenceFor(ctx context.Context, jobID, recordStartedAt string) recordedAtEvidence {
	ev := recordedAtEvidence{JobID: jobID, RecordStartedAt: recordStartedAt}
	if rt == nil || strings.TrimSpace(rt.cfg.WorkRoot) == "" || !validArtifactJob(jobID) {
		return ev
	}
	run := canonicalRunPath(rt.cfg.WorkRoot, jobID)
	if mkv := filepath.Join(run, "recording.mkv"); fileExists(mkv) {
		probe := rt.recordingAnchorProbe
		if probe == nil {
			probe = ffprobeRecordingAnchors
		}
		if anchors, err := probe(ctx, mkv); err == nil {
			ev.Anchors = anchors
		} else if rt.logger != nil {
			rt.logger.Printf("recorded-at: read recording anchors job=%s: %v", jobID, err)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(run, "session", "session.json")); err == nil {
		var session struct {
			StartedWallUTC string `json:"started_wall_utc"`
		}
		if json.Unmarshal(raw, &session) == nil {
			ev.SessionStartedWallUTC = session.StartedWallUTC
		}
	}
	return ev
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
