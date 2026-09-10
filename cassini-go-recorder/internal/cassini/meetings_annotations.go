package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gocassini/internal/inspect"
	"gocassini/internal/portable"
)

// `cassini meetings tags | annotations | annotate`: the agent's side of tags
// and marks. This CLI never touches a file. The app reads a meeting's marks as
// the caller, and writes a batch after the same visibility check, stamping each
// new mark with the caller's authenticated user id — never a value sent from
// here. --actor-kind is self-declared attribution, not an access control.

const (
	meetingsAnnotationsMeetingPath = "annotations/meetings/"
	meetingsAnnotationsTagsPath    = "annotations/tags"

	// maxAnnotateBodyBytes is the app's cap on one annotate request.
	maxAnnotateBodyBytes = 64 << 10

	// meetingsWriteTimeout is far longer than the reads' on purpose: one commit
	// re-uploads the whole recording and the app retries a collision before it
	// answers. Cutting that short turns a write that probably landed into "maybe".
	meetingsWriteTimeout = 3 * time.Minute
)

// meetingsStdin is where `--ops -` reads from; tests substitute it.
var meetingsStdin io.Reader = os.Stdin

// errMeetingsTagsUnavailable is an app whose tags route answers 404: one that
// serves no tags, so a --tag can only be refused, never passed on and ignored.
var errMeetingsTagsUnavailable = errors.New("this Cassini app does not offer tags and marks; ask an administrator to update it")

type meetingsAnnotationsAnswer struct {
	MeetingID   string          `json:"meetingId"`
	Revision    int             `json:"revision"`
	Annotations json.RawMessage `json:"annotations"`
	Resolved    *bool           `json:"resolved"`
}

// meetingsAnnotateRequest has no actor id: the app takes that from the
// authenticated caller and nowhere else.
type meetingsAnnotateRequest struct {
	Ops            []json.RawMessage `json:"ops"`
	ExpectRevision *int              `json:"expectRevision,omitempty"`
	ActorKind      string            `json:"actorKind"`
	OperationID    string            `json:"operationId,omitempty"`
}

type meetingsAnnotateAnswer struct {
	MeetingID   string   `json:"meetingId"`
	Revision    int      `json:"revision"`
	OperationID string   `json:"operationId"`
	Added       []string `json:"added"`
	Removed     []string `json:"removed"`
	NotFound    []string `json:"notFound"`
	Resolved    *bool    `json:"resolved"`
}

type meetingsTagsAnswer struct {
	Tags []struct {
		TagID    string `json:"tagId"`
		Label    string `json:"label"`
		Meetings int    `json:"meetings"`
		Marks    int    `json:"marks"`
	} `json:"tags"`
	Coverage struct {
		Visible int `json:"visible"`
		Indexed int `json:"indexed"`
	} `json:"coverage"`
}

