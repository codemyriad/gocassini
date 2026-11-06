package operator

// Preserve original diagnostic text locally so filtering remains an export
// choice. Scrubbed exports use safeSupportEvent; original exports retain detail.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const supportJournalLimit = 1 << 20 // per generation: two per stream, at most 4 MiB on disk

// supportJobStateDetailMax bounds the error copied into each job_state event.
const supportJobStateDetailMax = 512

// Structured job, delivery and share events are the evidence a support bundle
// exists for, and they are rare. Free-text operator output is frequent. Each
// gets its own pair of generations so log volume can never rotate the
// structured evidence away.
const (
	supportStreamEvents   = "events"
	supportStreamOperator = "operator"
)

var supportStreams = []string{supportStreamEvents, supportStreamOperator}

func supportStreamFor(event string) string {
	if event == "operator_log" || event == "operator_failure" {
		return supportStreamOperator
	}
	return supportStreamEvents
}

func supportJournalFiles(stream string) (active, previous string) {
	return "diagnostics/" + stream + ".jsonl", "diagnostics/" + stream + ".previous.jsonl"
}

// supportJournalPaths lists every journal file, oldest generation first within
// each stream, relative to the work root.
func supportJournalPaths() []string {
	paths := make([]string, 0, 2*len(supportStreams))
	for _, stream := range supportStreams {
		active, previous := supportJournalFiles(stream)
		paths = append(paths, previous, active)
	}
	return paths
}

type supportEvent struct {
	At              string `json:"at"`
	Event           string `json:"event"`
	Job             string `json:"job,omitempty"`
	Attempt         int    `json:"attempt,omitempty"`
	Stage           string `json:"stage,omitempty"`
	State           string `json:"state,omitempty"`
	Reason          string `json:"reason,omitempty"`
	HTTP            int    `json:"http_status,omitempty"`
	OCS             int    `json:"ocs_status,omitempty"`
	Recipient       string `json:"recipient_kind,omitempty"`
	Count           int    `json:"count,omitempty"`
	Detail          string `json:"detail,omitempty"`
	RecipientID     string `json:"recipient_id,omitempty"`
	DetailTruncated bool   `json:"detail_truncated,omitempty"`
}

type supportJournal struct {
	mu       sync.Mutex
	workRoot string
	limit    int64
	failed   bool
}

func newSupportJournal(workRoot string) *supportJournal {
	j := &supportJournal{workRoot: workRoot, limit: supportJournalLimit}
	j.record(supportEvent{Event: "operator_started"})
	return j
}

// OpenRoot confines every open even if retention or another local process
// changes a path while it is being read. Reject links and non-regular files as
// well: a FIFO must never stall recording or an export.
func supportOpen(root *os.Root, name string, flags int) (*os.File, error) {
	parts := strings.Split(filepath.ToSlash(name), "/")
	for i := range parts {
		p := filepath.Join(parts[:i+1]...)
		info, err := root.Lstat(p)
		if err != nil {
			if os.IsNotExist(err) && i == len(parts)-1 && flags&os.O_CREATE != 0 {
				break
			}
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return nil, errors.New("unsafe diagnostic source")
		}
	}
	f, err := root.OpenFile(name, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("unsafe diagnostic source")
	}
	return f, nil
}

func (j *supportJournal) record(e supportEvent) {
	if j == nil || j.workRoot == "" {
		return
	}
	e.At = nowUTCString()
	var truncated bool
	e.Detail, truncated = supportTextPrefix(e.Detail, 16<<10)
	e.DetailTruncated = e.DetailTruncated || truncated
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.append(e); err != nil {
		j.failed = true // diagnostic I/O must never fail a recording
	}
}

