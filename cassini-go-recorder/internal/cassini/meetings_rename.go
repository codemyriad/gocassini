package cassini

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"gocassini/internal/inspect"
	"gocassini/internal/portable"
)

// `cassini meetings rename`: the agent's side of the meeting rename (D-862).
// A thin wrapper over the annotate route — the title is one set-title op, so
// it lands in the meeting file through the same durable path as a mark, and
// everyone with access to the meeting sees it. Access to the meeting is the
// only gate; the actor is always the authenticated account.

func runMeetingsRename(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var cfg meetingsConfig
	fs := flag.NewFlagSet("cassini meetings rename", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registerMeetingsConnectionFlags(fs, &cfg)
	clearTitle := fs.Bool("clear", false, "restore the default title (the room name, or both participants of a 1:1) instead of setting one")
	actorKind := fs.String("actor-kind", portable.AnnotationActorAgent,
		"attribute the rename as made by an agent or a person (the user id is\nalways the account you authenticate as)")
	asJSON := fs.Bool("json", false, "emit the app's answer as JSON")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini meetings rename <meeting-id> <title> [--actor-kind agent|person] [--json]
  cassini meetings rename <meeting-id> --clear

Rename one meeting. The new title shows for everyone with access to the
meeting and travels inside its portable file. --clear removes the custom
title, so the meeting falls back to its recorded default. Use
`+"`cassini meetings list`"+` to find the id.

`)
		fs.PrintDefaults()
	}
	// meetingsParseArgs for a command with up to TWO leading positionals: the
	// id and the title may come first, with flags after, the order every other
	// meetings command accepts.
	reordered := append([]string{}, args...)
	var lead []string
	for len(reordered) > 0 && !strings.HasPrefix(reordered[0], "-") && len(lead) < 2 {
		lead = append(lead, reordered[0])
		reordered = reordered[1:]
	}
	if err := fs.Parse(append(reordered, lead...)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	wantArgs := 2
	if *clearTitle {
		wantArgs = 1
	}
	if fs.NArg() != wantArgs {
		fmt.Fprintf(stderr, "rename takes a meeting id and a title (or just the id with --clear), got %d arguments: %v\n",
			fs.NArg(), redactMeetingsArgs(fs.Args()))
		if meetingsArgsLookLikeFlagsAfterPositional(fs.Args()) {
			fmt.Fprintln(stderr, "hint=flags must come before the meeting id")
		}
		fs.Usage()
		return 2
	}
	meetingID := strings.TrimSpace(fs.Arg(0))
	if meetingID == "" || meetingID == "." || meetingID == ".." {
		fmt.Fprintf(stderr, "rename configuration error: %q is not a meeting id; run `cassini meetings list` to find one\n", meetingID)
		return 2
	}
	title := ""
	if !*clearTitle {
		title = strings.TrimSpace(fs.Arg(1))
		if title == "" {
			fmt.Fprintln(stderr, "rename configuration error: the title is empty; to restore the default title, pass --clear")
			return 2
		}
		if err := portable.ValidateAnnotationTitle(title); err != nil {
			fmt.Fprintf(stderr, "rename configuration error: title: %v\n", err)
			return annotateExitInvalid
		}
	}
	kind := strings.TrimSpace(*actorKind)
	if kind != portable.AnnotationActorAgent && kind != portable.AnnotationActorPerson {
		fmt.Fprintf(stderr, "rename configuration error: --actor-kind must be %s or %s, got %q\n",
			portable.AnnotationActorAgent, portable.AnnotationActorPerson, oneLineField(kind))
		return 2
	}

	op, err := json.Marshal(map[string]string{"op": "set-title", "title": title})
	if err != nil {
		fmt.Fprintf(stderr, "rename failed: encode the request: %v\n", err)
		return 1
	}
	body, err := json.Marshal(meetingsAnnotateRequest{Ops: []json.RawMessage{op}, ActorKind: kind})
	if err != nil {
		fmt.Fprintf(stderr, "rename failed: encode the request: %v\n", err)
		return 1
	}
	if err := resolveMeetingsConfig(fs, &cfg); err != nil {
		fmt.Fprintf(stderr, "rename configuration error: %v\n", err)
		return 2
	}
	warnAboutInsecureTLS(stderr, cfg)

	answerBody, answer, err := newMeetingsClient(cfg).annotate(ctx, meetingID, body)
	if err != nil {
		return reportAnnotateError(stderr, cfg, err)
	}
	if *asJSON {
		return writeMeetingsAnswerJSON(stdout, stderr, "rename", answerBody)
	}
	id := inspect.Token(firstNonBlank(answer.MeetingID, meetingID))
	if *clearTitle {
		fmt.Fprintf(stdout, "cleared=%s revision=%d caller=%s\n", id, answer.Revision, cfg.user)
		fmt.Fprintln(stdout, "note=the meeting shows its default title again; `cassini meetings list` prints the effective one")
		return 0
	}
	// The title is free text, so it goes last.
	fmt.Fprintf(stdout, "renamed=%s revision=%d caller=%s title=%s\n", id, answer.Revision, cfg.user, title)
	return 0
}
