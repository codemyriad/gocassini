package operator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startupTestEnv is the environment of a standalone ExApp boot with no Talk
// secret configured, so Run generates and persists one.
func startupTestEnv(t *testing.T) (repoRoot, dataRoot string) {
	t.Helper()
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	repoRoot = makeFakeOperatorRepoRoot(t)
	dataRoot = t.TempDir()
	t.Setenv("CASSINI_REPO_ROOT", repoRoot)
	t.Setenv(envAppID, "gocassini")
	t.Setenv(envAppVersion, "test")
	t.Setenv(envAppSecret, "test-app-secret")
	t.Setenv(envNextcloudURL, "https://cloud.example.test")
	t.Setenv(envAppHost, "")
	t.Setenv(envAppPort, "")
	t.Setenv(envAppPersistentStorage, "")
	t.Setenv(envAppAPIRequired, "false")
	t.Setenv(envViewerDist, "")
	t.Setenv(envPublishSinkName, "")
	t.Setenv(envSTTCUDACapable, "0")
	t.Setenv("CASSINI_TALK_RECORDING_SECRET", "")
	t.Setenv(envTalkSignalingInternalSecret, "test-signaling-secret")
	return repoRoot, dataRoot
}

func startupTestBoot(t *testing.T, repoRoot, dataRoot string) (int, string) {
	t.Helper()
	args := []string{
		"--bind", "127.0.0.1:0",
		"--db", filepath.Join(dataRoot, "jobs.sqlite3"),
		"--work-root", filepath.Join(dataRoot, "jobs"),
		"--site-root", filepath.Join(dataRoot, "site"),
		"--cassini-bin", filepath.Join(repoRoot, "bin", "cassini"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	code := Run(ctx, args, &stdout, &stderr)
	return code, stderr.String()
}

// The journal starts right after the work-root lock, so the lines that explain
// a "reset at reboot" (a new Talk secret, a missing or unreadable settings
// file) reach support bundles, each after its own boot's operator_started.
func TestStartupLinesReachTheSupportJournal(t *testing.T) {
	repoRoot, dataRoot := startupTestEnv(t)
	boot := func() string {
		t.Helper()
		code, logs := startupTestBoot(t, repoRoot, dataRoot)
		if code != 0 {
			t.Fatalf("Run() = %d\n%s", code, logs)
		}
		return logs
	}
	const (
		secretLine   = "talk provisioning: generated a Talk recording secret"
		settingsLine = "settings.json was not found; using detected defaults (transcription off)"
		llmLine      = "llm_settings created from the deploy environment (llm-settings.json was not found)"
		failedLine   = "stt_settings could not be loaded"
	)

	first := boot() // fresh install
	for _, want := range []string{secretLine, "press Connect Talk again", settingsLine, llmLine} {
		if !strings.Contains(first, want) {
			t.Fatalf("first boot does not log %q:\n%s", want, first)
		}
	}
	if strings.Contains(first, "wire-cassini.sh") {
		t.Fatalf("the secret line still names a sandbox script:\n%s", first)
	}
	second := boot() // healthy restart: llm-settings.json and the secret exist
	for _, unwanted := range []string{secretLine, llmLine} {
		if strings.Contains(second, unwanted) {
			t.Fatalf("healthy restart logs %q:\n%s", unwanted, second)
		}
	}
	// settings.json is written only by a save, so its absence is logged again.
	if !strings.Contains(second, settingsLine) {
		t.Fatalf("restart with no saved settings does not say so:\n%s", second)
	}
	for _, name := range []string{"llm-settings.json", talkProvisioningFile} {
		if err := os.Remove(filepath.Join(dataRoot, name)); err != nil {
			t.Fatal(err)
		}
	}
	boot() // files lost beside a surviving journal
	if err := os.WriteFile(filepath.Join(dataRoot, "settings.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	fourth := boot() // unreadable settings
	if !strings.Contains(fourth, failedLine) || strings.Contains(fourth, settingsLine) {
		t.Fatalf("an unreadable settings file must be reported as such, not as absent:\n%s", fourth)
	}

	j := &supportJournal{workRoot: filepath.Join(dataRoot, "jobs"), limit: supportJournalLimit}
	events, failed := j.read(time.Time{})
	if failed {
		t.Fatal("journal read failed")
	}
	started := 0
	var secretBoots, settingsBoots, llmBoots, failedBoots []int
	for _, e := range events {
		switch {
		case e.Event == "operator_started":
			started++
		case strings.Contains(e.Detail, secretLine):
			secretBoots = append(secretBoots, started)
		case strings.Contains(e.Detail, settingsLine):
			settingsBoots = append(settingsBoots, started)
		case strings.Contains(e.Detail, llmLine):
			llmBoots = append(llmBoots, started)
		case strings.Contains(e.Detail, failedLine):
			failedBoots = append(failedBoots, started)
		}
	}
	if started != 4 {
		t.Fatalf("operator_started = %d, want 4", started)
	}
	for _, c := range []struct {
		name      string
		got, want []int
	}{
		{"secret", secretBoots, []int{1, 3}},
		{"settings absent", settingsBoots, []int{1, 2, 3}},
		{"llm settings created", llmBoots, []int{1, 3}},
		{"settings unreadable", failedBoots, []int{4}},
	} {
		if len(c.got) != len(c.want) {
			t.Fatalf("%s journaled in boots %v, want %v", c.name, c.got, c.want)
		}
		for i := range c.got {
			if c.got[i] != c.want[i] {
				t.Fatalf("%s journaled in boots %v, want %v", c.name, c.got, c.want)
			}
		}
	}
}

// A second operator on a locked work root exits before it writes anything to
// the volume, so it cannot generate a Talk secret the running one never uses.
func TestASecondOperatorExitsBeforeItWritesATalkSecret(t *testing.T) {
	repoRoot, dataRoot := startupTestEnv(t)
	unlock, err := lockWorkRoot(filepath.Join(dataRoot, "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	code, logs := startupTestBoot(t, repoRoot, dataRoot)
	if code != 1 || !strings.Contains(logs, "work root:") {
		t.Fatalf("Run() = %d, want 1 with a work root error\n%s", code, logs)
	}
	for _, name := range []string{talkProvisioningFile, "jobs.sqlite3", "llm-settings.json"} {
		if _, err := os.Stat(filepath.Join(dataRoot, name)); !os.IsNotExist(err) {
			t.Fatalf("%s exists after a refused start (err=%v)", name, err)
		}
	}
}