func (j *supportJournal) append(e supportEvent) error {
	if err := os.MkdirAll(j.workRoot, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(j.workRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Mkdir("diagnostics", 0700); err != nil && !os.IsExist(err) {
		return err
	}
	raw, _ := json.Marshal(e)
	raw = append(raw, '\n')
	if int64(len(raw)) > j.limit {
		return errors.New("diagnostic event exceeds journal limit")
	}
	name, previousName := supportJournalFiles(supportStreamFor(e.Event))
	f, err := supportOpen(root, name, os.O_CREATE|os.O_APPEND|os.O_WRONLY)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if info.Size()+int64(len(raw)) > j.limit {
		// Copy the bounded generation before truncating. Root.Rename requires
		// Go 1.25; the production image still builds with Go 1.24.
		old, openErr := supportOpen(root, name, os.O_RDONLY)
		if openErr != nil {
			f.Close()
			return openErr
		}
		previous, openErr := supportOpen(root, previousName, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
		if openErr != nil {
			old.Close()
			f.Close()
			return openErr
		}
		_, copyErr := io.Copy(previous, io.LimitReader(old, j.limit))
		copyErr = errors.Join(copyErr, previous.Close(), old.Close())
		if copyErr != nil {
			f.Close()
			return copyErr
		}
		if err := f.Truncate(0); err != nil {
			f.Close()
			return err
		}
	}
	_, err = f.Write(raw)
	return errors.Join(err, f.Close())
}

// Tee operator output into the bounded journal. Do not discard unfamiliar
// messages at capture time: users may need their original wording for support.
func (j *supportJournal) Write(p []byte) (int, error) {
	text := strings.ToLower(string(p))
	e := supportEvent{Event: "operator_log", Detail: string(p), Reason: supportFailure(text)}
	if supportOperatorFailurePattern.MatchString(text) || (e.Reason != "unclassified_failure" && e.Reason != "none") {
		e.Event = "operator_failure"
	} else {
		e.Reason = "none"
	}
	j.record(e)
	return len(p), nil
}

func supportTextPrefix(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end], true
}

func supportEnum(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return candidate
		}
	}
	return "unknown"
}

func supportStage(value string) string {
	return supportEnum(value, "record", "build", "seal", "publish", "done")
}
func supportState(value string) string {
	return supportEnum(value, "queued", "running", "succeeded", "failed", "interrupted", "blocked", "deferred", "skipped", "completed")
}

// Failure text is inspected locally, never returned. These fixed categories
// deliberately trade prose for the guarantee that meeting text cannot escape.
func supportFailure(value string) string {
	v := strings.ToLower(value)
	if (strings.Contains(v, "checksum") || strings.Contains(v, "sha256")) && (strings.Contains(v, "mismatch") || strings.Contains(v, "fail") || strings.Contains(v, "invalid")) {
		return "integrity_failure"
	}
	for _, rule := range []struct{ match, code string }{
		{"sharing is only allowed with group members", "sharing_group_restriction"},
		{"only share with users in their groups", "sharing_group_restriction"},
		{"out of memory", "out_of_memory"}, {"memory unavailable", "memory_unavailable"},
		{"no space left", "disk_full"}, {"permission denied", "permission_denied"},
		{"not shared with caller", "recording_not_shared"}, {"no local starter", "missing_recording_starter"},
		{"share skipped", "recipient_share_skipped"}, {"missing after publication", "share_verification_failed"},
		{"forbidden", "forbidden"}, {"unauthorized", "unauthorized"},
		{"deadline exceeded", "timeout"}, {"timeout", "timeout"},
		{"connection refused", "connection_refused"}, {"no such host", "dns_failure"},
		{"no such file", "source_missing"},
		{"transcription_failed", "transcription_failed"}, {"llm_failed", "llm_failed"},
	} {
		if strings.Contains(v, rule.match) {
			return rule.code
		}
	}
	if value == "" {
		return "none"
	}
	return "unclassified_failure"
}

func supportError(err error) (reason string, httpStatus, ocsStatus int) {
	if err == nil {
		return "none", 0, 0
	}
	reason = supportFailure(err.Error())
	var share *ncShareError
	if errors.As(err, &share) {
		httpStatus, ocsStatus = share.HTTPStatus, share.OCSStatus
		if reason == "unclassified_failure" {
			switch share.OCSStatus {
			case 403:
				reason = "sharing_policy_refusal"
			case 404:
				reason = "share_recipient_not_found"
			}
		}
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		reason = "timeout"
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		reason = "response_too_large"
	}
	return
}

type supportDeliveryContext struct {
	job     string
	attempt int
}
type supportDeliveryKey struct{}

func (c ExAppConfig) supportShare(ctx context.Context, event, kind, recipientID string, err error, count int) {
	delivery, _ := ctx.Value(supportDeliveryKey{}).(supportDeliveryContext)
	reason, httpStatus, ocsStatus := supportError(err)
	detail := supportOriginalError(err)
	c.supportJournal.record(supportEvent{Event: event, Job: delivery.job, Attempt: delivery.attempt, Recipient: supportEnum(kind, "user", "group", "circle"), RecipientID: recipientID, Reason: reason, HTTP: httpStatus, OCS: ocsStatus, Count: count, Detail: detail})
}

// Read fixed-size snapshots. No mutex spans export/compression or a client
// download; rotation may remove an old generation, which is a partial source.
func (j *supportJournal) read(cutoff time.Time) ([]supportEvent, bool) {
	if j == nil {
		return nil, true
	}
	j.mu.Lock()
	failed := j.failed
	j.mu.Unlock()
	events, failed, _ := j.readSnapshot(cutoff, failed)
	return events, failed
}

