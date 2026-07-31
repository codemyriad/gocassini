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
		{name: "flag local", args: []string{"--sink", publishSinkLocal}, want: publishSinkLocal},
		{
			// CLI beats env, so an operator can override a baked-in image value.
			name: "flag overrides env",
			env:  "bogus",
			args: []string{"--sink", publishSinkLocal},
			want: publishSinkLocal,
		},
		{name: "unknown flag", args: []string{"--sink", "bogus"}, wantErr: "unknown publish sink", wantExit: 2},
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
