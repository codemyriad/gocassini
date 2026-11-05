package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Telling the person (D-885).
//
// A recording reaches its audience through Nextcloud shares, and Nextcloud
// lets an account — or an administrator, for everyone — require that shared
// files be accepted by hand before they appear. Such a recording is in nobody's
// list until the person accepts it, and nothing in Cassini said so. And where
// a sharing policy refused the person outright, the meeting was simply
// missing for them, with no way to tell that from "nothing was recorded".
//
// So /setup, which every signed-in user may read, carries the caller's own
// view: the recordings waiting for their acceptance, and how many could not
// be shared with them. The reads run AS the caller, so Nextcloud decides what
// they may know; the operator only joins its own records to it. Nothing here
// is for anyone but the caller, and nothing is accepted on their behalf
// unless they ask (POST /shares/accept).

// setupSharing is the caller's view of recording shares.
type setupSharing struct {
	// Checked is false when the reads as the caller failed or ran out of
	// time. Then Pending is empty and the counts are zero, and the app shows
	// nothing: a guess would send someone looking for a share that is not
	// there, or hide one that is.
	Checked bool `json:"checked"`
	// Pending is one entry per meeting whose shares wait for the caller,
	// newest first.
	Pending []pendingRecordingShare `json:"pending"`
	// Refused counts published meetings whose share to the caller, or to a
	// group the caller is in, Nextcloud refused, and whose notice the caller
	// has not dismissed; RefusedMeetings names them.
	Refused         int               `json:"refused"`
	RefusedMeetings []recordingNotice `json:"refused_meetings"`
	// RefusedStarted counts recordings the caller started that were not
	// published because Nextcloud refused to share them with the caller, and
	// whose notice the caller has not dismissed; RefusedStartedMeetings names
	// them.
	RefusedStarted         int               `json:"refused_started"`
	RefusedStartedMeetings []recordingNotice `json:"refused_started_meetings"`
}

// recordingNotice is one meeting Browse tells the caller about by name, one
// notice each, so they can recognise it and dismiss it. A recording that was
// never published usually has no title; the room it was recorded in and when
// are what identify it.
type recordingNotice struct {
	JobID     string `json:"job_id"`
	Title     string `json:"title"`
	RoomName  string `json:"room_name"`
	StartedAt string `json:"started_at"`
}

type pendingRecordingShare struct {
	// ShareIDs are every pending share of the same meeting for the caller: a
	// person can hold a user share and a group share of the same file.
	ShareIDs []string `json:"share_ids"`
	JobID    string   `json:"job_id"`
	// Title, StartedAt and DurationSeconds come from the operator's own
	// record of the meeting. Empty or zero when it has none: the entry still
	// appears, because the share is still waiting.
	Title           string `json:"title"`
	StartedAt       string `json:"started_at"`
	DurationSeconds int    `json:"duration_seconds"`
}

func emptySetupSharing() setupSharing {
	return setupSharing{Pending: []pendingRecordingShare{}, RefusedMeetings: []recordingNotice{}, RefusedStartedMeetings: []recordingNotice{}}
}

// sharingReadTimeout bounds both reads as the caller together. /setup is
// read when Browse opens; a slow Nextcloud must not hold the whole page.
var sharingReadTimeout = 5 * time.Second

// sharingForCaller computes the caller's view. Never for anyone else: the
// caller is the user AppAPI attributed the request to, and every read runs
// as them.
func (rt *Runtime) sharingForCaller(ctx context.Context, caller string) setupSharing {
	caller = strings.TrimSpace(caller)
	sink, ok := rt.sink().(*directSharesPublishSink)
	if !ok || caller == "" || rt.store == nil {
		return emptySetupSharing()
	}
	ctx, cancel := context.WithTimeout(ctx, sharingReadTimeout)
	defer cancel()
	var (
		pending    []ncShare
		groups     []string
		pendingErr error
		groupsErr  error
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		pending, pendingErr = sink.cfg.pendingShares(ctx, sink.client, caller)
	}()
	go func() {
		defer wg.Done()
		groups, groupsErr = sink.cfg.callerGroups(ctx, sink.client, caller)
	}()
	wg.Wait()
	if pendingErr != nil || groupsErr != nil {
		rt.logger.Printf("sharing for %s: pending shares: %v; groups: %v", caller, pendingErr, groupsErr)
		return emptySetupSharing()
	}
	retired, err := rt.store.retiredMeetingJobs(ctx)
	if err != nil {
		rt.logger.Printf("sharing for %s: retired meetings: %v", caller, err)
		return emptySetupSharing()
	}
	refused, refusedStarted, err := rt.store.refusedRecordings(ctx, caller, groups, retired)
	if err != nil {
		rt.logger.Printf("sharing for %s: refused shares: %v", caller, err)
		return emptySetupSharing()
	}
	entries := groupPendingRecordingShares(pending, retired)
	jobIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		jobIDs = append(jobIDs, entry.JobID)
	}
	allFacts := rt.meetingFactsForJobs(ctx, jobIDs)
	for i := range entries {
		facts := allFacts[entries[i].JobID]
		entries[i].Title = facts.Title
		if !facts.StartedAt.IsZero() {
			entries[i].StartedAt = facts.StartedAt.UTC().Format(time.RFC3339)
		}
		entries[i].DurationSeconds = int(facts.Duration / time.Second)
	}
	sortPendingNewestFirst(entries)
	refusedMeetings := rt.recordingNotices(ctx, refused)
	started := rt.recordingNotices(ctx, refusedStarted)
	return setupSharing{
		Checked: true, Pending: entries,
		Refused: len(refusedMeetings), RefusedMeetings: refusedMeetings,
		RefusedStarted: len(started), RefusedStartedMeetings: started,
	}
}

