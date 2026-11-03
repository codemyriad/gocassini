package operator

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// `cassini-operator topics-preview on|off|status`: unlock or lock the
// unannounced topic suggestions preview on this install (topics_preview.go).
// It writes one row of the annotations database, so it is safe to run while
// the operator serves, which notices at its next request.
const topicsPreviewCommand = "topics-preview"

func runTopicsPreview(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini-operator "+topicsPreviewCommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Unlock or lock the topic suggestions preview on this server.

Topic suggestions are locked until this command unlocks them: Settings does
not show them and nothing runs. Once unlocked, an administrator downloads the
model and switches them on in Settings. Locking again keeps everything they
stored and the Settings switch; unlocking brings them back as they were.

Usage:
  cassini-operator `+topicsPreviewCommand+` on|off|status
`)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	action := fs.Arg(0)
	if fs.NArg() != 1 || action != "on" && action != "off" && action != "status" {
		fmt.Fprintf(stderr, "%s: say on, off or status\n", topicsPreviewCommand)
		return 2
	}
	cfg, code, err := loadConfig(nil, stderr)
	if err != nil || code != 0 {
		if err != nil {
			fmt.Fprintf(stderr, "operator config: %v\n", err)
		}
		return 3
	}
	annotations, err := openAnnotationStore(sidecarPath(cfg.DBPath, annotationsStoreFilename), nil)
	if err != nil {
		fmt.Fprintf(stderr, "open annotations database: %v\n", err)
		return 3
	}
	defer annotations.Close()
	if action != "status" {
		if err := newTopicsStore(annotations).setTopicsPreviewUnlocked(ctx, action == "on"); err != nil {
			fmt.Fprintf(stderr, "%s %s: %v\n", topicsPreviewCommand, action, err)
			return 1
		}
	}
	settings, err := savedTopicSettings(settingsPath(cfg))
	if err != nil {
		fmt.Fprintf(stderr, "read settings: %v\n", err)
		return 1
	}
	switch action {
	case "status":
		state, onOff := "locked", "off"
		if annotations.topicsPreviewUnlocked(ctx) {
			state = "unlocked"
		}
		if settings.Enabled {
			onOff = "on"
		}
		fmt.Fprintf(stdout, "preview: %s\nswitch in Settings: %s\n", state, onOff)
	case "on":
		if settings.Enabled {
			fmt.Fprintln(stdout, "Unlocked. Topic suggestions were switched on in Settings before, so they run again now.")
		} else {
			fmt.Fprintln(stdout, "Unlocked. In Settings, download the topic suggestions model, then switch suggestions on.")
		}
	case "off":
		fmt.Fprintln(stdout, "Locked. Nothing runs and Settings no longer shows topic suggestions; what they stored is kept.")
	}
	return 0
}

// savedTopicSettings reads the topic_suggestions block of settings.json
// without creating or rewriting the file, which the serving operator owns.
func savedTopicSettings(path string) (topicSettings, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return topicSettings{}, nil
	}
	if err != nil {
		return topicSettings{}, err
	}
	var saved struct {
		TopicSuggestions topicSettings `json:"topic_suggestions"`
	}
	err = json.Unmarshal(raw, &saved)
	return saved.TopicSuggestions, err
}