func runMeetingsTags(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var cfg meetingsConfig
	fs := flag.NewFlagSet("cassini meetings tags", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registerMeetingsConnectionFlags(fs, &cfg)
	asJSON := fs.Bool("json", false, "emit the app's answer as JSON")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini meetings tags [--json]

List the tags on the meetings you may read: each tag's id and label, and how
many of your meetings and marks carry it. The tag= value, or the label, is what
--tag on `+"`meetings search`"+` and `+"`meetings list`"+` accepts.

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "tags does not accept positional arguments: %v\n", redactMeetingsArgs(fs.Args()))
		fs.Usage()
		return 2
	}
	if err := resolveMeetingsConfig(fs, &cfg); err != nil {
		fmt.Fprintf(stderr, "tags configuration error: %v\n", err)
		return 2
	}
	warnAboutInsecureTLS(stderr, cfg)

	body, vocabulary, err := newMeetingsClient(cfg).tags(ctx)
	if err != nil {
		return reportMeetingsError(stderr, "tags", cfg, err)
	}
	if *asJSON {
		return writeMeetingsAnswerJSON(stdout, stderr, "tags", body)
	}

	fmt.Fprintf(stdout, "tags=%d caller=%s indexed=%d of %d meeting(s) you can read\n",
		len(vocabulary.Tags), cfg.user, vocabulary.Coverage.Indexed, vocabulary.Coverage.Visible)
	if unindexed := vocabulary.Coverage.Visible - vocabulary.Coverage.Indexed; unindexed > 0 {
		// Without it a short vocabulary reads as "nobody tagged that".
		fmt.Fprintf(stdout, "note=%d meeting(s) you can read are not in the tag index yet, so these counts do not cover them\n", unindexed)
	}
	if len(vocabulary.Tags) == 0 {
		if vocabulary.Coverage.Visible == 0 {
			fmt.Fprintln(stdout, "note=no recordings are visible to this account; this is also what a mis-provisioned recordings folder looks like")
		} else {
			fmt.Fprintln(stdout, "note=no meeting you can read carries a tag yet")
		}
		return 0
	}
	for _, tag := range vocabulary.Tags {
		fmt.Fprintf(stdout, "tag=%s meetings=%d marks=%d label=%s\n",
			inspect.Token(tag.TagID), tag.Meetings, tag.Marks, blankMeetingsDash(tag.Label))
	}
	fmt.Fprintln(stdout, "hint=narrow a search or a list with --tag <label or tag= value>; read one meeting's marks with `cassini meetings annotations <meeting-id>`")
	return 0
}