// unreadable reports a journal file that exists but could not be opened, as
// distinct from a corrupt line inside a file that was read.
func (j *supportJournal) readSnapshot(cutoff time.Time, failed bool) (events []supportEvent, _ bool, unreadable bool) {
	root, err := os.OpenRoot(j.workRoot)
	if err != nil {
		return nil, true, true
	}
	defer root.Close()
	for _, name := range supportJournalPaths() {
		f, err := supportOpen(root, name, os.O_RDONLY)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			failed, unreadable = true, true
			continue
		}
		scanner := bufio.NewScanner(io.LimitReader(f, 2*supportJournalLimit))
		scanner.Buffer(make([]byte, 4096), 256<<10)
		for scanner.Scan() {
			var e supportEvent
			if json.Unmarshal(scanner.Bytes(), &e) != nil {
				failed = true
				continue
			}
			stamp, err := time.Parse(time.RFC3339Nano, e.At)
			if err == nil && !stamp.Before(cutoff) {
				events = append(events, e)
			}
		}
		if scanner.Err() != nil {
			failed = true
		}
		f.Close()
	}
	// Streams are stored separately; present one timeline.
	sort.SliceStable(events, func(a, b int) bool {
		ta, _ := time.Parse(time.RFC3339Nano, events[a].At)
		tb, _ := time.Parse(time.RFC3339Nano, events[b].At)
		return ta.Before(tb)
	})
	return events, failed, unreadable
}

func (j *supportJournal) expire(policy retentionPolicy, now time.Time) error {
	if j == nil || policy.Forever {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	events, failed, unreadable := j.readSnapshot(time.Time{}, false)
	if unreadable {
		// Rewriting would delete the events that could not be read.
		j.failed = true
		return errors.New("diagnostic journal unreadable; retention skipped")
	}
	if failed {
		j.failed = true
		// A crash may leave one incomplete line. Expire valid events and drop
		// corrupt lines rather than allowing one line to wedge age retention.
	}
	root, err := os.OpenRoot(j.workRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	// Delete only known files. Filtering also applies to the active generation,
	// so a busy installation cannot retain old events indefinitely.
	for _, name := range supportJournalPaths() {
		if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for _, event := range events {
		stamp, err := time.Parse(time.RFC3339Nano, event.At)
		if err == nil && !policy.due(stamp, now) {
			if err := j.append(event); err != nil {
				j.failed = true
				return err
			}
		}
	}
	if failed {
		return errors.New("incomplete diagnostic history discarded")
	}
	return nil
}

// Reapply the same projection when reading persisted bytes. Even a manually
// edited journal cannot add arbitrary strings to an export.
func safeSupportEvent(e supportEvent, jobRef func(string) string) supportEvent {
	return supportEvent{
		At: supportTime(e.At), Job: jobRef(e.Job), Attempt: e.Attempt,
		Event: supportEnum(e.Event, "operator_started", "operator_log", "operator_failure", "job_state", "share_list", "share_create", "share_verify", "share_skipped", "delivery_started", "delivery_succeeded", "delivery_failed"),
		Stage: supportStage(e.Stage), State: supportState(e.State),
		Reason: supportReason(e.Reason), HTTP: e.HTTP, OCS: e.OCS,
		Recipient: supportEnum(e.Recipient, "user", "group", "circle"), Count: e.Count,
	}
}

func supportReason(s string) string {
	return supportEnum(s, "none", "sharing_group_restriction", "out_of_memory", "memory_unavailable", "disk_full", "permission_denied", "recording_not_shared", "missing_recording_starter", "recipient_share_skipped", "share_verification_failed", "forbidden", "unauthorized", "timeout", "connection_refused", "dns_failure", "source_missing", "integrity_failure", "transcription_failed", "llm_failed", "sharing_policy_refusal", "share_recipient_not_found", "unclassified_failure", "transcription_disabled", "model_unavailable", "no_transcript", "no_llm", "disabled", "response_too_large")
}

func supportTime(value string) string {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

var supportHTTPPattern = regexp.MustCompile(`(?i)\bHTTP[ =:]+([1-5][0-9]{2})\b`)
var supportOCSPattern = regexp.MustCompile(`(?i)\bOCS[ =:]+([0-9]{3})\b`)
var supportOperatorFailurePattern = regexp.MustCompile(`\bfailed(?:[ :=]|$)|\bunavailable(?:[ :(]|$)|\berror:`)
