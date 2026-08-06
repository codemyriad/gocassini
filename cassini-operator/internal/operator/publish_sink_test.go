package operator

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigDefaultsPublishSinkToUnset(t *testing.T) {
	repoRoot := makeFakeOperatorRepoRoot(t)
	t.Setenv("CASSINI_REPO_ROOT", repoRoot)

	cfg, exitCode, err := loadConfig(nil, ioDiscard{})
	if err != nil || exitCode != 0 {
		t.Fatalf("loadConfig() exitCode = %d err = %v", exitCode, err)
	}
	// Unset, not "local": the selection stays empty so a later layer can tell
	// "the deployment said nothing" from "the deployment said local".
	if cfg.PublishSink != "" {
		t.Fatalf("PublishSink = %q, want empty (unset)", cfg.PublishSink)
	}
	if got := publishSinkNameOrDefault(cfg.PublishSink); got != publishSinkLocal {
		t.Fatalf("unset resolves to %q, want %q", got, publishSinkLocal)
	}
}

func TestLoadConfigPublishSinkSelection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		env      string
		args     []string
		want     string
		wantErr  string
		wantExit int
	}{
		{name: "env local", env: publishSinkLocal, want: publishSinkLocal},
		{name: "flag local", args: []string{"--publish-sink", publishSinkLocal}, want: publishSinkLocal},
		{
			// CLI beats env, so an operator can override a baked-in image value.
			name: "flag overrides env",
			env:  "bogus",
			args: []string{"--publish-sink", publishSinkLocal},
			want: publishSinkLocal,
		},
		{name: "unknown flag", args: []string{"--publish-sink", "bogus"}, wantErr: "unknown publish sink", wantExit: 2},
		{name: "unknown env", env: "bogus", wantErr: "unknown publish sink", wantExit: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoRoot := makeFakeOperatorRepoRoot(t)
			t.Setenv("CASSINI_REPO_ROOT", repoRoot)
			if tc.env != "" {
				t.Setenv("CASSINI_PUBLISH_SINK", tc.env)
			}

			cfg, exitCode, err := loadConfig(tc.args, ioDiscard{})
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error naming an unknown sink")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
				}
				// The message must tell the operator what the valid names are.
				if !strings.Contains(err.Error(), publishSinkLocal) {
					t.Fatalf("error = %v, want it to list %q", err, publishSinkLocal)
				}
				if exitCode != tc.wantExit {
					t.Fatalf("exitCode = %d, want %d", exitCode, tc.wantExit)
				}
				return
			}
			if err != nil || exitCode != 0 {
				t.Fatalf("loadConfig() exitCode = %d err = %v", exitCode, err)
			}
			if cfg.PublishSink != tc.want {
				t.Fatalf("PublishSink = %q, want %q", cfg.PublishSink, tc.want)
			}
		})
	}
}

func TestNewPublishSinkTreatsUnsetAsDefault(t *testing.T) {
	logger := log.New(ioDiscard{}, "", 0)

	sink, err := newPublishSink("", Config{SiteRoot: t.TempDir()}, logger)
	if err != nil {
		t.Fatalf("newPublishSink(\"\") error = %v", err)
	}
	if sink.Name() != publishSinkLocal {
		t.Fatalf("unset sink = %q, want %q", sink.Name(), publishSinkLocal)
	}
	if _, err := newPublishSink("bogus", Config{}, logger); err == nil {
		t.Fatalf("expected newPublishSink to reject a non-empty unknown name")
	}
}

// errPublishSink fails every delivery, standing in for a destination that is
// unreachable.
type errPublishSink struct{ err error }

func (s errPublishSink) Name() string { return "stub" }
func (s errPublishSink) Deliver(context.Context, publishDelivery) (string, error) {
	return "", s.err
}

// okPublishSink records what it was handed and reports a destination of its own
// choosing, so the test can prove the job records the sink's location rather
// than an assumed site root.
type okPublishSink struct {
	location  string
	delivered publishDelivery
}