func runMeetingsAnnotations(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var cfg meetingsConfig
	fs := flag.NewFlagSet("cassini meetings annotations", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registerMeetingsConnectionFlags(fs, &cfg)
	asJSON := fs.Bool("json", false, "emit the app's answer as JSON")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini meetings annotations <meeting-id> [--json]

Print one meeting's tags and marks: for each mark, its tag, what it covers (the
whole meeting, or a stretch of it), who made it, and the batch it came in. Use
`+"`cassini meetings list`"+` to find the id.

`)
		fs.PrintDefaults()
	}
	meetingID, code, ok := parseMeetingsOneID(fs, args, "annotations", stderr)
	if !ok {
		return code
	}
	if err := resolveMeetingsConfig(fs, &cfg); err != nil {
		fmt.Fprintf(stderr, "annotations configuration error: %v\n", err)
		return 2
	}
	warnAboutInsecureTLS(stderr, cfg)

	var answer meetingsAnnotationsAnswer
	body, err := newMeetingsClient(cfg).getAnnotations(ctx, meetingsAnnotationsMeetingPath+url.PathEscape(meetingID), &answer)
	if err != nil {
		return reportMeetingsError(stderr, "annotations", cfg, err)
	}
	if *asJSON {
		return writeMeetingsAnswerJSON(stdout, stderr, "annotations", body)
	}
	printMeetingsAnnotations(stdout, stderr, cfg, firstNonBlank(answer.MeetingID, meetingID), answer)
	return 0
}

// printMeetingsAnnotations renders one meeting's document, one line per mark,
// through the format's tolerant reader: a format it does not know is named and
// skipped, never guessed at.
func printMeetingsAnnotations(stdout, stderr io.Writer, cfg meetingsConfig, meetingID string, answer meetingsAnnotationsAnswer) {
	doc, err := portable.ParseAnnotations(answer.Annotations)
	if err != nil {
		fmt.Fprintf(stdout, "meeting=%s revision=%d caller=%s\n", inspect.Token(meetingID), answer.Revision, cfg.user)
		if errors.Is(err, portable.ErrAnnotationsFormatUnsupported) {
			fmt.Fprintln(stdout, "note=this meeting's marks are in a format this cassini build does not read; update the CLI, or read the raw document with --json")
		} else {
			fmt.Fprintf(stderr, "warning=this meeting's marks could not be read (%s); --json shows the raw document\n", oneLineField(err.Error()))
		}
		return
	}
	var tags, marks int
	if doc != nil {
		tags, marks = len(doc.Tags), len(doc.Items)
	}
	fmt.Fprintf(stdout, "meeting=%s revision=%d tags=%d marks=%d resolved=%s caller=%s\n",
		inspect.Token(meetingID), answer.Revision, tags, marks, meetingsResolved(answer.Resolved), cfg.user)
	if marks == 0 {
		fmt.Fprintln(stdout, "note=this meeting carries no marks")
		return
	}
	if answer.Resolved != nil && !*answer.Resolved {
		fmt.Fprintln(stdout, "note=these marks were made against different audio, so their times do not point into this recording, and no new mark can be added until they are migrated")
	}
	labels := make(map[string]string, len(doc.Tags))
	for _, tag := range doc.Tags {
		labels[tag.ID] = tag.Label
	}
	for _, item := range doc.Items {
		// The label is free text, so it goes last.
		fmt.Fprintf(stdout, "mark=%s target=%s actor=%s:%s created=%s operation=%s tag_id=%s tag=%s\n",
			inspect.Token(item.ID),
			describeMeetingsTarget(item.Target),
			inspect.Token(item.Actor.Kind), inspect.Token(item.Actor.ID),
			inspect.Token(item.CreatedAtUTC),
			inspect.Token(item.OperationID),
			inspect.Token(item.TagID),
			blankMeetingsDash(labels[item.TagID]))
	}
	fmt.Fprintf(stdout, "hint=add or remove marks with `cassini meetings annotate %s --ops <file>`; --json carries each range's exact milliseconds\n", inspect.Token(meetingID))
}

func runMeetingsAnnotate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var cfg meetingsConfig
	fs := flag.NewFlagSet("cassini meetings annotate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registerMeetingsConnectionFlags(fs, &cfg)
	opsPath := fs.String("ops", "", `required: the {"ops": [...]} file to apply, or - to read it from stdin`)
	actorKind := fs.String("actor-kind", portable.AnnotationActorAgent,
		"attribute these marks as made by an agent or a person (the user id is\nalways the account you authenticate as)")
	expectRevision := fs.Int("expect-revision", 0,
		"refuse the batch unless the meeting's marks are at this revision\n(0 means it carries none yet); off unless passed")
	operationID := fs.String("operation-id", "", "stamp the batch with this operation id instead of a minted one")
	asJSON := fs.Bool("json", false, "emit the app's answer as JSON")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini meetings annotate <meeting-id> --ops ./ops.json [--actor-kind agent|person]
                            [--expect-revision N] [--operation-id ID] [--json]

Apply a batch of ops to one meeting's marks, as one commit. The ops file is the
{"ops": [...]} document `+"`cassini annotate apply`"+` reads, for example:

  {"ops": [
    {"op": "mark", "tag": {"label": "hiring"}, "target": {"kind": "meeting"}},
    {"op": "mark", "tag": {"label": "hiring"},
     "target": {"kind": "time-range", "startMs": 869000, "endMs": 884000}},
    {"op": "unmark", "itemId": "mk_..."},
    {"op": "unmark-tag", "tagId": "tag_..."},
    {"op": "undo-operation", "operationId": "op_..."},
    {"op": "relabel", "tagId": "tag_...", "label": "recruiting"}
  ]}

Every new mark is attributed to the account you authenticate as. A mark that
already exists is not added twice, so re-running a batch is safe.

`)
		fs.PrintDefaults()
	}
	meetingID, code, ok := parseMeetingsOneID(fs, args, "annotate", stderr)
	if !ok {
		return code
	}
	passed := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { passed[f.Name] = true })

	if strings.TrimSpace(*opsPath) == "" {
		fmt.Fprintln(stderr, "annotate configuration error: --ops is required (a file, or - for stdin)")
		return 2
	}
	kind := strings.TrimSpace(*actorKind)
	if kind != portable.AnnotationActorAgent && kind != portable.AnnotationActorPerson {
		fmt.Fprintf(stderr, "annotate configuration error: --actor-kind must be %s or %s, got %q\n",
			portable.AnnotationActorAgent, portable.AnnotationActorPerson, oneLineField(kind))
		return 2
	}
	var expect *int
	if passed["expect-revision"] {
		if *expectRevision < 0 {
			fmt.Fprintf(stderr, "annotate configuration error: --expect-revision must be 0 or more, got %d\n", *expectRevision)
			return 2
		}
		expect = expectRevision
	}
	source := meetingsStdin
	if *opsPath != "-" {
		file, err := os.Open(*opsPath)
		if err != nil {
			fmt.Fprintf(stderr, "annotate configuration error: read --ops: %v\n", err)
			return 2
		}
		defer file.Close()
		source = file
	}
	raw, err := io.ReadAll(io.LimitReader(source, maxAnnotateBodyBytes+1))
	if err != nil {
		fmt.Fprintf(stderr, "annotate configuration error: read --ops: %v\n", err)
		return 2
	}
	if len(raw) > maxAnnotateBodyBytes {
		fmt.Fprintf(stderr, "annotate configuration error: --ops is larger than %d KiB, the most the app accepts in one request; split it into several\n", maxAnnotateBodyBytes>>10)
		return annotateExitInvalid
	}
	ops, err := parseAnnotateOps(raw)
	if err == nil && len(ops) == 0 {
		err = errors.New("--ops holds no ops, so there is nothing to apply")
	}
	if err != nil {
		fmt.Fprintf(stderr, "annotate configuration error: %v\n", err)
		return annotateExitInvalid
	}
	request := meetingsAnnotateRequest{ExpectRevision: expect, ActorKind: kind, OperationID: strings.TrimSpace(*operationID)}
	for _, op := range ops {
		request.Ops = append(request.Ops, op.raw)
	}
	body, err := json.Marshal(request)
	if err != nil {
		fmt.Fprintf(stderr, "annotate failed: encode the request: %v\n", err)
		return 1
	}
	if err := resolveMeetingsConfig(fs, &cfg); err != nil {
		fmt.Fprintf(stderr, "annotate configuration error: %v\n", err)
		return 2
	}
	warnAboutInsecureTLS(stderr, cfg)

	answerBody, answer, err := newMeetingsClient(cfg).annotate(ctx, meetingID, body)
	if err != nil {
		return reportAnnotateError(stderr, cfg, err)
	}
	if *asJSON {
		return writeMeetingsAnswerJSON(stdout, stderr, "annotate", answerBody)
	}

	fmt.Fprintf(stdout, "annotated=%s revision=%d operation=%s added=%d removed=%d not_found=%d resolved=%s caller=%s\n",
		inspect.Token(firstNonBlank(answer.MeetingID, meetingID)), answer.Revision, inspect.Token(answer.OperationID),
		len(answer.Added), len(answer.Removed), len(answer.NotFound), meetingsResolved(answer.Resolved), cfg.user)
	for _, id := range answer.Added {
		fmt.Fprintf(stdout, "change=added mark=%s\n", inspect.Token(id))
	}
	for _, id := range answer.Removed {
		fmt.Fprintf(stdout, "change=removed mark=%s\n", inspect.Token(id))
	}
	for _, id := range answer.NotFound {
		fmt.Fprintf(stdout, "change=not-found id=%s\n", inspect.Token(id))
	}
	if len(answer.Added) > 0 && answer.OperationID != "" {
		fmt.Fprintf(stdout, "hint=undo this batch's marks with {\"op\": \"undo-operation\", \"operationId\": %q}\n", answer.OperationID)
	}
	return 0
}