// recordingNotices describes meetings for the caller's notices: the title the
// metadata index has, the room's name from the job's Talk binding or the room
// names the operator keeps, and when the recording started. Newest first.
func (rt *Runtime) recordingNotices(ctx context.Context, jobIDs []string) []recordingNotice {
	out := make([]recordingNotice, 0, len(jobIDs))
	if len(jobIDs) == 0 {
		return out
	}
	facts := rt.meetingFactsForJobs(ctx, jobIDs)
	jobs := make([]Job, 0, len(jobIDs))
	for _, id := range jobIDs {
		job, err := rt.store.GetJob(ctx, id)
		if err != nil {
			job = Job{ID: id}
		}
		jobs = append(jobs, job)
	}
	rt.projectJobRoomNames(ctx, jobs)
	for _, job := range jobs {
		entry := recordingNotice{JobID: job.ID, Title: facts[job.ID].Title, RoomName: job.RoomDisplayName}
		if at := facts[job.ID].StartedAt; !at.IsZero() {
			entry.StartedAt = at.UTC().Format(time.RFC3339)
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.StartedAt != b.StartedAt {
			return a.StartedAt > b.StartedAt
		}
		return a.JobID > b.JobID
	})
	return out
}

// pendingRecordingJobID says which published meeting a pending share is for,
// or false for any other pending share — a document a colleague shared, a
// folder — which this never mentions. The file must be Cassini's: the
// recordings owner's, and named like a meeting file.
func pendingRecordingJobID(share ncShare) (string, bool) {
	owner := strings.TrimSpace(share.UIDFileOwner)
	if owner == "" {
		owner = strings.TrimSpace(share.UIDOwner)
	}
	if owner != ncRecordingsOwner || (share.ItemType != "" && share.ItemType != "file") {
		return "", false
	}
	name := path.Base(strings.TrimSpace(share.Path))
	if name == "" || name == "." || name == "/" || !isMeetingFile(name) {
		return "", false
	}
	stem := meetingStem(name)
	if stem == "" || strings.ContainsAny(stem, "/\\\x00") {
		return "", false
	}
	return stem, true
}

// groupPendingRecordingShares keeps the pending shares that are recording
// files, one entry per meeting, without the facts. Retired meetings — expired
// or deleted — are left out: there is nothing left to accept.
func groupPendingRecordingShares(shares []ncShare, retired map[string]bool) []pendingRecordingShare {
	byJob := map[string]*pendingRecordingShare{}
	order := []string{}
	for _, share := range shares {
		jobID, ok := pendingRecordingJobID(share)
		if !ok || share.ID <= 0 || retired[jobID] {
			continue
		}
		entry := byJob[jobID]
		if entry == nil {
			entry = &pendingRecordingShare{JobID: jobID, ShareIDs: []string{}}
			byJob[jobID] = entry
			order = append(order, jobID)
		}
		id := strconv.FormatInt(int64(share.ID), 10)
		duplicate := false
		for _, have := range entry.ShareIDs {
			if have == id {
				duplicate = true
				break
			}
		}
		if !duplicate {
			entry.ShareIDs = append(entry.ShareIDs, id)
		}
	}
	out := make([]pendingRecordingShare, 0, len(order))
	for _, jobID := range order {
		out = append(out, *byJob[jobID])
	}
	return out
}

// sortPendingNewestFirst orders by start, newest first; entries with no date
// go last, newest job id first (job ids are ULIDs, which sort by time).
func sortPendingNewestFirst(entries []pendingRecordingShare) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		switch {
		case a.StartedAt != "" && b.StartedAt != "" && a.StartedAt != b.StartedAt:
			return a.StartedAt > b.StartedAt
		case (a.StartedAt != "") != (b.StartedAt != ""):
			return a.StartedAt != ""
		default:
			return a.JobID > b.JobID
		}
	})
}

// callerGroups reads the caller's own groups, as the caller: /cloud/user
// answers for the session it runs in, which is exactly the scope this is for.
func (c ExAppConfig) callerGroups(ctx context.Context, client *http.Client, caller string) ([]string, error) {
	status, body, err := c.apiGetAs(ctx, client, caller, c.ocsURL("/cloud/user"))
	if err != nil {
		return nil, fmt.Errorf("read %s's groups: %w", caller, err)
	}
	if refusal := ocsRefusal(status, body); refusal != "" {
		return nil, fmt.Errorf("read %s's groups: %s", caller, refusal)
	}
	var env struct {
		OCS struct {
			Data *struct {
				Groups []string `json:"groups"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("read %s's groups: decode: %w", caller, err)
	}
	if env.OCS.Data == nil {
		return nil, fmt.Errorf("read %s's groups: no user in the response", caller)
	}
	return cleanGroupList(env.OCS.Data.Groups), nil
}