func (s *okPublishSink) Name() string { return "stub" }
func (s *okPublishSink) Deliver(_ context.Context, d publishDelivery) (string, error) {
	s.delivered = d
	return s.location, nil
}

// newBarePublishRuntime builds a Runtime with no workers or dispatcher, so the
// test drives runPublishJob synchronously. newTestRuntime would start the
// background publish worker, which races the direct call for the queued job and
// makes the outcome depend on who wins MarkPublishRunning. Same reason
// TestPublishTimeoutKillsHungPublish builds one by hand.
func newBarePublishRuntime(t *testing.T, logger *log.Logger) (*Runtime, func()) {
	t.Helper()
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	tmp := t.TempDir()
	store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	rt := &Runtime{
		ctx:    context.Background(),
		store:  store,
		logger: logger,
		stdout: ioDiscard{},
		stderr: ioDiscard{},
		cfg: Config{
			WorkRoot: filepath.Join(tmp, "jobs"),
			SiteRoot: filepath.Join(tmp, "site"),
		},
	}
	return rt, func() { store.Close() }
}

func TestPublishFailsWhenSinkCannotDeliver(t *testing.T) {
	rt, cleanup := newBarePublishRuntime(t, log.New(ioDiscard{}, "", 0))
	defer cleanup()

	attemptSite := filepath.Join(t.TempDir(), "attempt.site")
	rt.publishJobFn = func(context.Context, publishTask) (string, error) { return attemptSite, nil }
	rt.publishSink = errPublishSink{err: errors.New("destination unreachable")}

	insertJob(t, rt.store.db, "pub-sink-fail", "2026-06-12T10:00:00Z")
	if err := rt.store.MarkPublishQueued(context.Background(), "pub-sink-fail", "/tmp/meeting", "/tmp/meeting", nowUTCString()); err != nil {
		t.Fatalf("MarkPublishQueued() error = %v", err)
	}

	rt.runPublishJob(publishTask{JobID: "pub-sink-fail", AttemptNumber: 1})

	job := mustGetJob(t, rt.store, "pub-sink-fail")
	if job.State != "failed" {
		t.Fatalf("state = %q, want failed", job.State)
	}
	if job.Error == nil || !strings.Contains(*job.Error, "destination unreachable") {
		t.Fatalf("error = %#v, want it to carry the sink's failure", job.Error)
	}
	// A sink that could not deliver must not leave a site behind.
	if _, err := os.Stat(rt.cfg.SiteRoot); !os.IsNotExist(err) {
		t.Fatalf("SiteRoot should be untouched after a failed delivery, stat err = %v", err)
	}
}

func TestPublishRecordsTheLocationTheSinkReturns(t *testing.T) {
	var logs bytes.Buffer
	rt, cleanup := newBarePublishRuntime(t, log.New(&logs, "", 0))
	defer cleanup()

	attemptSite := filepath.Join(t.TempDir(), "attempt.site")
	rt.publishJobFn = func(context.Context, publishTask) (string, error) { return attemptSite, nil }
	sink := &okPublishSink{location: "somewhere://else"}
	rt.publishSink = sink

	insertJob(t, rt.store.db, "pub-sink-ok", "2026-06-12T10:00:00Z")
	if err := rt.store.MarkPublishQueued(context.Background(), "pub-sink-ok", "/tmp/meeting", "/tmp/meeting", nowUTCString()); err != nil {
		t.Fatalf("MarkPublishQueued() error = %v", err)
	}

	rt.runPublishJob(publishTask{JobID: "pub-sink-ok", AttemptNumber: 1})

	job := mustGetJob(t, rt.store, "pub-sink-ok")
	if job.State != "succeeded" {
		t.Fatalf("state = %q, want succeeded (error=%#v)\noperator logs:\n%s", job.State, job.Error, logs.String())
	}
	if job.ArtifactSitePath == nil || *job.ArtifactSitePath != "somewhere://else" {
		t.Fatalf("artifact_site_path = %#v, want the location the sink returned", job.ArtifactSitePath)
	}
	// The sink receives the attempt site as input, and the job identity it needs
	// to record lineage.
	if sink.delivered.AttemptSitePath != attemptSite {
		t.Fatalf("sink got AttemptSitePath = %q, want %q", sink.delivered.AttemptSitePath, attemptSite)
	}
	if sink.delivered.JobID != "pub-sink-ok" || sink.delivered.AttemptNumber != 1 {
		t.Fatalf("sink got %#v, want job pub-sink-ok attempt 1", sink.delivered)
	}
	if strings.TrimSpace(sink.delivered.PublishedAtUTC) == "" {
		t.Fatalf("sink got no publish timestamp")
	}
}