// parseMeetingsOneID parses a command that takes exactly one meeting id, with
// the same ordering rules and messages as `fetch`. It returns the id, or the
// exit code to stop with.
func parseMeetingsOneID(fs *flag.FlagSet, args []string, verb string, stderr io.Writer) (string, int, bool) {
	if err := fs.Parse(meetingsParseArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", 0, false
		}
		return "", 2, false
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "%s takes exactly one meeting id, got %d arguments: %v\n", verb, fs.NArg(), redactMeetingsArgs(fs.Args()))
		if meetingsArgsLookLikeFlagsAfterPositional(fs.Args()) {
			fmt.Fprintf(stderr, "hint=flags must come before the meeting id, or the id must come last\n")
		}
		fs.Usage()
		return "", 2, false
	}
	meetingID := strings.TrimSpace(fs.Arg(0))
	// PathEscape leaves "." and "..", which would resolve as dot segments and
	// send the request to another route.
	if meetingID == "" || meetingID == "." || meetingID == ".." {
		fmt.Fprintf(stderr, "%s configuration error: %q is not a meeting id; run `cassini meetings list` to find one\n", verb, meetingID)
		return "", 2, false
	}
	return meetingID, 0, true
}

func (c *meetingsClient) annotationsURL(ref string) (*url.URL, error) {
	root, err := c.appRootURL()
	if err != nil {
		return nil, err
	}
	return root.Parse(ref)
}

