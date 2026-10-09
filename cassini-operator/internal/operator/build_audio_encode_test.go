package operator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRebuildAudioEncodePolicyRepeatsThePublishedMeetingsEncode(t *testing.T) {
	tests := []struct {
		name     string
		manifest string // "": meeting bundle without manifest.json
		noBundle bool
		want     string
	}{
		{name: "first build has no meeting to match", noBundle: true, want: ""},
		{name: "meeting without a manifest predates D-850", want: audioEncodeLegacy},
		{name: "manifest without an audioEncode record predates D-850", manifest: `{"kind":"cassini.meeting-artifact.v1","files":{"audio":"meeting.webm"}}`, want: audioEncodeLegacy},
		{name: "recorded policy is repeated", manifest: `{"audioEncode":{"policy":"match-source","bitrateBps":32000,"cutoffHz":8000}}`, want: "match-source"},
		{name: "recorded legacy policy is repeated", manifest: `{"audioEncode":{"policy":"fixed-64k","bitrateBps":64000}}`, want: audioEncodeLegacy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt, _ := newAdmissionTestRuntime(t, fakeModelCassini(t, readyInt8Inventory), "job-1")
			if !tt.noBundle {
				seedCanonicalMeeting(t, rt.cfg.WorkRoot, "job-1", tt.manifest)
			}
			got, err := rt.rebuildAudioEncodePolicy("job-1")
			if err != nil || got != tt.want {
				t.Fatalf("rebuildAudioEncodePolicy() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

// When the published meeting's encode cannot be read, any guess may differ
// from it and the publish would discard its marks, so the rerun stops instead.
func TestRebuildAudioEncodePolicyRefusesToGuess(t *testing.T) {
	tests := []struct {
		name string
		seed func(t *testing.T, meetingDir string)
	}{
		{name: "unreadable manifest", seed: func(t *testing.T, meetingDir string) {
			if err := os.Mkdir(filepath.Join(meetingDir, "manifest.json"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "malformed manifest", seed: func(t *testing.T, meetingDir string) {
			if err := os.WriteFile(filepath.Join(meetingDir, "manifest.json"), []byte(`{"audioEncode":`), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "audio encode record without a policy", seed: func(t *testing.T, meetingDir string) {
			if err := os.WriteFile(filepath.Join(meetingDir, "manifest.json"), []byte(`{"audioEncode":{"bitrateBps":64000}}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt, _ := newAdmissionTestRuntime(t, fakeModelCassini(t, readyInt8Inventory), "job-1")
			seedCanonicalMeeting(t, rt.cfg.WorkRoot, "job-1", "")
			tt.seed(t, canonicalMeetingPath(rt.cfg.WorkRoot, "job-1"))
			if got, err := rt.rebuildAudioEncodePolicy("job-1"); err == nil {
				t.Fatalf("rebuildAudioEncodePolicy() = %q with no error; want the rerun refused", got)
			}
		})
	}
}

// A rerun of a meeting published before D-850 must rebuild its audio with the
// encode it was published with, or the publish discards the marks people made
// on it ("different audio"). That holds for the audio-only rebuild after a
// transcription crash too; a first build takes the new default.
func TestExecuteBuildCLIRerunKeepsThePublishedAudioEncode(t *testing.T) {
	const jobID = "rerun-keeps-marks"
	argsLog := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("FAKE_ARGS_LOG", argsLog)
	bin := fakeModelCassini(t, readyInt8Inventory)
	script, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	logged := strings.Replace(string(script), "#!/bin/sh\n", "#!/bin/sh\n[ \"$1\" = build ] && printf '%s\\n' \"$*\" >> \"$FAKE_ARGS_LOG\"\n", 1)
	if err := os.WriteFile(bin, []byte(logged), 0o755); err != nil {
		t.Fatal(err)
	}
	rt, runPath := newAdmissionTestRuntime(t, bin, jobID)

	builds := func() []string {
		t.Helper()
		raw, err := os.ReadFile(argsLog)
		if err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(argsLog)
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}

	if _, err := rt.executeBuildCLI(context.Background(), buildTask{JobID: jobID, AttemptNumber: 1, ArtifactRunPath: runPath}); err != nil {
		t.Fatalf("first build: %v", err)
	}
	for _, args := range builds() {
		if strings.Contains(args, "--audio-encode") {
			t.Fatalf("a first build pinned an audio encode: %q", args)
		}
	}

	seedCanonicalMeeting(t, rt.cfg.WorkRoot, jobID, `{"kind":"cassini.meeting-artifact.v1"}`)
	if _, err := rt.executeBuildCLI(context.Background(), buildTask{JobID: jobID, AttemptNumber: 2, ArtifactRunPath: runPath}); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	got := builds()
	if len(got) != 2 {
		t.Fatalf("rerun ran %d builds, want the transcribing build and its audio-only fallback: %q", len(got), got)
	}
	for _, args := range got {
		if !strings.Contains(args, "--audio-encode "+audioEncodeLegacy) {
			t.Fatalf("rerun build %q does not keep the published %s encode", args, audioEncodeLegacy)
		}
	}
	if err := os.WriteFile(filepath.Join(canonicalMeetingPath(rt.cfg.WorkRoot, jobID), "manifest.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.executeBuildCLI(context.Background(), buildTask{JobID: jobID, AttemptNumber: 3, ArtifactRunPath: runPath}); err == nil || !strings.Contains(err.Error(), "keep its marks") {
		t.Fatalf("rerun of a meeting whose encode cannot be read: err = %v, want it refused", err)
	}
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("a build ran although the published meeting's encode could not be read (stat args log: %v)", err)
	}
}

func seedCanonicalMeeting(t *testing.T, workRoot, jobID, manifest string) {
	t.Helper()
	dir := canonicalMeetingPath(workRoot, jobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