// writeAttemptSite builds the one-meeting site `cassini publish` produces: a
// catalog naming the meeting, and the .opus it points at.
func writeAttemptSite(t *testing.T, dir string, meetingIDs ...string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "meetings"), 0o755); err != nil {
		t.Fatalf("mkdir attempt site: %v", err)
	}
	entries := make([]string, 0, len(meetingIDs))
	for _, id := range meetingIDs {
		if err := os.WriteFile(filepath.Join(dir, "meetings", id+".opus"), []byte("opus-"+id), 0o644); err != nil {
			t.Fatalf("write attempt opus: %v", err)
		}
		entries = append(entries, `{"id":"`+id+`","title":"`+id+`","audioPath":"./meetings/`+id+`.opus"}`)
	}
	body := `{"version":"cassini.viewer.catalog.v1","meetings":[` + strings.Join(entries, ",") + `]}`
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write attempt catalog: %v", err)
	}
	return dir
}

func deliverToLocalSink(t *testing.T, siteRoot, attemptSite, jobID string) (string, error) {
	t.Helper()
	sink := &localPublishSink{siteRoot: siteRoot, logger: log.New(ioDiscard{}, "", 0)}
	return sink.Deliver(context.Background(), publishDelivery{
		AttemptSitePath: attemptSite,
		JobID:           jobID,
		AttemptNumber:   1,
		PublishedAtUTC:  "2026-06-12T00:00:00Z",
	})
}