// getAnnotations GETs an annotations route and decodes it into answer.
func (c *meetingsClient) getAnnotations(ctx context.Context, ref string, answer any) ([]byte, error) {
	target, err := c.annotationsURL(ref)
	if err != nil {
		return nil, err
	}
	body, _, err := c.readListing(ctx, target)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, answer); err != nil {
		return nil, fmt.Errorf("parse the answer from %s: %w", meetingsTargetLabel(target), err)
	}
	return body, nil
}

func (c *meetingsClient) tags(ctx context.Context) ([]byte, meetingsTagsAnswer, error) {
	var answer meetingsTagsAnswer
	body, err := c.getAnnotations(ctx, meetingsAnnotationsTagsPath, &answer)
	if meetingsHTTPStatus(err) == http.StatusNotFound {
		err = errMeetingsTagsUnavailable
	}
	return body, answer, err
}

func (c *meetingsClient) annotate(ctx context.Context, meetingID string, body []byte) ([]byte, meetingsAnnotateAnswer, error) {
	var answer meetingsAnnotateAnswer
	target, err := c.annotationsURL(meetingsAnnotationsMeetingPath + url.PathEscape(meetingID))
	if err != nil {
		return nil, answer, err
	}
	// The reads' transport, so --insecure means the same thing here.
	writer := &http.Client{Timeout: meetingsWriteTimeout, Transport: c.json.Transport, CheckRedirect: refuseMeetingsRedirect}
	resp, err := c.post(ctx, target, body, writer)
	if err != nil {
		return nil, answer, err
	}
	defer resp.Body.Close()
	answerBody, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes))
	if err == nil {
		err = json.Unmarshal(answerBody, &answer)
	}
	if err != nil {
		return nil, answer, fmt.Errorf("the batch was committed, but the answer from %s could not be read: %w", meetingsTargetLabel(target), err)
	}
	return answerBody, answer, nil
}

// meetingsTagNarrowing is what the vocabulary says about a --tag before the app
// is asked to narrow by it. Asking first is not optional: an app older than
// tags ignores the parameter, and would answer the untagged question under the
// tagged one's name. The same answer says whether any readable meeting carries
// the tag, and how many the tag index has not read.
type meetingsTagNarrowing struct {
	checked   bool
	known     bool
	unindexed int
}

func (c *meetingsClient) checkTagNarrowing(ctx context.Context, tag string) (meetingsTagNarrowing, error) {
	_, vocabulary, err := c.tags(ctx)
	if err != nil {
		return meetingsTagNarrowing{}, err
	}
	narrowing := meetingsTagNarrowing{
		checked:   true,
		unindexed: vocabulary.Coverage.Visible - vocabulary.Coverage.Indexed,
	}
	for _, entry := range vocabulary.Tags {
		if entry.TagID == tag || strings.EqualFold(strings.TrimSpace(entry.Label), tag) {
			narrowing.known = true
			break
		}
	}
	return narrowing, nil
}