func readCatalogIDs(t *testing.T, siteRoot string) []string {
	t.Helper()
	catalog, ok, err := loadSiteCatalog(siteRoot)
	if err != nil || !ok {
		t.Fatalf("loadSiteCatalog(%s) ok = %v err = %v", siteRoot, ok, err)
	}
	ids := make([]string, 0, len(catalog.Meetings))
	for _, entry := range catalog.Meetings {
		id, err := catalogEntryID(entry)
		if err != nil {
			t.Fatalf("catalogEntryID() error = %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestLocalSinkPublishesIntoAnEmptySite(t *testing.T) {
	siteRoot := filepath.Join(t.TempDir(), "site")
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")

	location, err := deliverToLocalSink(t, siteRoot, attempt, "meeting-a")
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if location != siteRoot {
		t.Fatalf("location = %q, want %q", location, siteRoot)
	}
	if got := readCatalogIDs(t, siteRoot); len(got) != 1 || got[0] != "meeting-a" {
		t.Fatalf("catalog ids = %v, want [meeting-a]", got)
	}
	if _, err := os.Stat(filepath.Join(siteRoot, "meetings", "meeting-a.opus")); err != nil {
		t.Fatalf("published opus missing: %v", err)
	}
}

func TestLocalSinkKeepsMeetingsItDidNotPublish(t *testing.T) {
	// The whole point of the upsert: publishing B must not disturb A. Under the
	// old wholesale replace, A survived only because the exporter re-exported
	// it every time — which is what made publishing cost O(archive).
	siteRoot := filepath.Join(t.TempDir(), "site")
	if _, err := deliverToLocalSink(t, siteRoot, writeAttemptSite(t, filepath.Join(t.TempDir(), "a"), "meeting-a"), "meeting-a"); err != nil {
		t.Fatalf("first Deliver() error = %v", err)
	}

	publishedA := filepath.Join(siteRoot, "meetings", "meeting-a.opus")
	before, err := os.Stat(publishedA)
	if err != nil {
		t.Fatalf("stat first opus: %v", err)
	}

	if _, err := deliverToLocalSink(t, siteRoot, writeAttemptSite(t, filepath.Join(t.TempDir(), "b"), "meeting-b"), "meeting-b"); err != nil {
		t.Fatalf("second Deliver() error = %v", err)
	}

	got := readCatalogIDs(t, siteRoot)
	if len(got) != 2 || got[0] != "meeting-a" || got[1] != "meeting-b" {
		t.Fatalf("catalog ids = %v, want [meeting-a meeting-b] in that order", got)
	}
	after, err := os.Stat(publishedA)
	if err != nil {
		t.Fatalf("stat first opus after second publish: %v", err)
	}
	// Untouched, not merely present — this is the O(1) claim.
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("publishing meeting-b rewrote meeting-a.opus (mtime %s -> %s)", before.ModTime(), after.ModTime())
	}
}

func TestLocalSinkUpdatesARepublishedMeetingInPlace(t *testing.T) {
	siteRoot := filepath.Join(t.TempDir(), "site")
	for _, id := range []string{"meeting-a", "meeting-b"} {
		if _, err := deliverToLocalSink(t, siteRoot, writeAttemptSite(t, filepath.Join(t.TempDir(), id), id), id); err != nil {
			t.Fatalf("Deliver(%s) error = %v", id, err)
		}
	}

	// Re-publish A with new audio, as a rerun would.
	rerun := filepath.Join(t.TempDir(), "rerun")
	writeAttemptSite(t, rerun, "meeting-a")
	if err := os.WriteFile(filepath.Join(rerun, "meetings", "meeting-a.opus"), []byte("opus-rerun"), 0o644); err != nil {
		t.Fatalf("write rerun opus: %v", err)
	}
	if _, err := deliverToLocalSink(t, siteRoot, rerun, "meeting-a"); err != nil {
		t.Fatalf("rerun Deliver() error = %v", err)
	}

	got := readCatalogIDs(t, siteRoot)
	if len(got) != 2 || got[0] != "meeting-a" || got[1] != "meeting-b" {
		t.Fatalf("catalog ids = %v, want [meeting-a meeting-b] — updated in place, not duplicated", got)
	}
	body, err := os.ReadFile(filepath.Join(siteRoot, "meetings", "meeting-a.opus"))
	if err != nil {
		t.Fatalf("read republished opus: %v", err)
	}
	if string(body) != "opus-rerun" {
		t.Fatalf("republished opus = %q, want the rerun's audio", body)
	}
}

func TestLocalSinkRefreshesTheSiteShell(t *testing.T) {
	// The standalone image publishes with --rebuild-viewer, so its shell rides
	// along with each publish; the wholesale promote used to refresh it.
	siteRoot := filepath.Join(t.TempDir(), "site")
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")
	if err := os.WriteFile(filepath.Join(attempt, "index.html"), []byte("<html>new</html>"), 0o644); err != nil {
		t.Fatalf("write attempt shell: %v", err)
	}

	if _, err := deliverToLocalSink(t, siteRoot, attempt, "meeting-a"); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	body, err := os.ReadFile(filepath.Join(siteRoot, "index.html"))
	if err != nil {
		t.Fatalf("read published shell: %v", err)
	}
	if string(body) != "<html>new</html>" {
		t.Fatalf("shell = %q, want the attempt site's", body)
	}
}

func TestLocalSinkRefusesAMalformedLiveCatalog(t *testing.T) {
	// Overwriting an unreadable catalog would silently drop the archive it
	// indexes, so refuse and leave it for a human.
	siteRoot := filepath.Join(t.TempDir(), "site")
	if err := os.MkdirAll(siteRoot, 0o755); err != nil {
		t.Fatalf("mkdir site: %v", err)
	}
	if err := os.WriteFile(filepath.Join(siteRoot, "catalog.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write malformed catalog: %v", err)
	}
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")

	if _, err := deliverToLocalSink(t, siteRoot, attempt, "meeting-a"); err == nil {
		t.Fatalf("expected Deliver to refuse a malformed live catalog")
	}
	body, err := os.ReadFile(filepath.Join(siteRoot, "catalog.json"))
	if err != nil || string(body) != "{not json" {
		t.Fatalf("malformed catalog was modified: %q err = %v", body, err)
	}
}

func TestLocalSinkFailsBeforeWritingTheCatalogWhenAnAssetIsMissing(t *testing.T) {
	// The invariant the ordering exists to protect: never publish a catalog
	// naming audio that is not on disk.
	siteRoot := filepath.Join(t.TempDir(), "site")
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")
	if err := os.Remove(filepath.Join(attempt, "meetings", "meeting-a.opus")); err != nil {
		t.Fatalf("remove attempt opus: %v", err)
	}

	if _, err := deliverToLocalSink(t, siteRoot, attempt, "meeting-a"); err == nil {
		t.Fatalf("expected Deliver to fail when the attempt site lacks the asset")
	}
	if _, err := os.Stat(filepath.Join(siteRoot, "catalog.json")); !os.IsNotExist(err) {
		t.Fatalf("catalog must not exist after a failed delivery, stat err = %v", err)
	}
}

func TestLocalSinkLeavesNoStagedAssetsBehind(t *testing.T) {
	siteRoot := filepath.Join(t.TempDir(), "site")

	// Success path.
	if _, err := deliverToLocalSink(t, siteRoot, writeAttemptSite(t, filepath.Join(t.TempDir(), "ok"), "meeting-a"), "meeting-a"); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	assertNoStagedAssets(t, siteRoot)

	// Failure path: two meetings where the second asset is missing, so the
	// first has already been staged when the delivery gives up. A stranded temp
	// in the live site would be served by the file server and swept by nothing.
	bad := writeAttemptSite(t, filepath.Join(t.TempDir(), "bad"), "meeting-b", "meeting-c")
	if err := os.Remove(filepath.Join(bad, "meetings", "meeting-c.opus")); err != nil {
		t.Fatalf("remove attempt opus: %v", err)
	}
	if _, err := deliverToLocalSink(t, siteRoot, bad, "meeting-b"); err == nil {
		t.Fatalf("expected Deliver to fail")
	}
	assertNoStagedAssets(t, siteRoot)
}

func assertNoStagedAssets(t *testing.T, siteRoot string) {
	t.Helper()
	_ = filepath.WalkDir(siteRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasSuffix(entry.Name(), stagedAssetSuffix) || strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("staged leftover in the live site: %s", path)
		}
		return nil
	})
}

func TestResolvePublishInputPathPrefersTheMeetingBundle(t *testing.T) {
	workRoot := t.TempDir()
	if err := os.MkdirAll(currentRoot(workRoot), 0o755); err != nil {
		t.Fatalf("mkdir current: %v", err)
	}
	meeting := canonicalMeetingPath(workRoot, "job1")
	opus := canonicalOpusPath(workRoot, "job1")

	// Neither artefact: a job with nothing to publish must say so rather than
	// export an empty site.
	if _, err := resolvePublishInputPath(workRoot, "job1"); err == nil {
		t.Fatalf("expected an error when neither artefact exists")
	} else if !strings.Contains(err.Error(), "job1") {
		t.Fatalf("error = %v, want it to name the job", err)
	}

	// Only the .opus (its .meeting was pruned): fall back to it.
	if err := os.WriteFile(opus, []byte("opus"), 0o644); err != nil {
		t.Fatalf("write opus: %v", err)
	}
	got, err := resolvePublishInputPath(workRoot, "job1")
	if err != nil || got != opus {
		t.Fatalf("resolvePublishInputPath() = %q err = %v, want %q", got, err, opus)
	}

	// Both present: prefer the .meeting. On a rerun the .opus still holds the
	// previous attempt's audio, because it is packed asynchronously after the
	// publish is enqueued.
	if err := os.MkdirAll(meeting, 0o755); err != nil {
		t.Fatalf("mkdir meeting: %v", err)
	}
	got, err = resolvePublishInputPath(workRoot, "job1")
	if err != nil || got != meeting {
		t.Fatalf("resolvePublishInputPath() = %q err = %v, want %q", got, err, meeting)
	}
}