// report writes what a caller must know about a tag-narrowed answer under key:
// note= beside text output, warning= on stderr beside --json.
func (n meetingsTagNarrowing) report(w io.Writer, key string) {
	if !n.checked {
		return
	}
	if !n.known {
		fmt.Fprintf(w, "%s=no meeting you can read carries that tag, so nothing can match it; run `cassini meetings tags` to see the tags that exist\n", key)
	}
	if n.unindexed > 0 {
		fmt.Fprintf(w, "%s=%d meeting(s) you can read are not in the tag index yet, so --tag cannot reach them and this answer does not cover them\n", key, n.unindexed)
	}
}

// reportAnnotateError maps the app's refusals of a batch onto the exit codes
// `cassini annotate` gives the same conditions.
func reportAnnotateError(stderr io.Writer, cfg meetingsConfig, err error) int {
	status, reason := meetingsHTTPStatus(err), meetingsServerReason(err)
	switch {
	case status == http.StatusBadRequest:
		fmt.Fprintf(stderr, "meetings annotate failed: the app refused these ops: %s\n", reason)
		return annotateExitInvalid
	case status == http.StatusRequestEntityTooLarge:
		fmt.Fprintln(stderr, "meetings annotate failed: the batch is too large for the app; split it into several")
		return annotateExitInvalid
	case status == http.StatusConflict && reason == "revision-conflict":
		fmt.Fprintln(stderr, "meetings annotate failed: the meeting's marks are no longer at the revision you expected; re-read them with `cassini meetings annotations <meeting-id>` and decide again")
		return annotateExitRevision
	case status == http.StatusConflict && reason == "unresolved":
		fmt.Fprintln(stderr, "meetings annotate failed: this meeting's marks were made against different audio, so no new mark can be added to it")
		return annotateExitUnresolved
	case status == http.StatusConflict && reason == "conflict":
		fmt.Fprintln(stderr, "meetings annotate failed: the recording kept changing while the app tried to write it; nothing was written, and re-running the same ops is safe")
		return 1
	}
	return reportMeetingsError(stderr, "annotate", cfg, err)
}

// meetingsServerReason is the app's {"error": "..."} on one line, or the start
// of the raw body, which is what a proxy's error page sends.
func meetingsServerReason(err error) string {
	var httpErr *meetingsHTTPError
	if !errors.As(err, &httpErr) {
		return ""
	}
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(httpErr.Snippet), &body) == nil && strings.TrimSpace(body.Error) != "" {
		return oneLineField(strings.TrimSpace(body.Error))
	}
	return oneLineField(httpErr.Snippet)
}

// writeMeetingsAnswerJSON re-emits the app's answer as sent, indented, so the
// server's payload stays the single contract.
func writeMeetingsAnswerJSON(stdout, stderr io.Writer, verb string, body []byte) int {
	var out bytes.Buffer
	if err := json.Indent(&out, body, "", "  "); err != nil {
		fmt.Fprintf(stderr, "%s failed: the app's answer is not JSON: %v\n", verb, err)
		return 1
	}
	out.WriteByte('\n')
	if _, err := stdout.Write(out.Bytes()); err != nil {
		fmt.Fprintf(stderr, "%s failed: write JSON: %v\n", verb, err)
		return 1
	}
	return 0
}

// describeMeetingsTarget renders what a mark covers: "meeting", or where in the
// recording to listen, as `meetings search` prints a moment.
func describeMeetingsTarget(target portable.AnnotationTarget) string {
	if target.Kind == portable.AnnotationTargetTimeRange && target.StartMS != nil && target.EndMS != nil {
		return formatMeetingsSpan(*target.StartMS, *target.EndMS)
	}
	return inspect.Token(target.Kind)
}

func meetingsResolved(value *bool) string {
	if value == nil {
		return "-"
	}
	return meetingsYesNo(*value)
}
