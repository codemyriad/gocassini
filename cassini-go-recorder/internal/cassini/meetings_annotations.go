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
	"strconv"
	"strings"
	"time"
	"unicode"

	"gocassini/internal/portable"
)

// `cassini meetings tags | annotations | annotate`: the agent's side of tags
// and marks (D-737).
//
// A mark lives inside the recording's .opus, as manifest.annotations, so a
// recording carries its marks wherever it goes. This CLI never touches a file.
// It asks the app, which:
//
//   - reads a meeting's marks AS THE CALLER, so Nextcloud re-checks the
//     permission on the bytes, exactly as `meetings context` does;
//   - writes a batch only after the same visibility check, as its own service
//     account (the recordings mount gives ordinary users a read ceiling), and
//     stamps every new mark with the caller's authenticated Nextcloud user id.
//
// The id is therefore never something this command sends, and cannot be
// spoofed from here. The kind (--actor-kind) is sent, and defaults to agent
// because this CLI is what agents drive: it is self-declared attribution — for
// telling a person's marks from an agent run's, and for undoing a run in one
// step — not an access control.
//
// The published routes' discipline holds throughout: failure is loud, denial
// is empty. A meeting the caller may not read answers 404 exactly like one that
// does not exist, and nothing below tells the two apart.

const (
	meetingsAnnotationsMeetingPath = "annotations/meetings/"
	meetingsAnnotationsTagsPath    = "annotations/tags"

	// maxAnnotateBodyBytes is the app's cap on one annotate request. The ops
	// file is read up to it and no further: a larger batch would only be
	// refused with 413 after the upload, and an unbounded read of stdin is not
	// something to do on an agent's say-so.
	maxAnnotateBodyBytes = 64 << 10

	// meetingsWriteTimeout bounds one annotate request. Far longer than the
	// reads' 20s on purpose: one commit re-uploads the whole recording (about
	// 14 MB for an hour), and the app retries a collision with a concurrent
	// writer up to three times before it answers. Cutting that off early turns
	// a write that probably landed into "maybe", the worst answer a write has.
	meetingsWriteTimeout = 3 * time.Minute
)

// Exit codes annotate shares with `cassini annotate`, so an agent reads the same
// number the same way whichever of the two it drove.
const (
	meetingsExitRevision   = 3 // --expect-revision did not match
	meetingsExitInvalid    = 4 // the ops were refused, or the batch is too large
	meetingsExitUnresolved = 5 // the meeting's marks are bound to other audio
)

// meetingsStdin is where `--ops -` reads from. Package-level so tests can
// substitute it.
var meetingsStdin io.Reader = os.Stdin

var (
	// errMeetingsTagsUnavailable means the app does not serve tags at all: one
	// older than D-737, or a deployment that cannot attribute a mark (the app
	// does not mount the routes there). Distinct from every other failure
	// because no retry and no permission change will help.
	errMeetingsTagsUnavailable = errors.New("this Cassini app does not offer tags and marks")

	// errMeetingsTagsNotReady is the app saying its tag index is still being
	// built from the recordings. Distinct because the right response is to wait.
	errMeetingsTagsNotReady = errors.New("the tag index is not ready yet")

	// errMeetingsAnnotateAnswerUnreadable means the app answered 200 — the batch
	// is committed — and the answer could not be decoded. Distinct from a
	// transport failure, where the commit may or may not have happened.
	errMeetingsAnnotateAnswerUnreadable = errors.New("the batch was committed, but the app's answer could not be read")
)

// meetingsAnnotationsAnswer is GET annotations/meetings/<id>. Annotations is
// the document the recording carries, verbatim, or null; Resolved is the app's
// comparison of its binding with the recording's audio digest, null when there
// is no document.
type meetingsAnnotationsAnswer struct {
	MeetingID   string          `json:"meetingId"`
	Revision    int             `json:"revision"`
	Annotations json.RawMessage `json:"annotations"`
	Resolved    *bool           `json:"resolved"`
}

// meetingsAnnotateRequest is the POST body. It deliberately has no actor id:
// the app takes that from the authenticated caller and nowhere else.
type meetingsAnnotateRequest struct {
	Ops            json.RawMessage `json:"ops"`
	ExpectRevision *int            `json:"expectRevision,omitempty"`
	ActorKind      string          `json:"actorKind"`
	OperationID    string          `json:"operationId,omitempty"`
}

// meetingsAnnotateAnswer is the POST's 200: what the batch did.
type meetingsAnnotateAnswer struct {
	MeetingID   string          `json:"meetingId"`
	Revision    int             `json:"revision"`
	OperationID string          `json:"operationId"`
	Added       []string        `json:"added"`
	Removed     []string        `json:"removed"`
	NotFound    []string        `json:"notFound"`
	Annotations json.RawMessage `json:"annotations"`
	Resolved    *bool           `json:"resolved"`
}

// meetingsTagsAnswer is GET annotations/tags: the vocabulary across the
// caller's readable meetings, and how much of that set the tag index covers.
type meetingsTagsAnswer struct {
	Tags     []meetingsTagEntry `json:"tags"`
	Coverage struct {
		Visible int `json:"visible"`
		Indexed int `json:"indexed"`
	} `json:"coverage"`
}

type meetingsTagEntry struct {
	TagID     string `json:"tagId"`
	Namespace string `json:"namespace"`
	Label     string `json:"label"`
	Meetings  int    `json:"meetings"`
	Marks     int    `json:"marks"`
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
many of your meetings and marks carry it. A tag that is on no meeting you can
read does not appear.

The tag= value, or the label, is what --tag on `+"`meetings search`"+` and
`+"`meetings list`"+` accepts.

`+"\n")
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

	client := newMeetingsClient(cfg)
	body, vocabulary, err := client.tags(ctx)
	if err != nil {
		return reportAnnotationsError(ctx, client, stderr, "tags", cfg, err)
	}
	if *asJSON {
		return writeMeetingsAnswerJSON(stdout, stderr, "tags", body)
	}

	fmt.Fprintf(stdout, "tags=%d caller=%s indexed=%d of %d meeting(s) you can read\n",
		len(vocabulary.Tags), cfg.user, vocabulary.Coverage.Indexed, vocabulary.Coverage.Visible)
	if unindexed := vocabulary.Coverage.Visible - vocabulary.Coverage.Indexed; unindexed > 0 {
		// The honest sentence, as search's coverage note is: without it a short
		// vocabulary reads as "nobody tagged that", when some meetings were
		// never read into the index at all.
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
			meetingsToken(tag.TagID), tag.Meetings, tag.Marks, blankMeetingsDash(tag.Label))
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
whole meeting, or a stretch of it), who made it, and the batch it came in. The
marks are read out of the recording itself, as you, so Nextcloud checks that
you may read it. Use `+"`cassini meetings list`"+` to find the id.

`+"\n")
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

	client := newMeetingsClient(cfg)
	body, answer, err := client.meetingAnnotations(ctx, meetingID)
	if err != nil {
		return reportAnnotationsError(ctx, client, stderr, "annotations", cfg, err)
	}
	if *asJSON {
		return writeMeetingsAnswerJSON(stdout, stderr, "annotations", body)
	}
	printMeetingsAnnotations(stdout, stderr, cfg, firstNonBlank(answer.MeetingID, meetingID), answer)
	return 0
}

// printMeetingsAnnotations renders one meeting's document, one line per mark.
//
// It reads the document with the format's own tolerant reader, so this command
// behaves as the format asks every reader to: a format it does not know is
// named and skipped, never guessed at.
func printMeetingsAnnotations(stdout, stderr io.Writer, cfg meetingsConfig, meetingID string, answer meetingsAnnotationsAnswer) {
	doc, err := portable.ParseAnnotations(answer.Annotations)
	switch {
	case errors.Is(err, portable.ErrAnnotationsFormatUnsupported):
		fmt.Fprintf(stdout, "meeting=%s revision=%d caller=%s\n", meetingsToken(meetingID), answer.Revision, cfg.user)
		fmt.Fprintln(stdout, "note=this meeting's marks are in a format this cassini build does not read; update the CLI, or read the raw document with --json")
		return
	case err != nil:
		fmt.Fprintf(stdout, "meeting=%s revision=%d caller=%s\n", meetingsToken(meetingID), answer.Revision, cfg.user)
		fmt.Fprintf(stderr, "warning=this meeting's marks could not be read (%s); --json shows the raw document\n", oneLineField(err.Error()))
		return
	}
	var tags, marks int
	if doc != nil {
		tags, marks = len(doc.Tags), len(doc.Items)
	}
	fmt.Fprintf(stdout, "meeting=%s revision=%d tags=%d marks=%d resolved=%s caller=%s\n",
		meetingsToken(meetingID), answer.Revision, tags, marks, meetingsResolved(answer.Resolved), cfg.user)
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
		// The label goes last, as a title does everywhere else in this family:
		// it is free text, and anything after it would be ambiguous.
		fmt.Fprintf(stdout, "mark=%s target=%s actor=%s:%s created=%s operation=%s tag_id=%s tag=%s\n",
			meetingsToken(item.ID),
			describeMeetingsTarget(item.Target),
			meetingsToken(item.Actor.Kind), meetingsToken(item.Actor.ID),
			meetingsToken(item.CreatedAtUTC),
			meetingsToken(item.OperationID),
			meetingsToken(item.TagID),
			blankMeetingsDash(labels[item.TagID]))
	}
	fmt.Fprintf(stdout, "hint=add or remove marks with `cassini meetings annotate %s --ops <file>`; --json carries each range's exact milliseconds\n", meetingsToken(meetingID))
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

Every new mark is attributed to the Nextcloud account you authenticate as, with
--actor-kind saying whether a person or an agent made it (agent by default).
Marking is idempotent: a mark that already exists is not added twice, so
re-running a batch is safe.

`+"\n")
		fs.PrintDefaults()
	}
	meetingID, code, ok := parseMeetingsOneID(fs, args, "annotate", stderr)
	if !ok {
		return code
	}
	passed := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { passed[f.Name] = true })

	// Everything about the request is checked before the network call: a
	// malformed batch is the caller's to fix either way.
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
		value := *expectRevision
		expect = &value
	}
	ops, code, err := readMeetingsAnnotateOps(*opsPath)
	if err != nil {
		fmt.Fprintf(stderr, "annotate configuration error: %v\n", err)
		return code
	}
	body, err := json.Marshal(meetingsAnnotateRequest{
		Ops: ops, ExpectRevision: expect, ActorKind: kind, OperationID: strings.TrimSpace(*operationID),
	})
	if err != nil {
		fmt.Fprintf(stderr, "annotate failed: encode the request: %v\n", err)
		return 1
	}
	if len(body) > maxAnnotateBodyBytes {
		fmt.Fprintf(stderr, "annotate configuration error: the batch is larger than %d KiB, the most the app accepts in one request; split it into several\n", maxAnnotateBodyBytes>>10)
		return meetingsExitInvalid
	}
	if err := resolveMeetingsConfig(fs, &cfg); err != nil {
		fmt.Fprintf(stderr, "annotate configuration error: %v\n", err)
		return 2
	}
	warnAboutInsecureTLS(stderr, cfg)

	client := newMeetingsClient(cfg)
	answerBody, answer, err := client.annotate(ctx, meetingID, body)
	if err != nil {
		return reportAnnotationsError(ctx, client, stderr, "annotate", cfg, err)
	}
	if *asJSON {
		return writeMeetingsAnswerJSON(stdout, stderr, "annotate", answerBody)
	}

	fmt.Fprintf(stdout, "annotated=%s revision=%d operation=%s added=%d removed=%d not_found=%d resolved=%s caller=%s\n",
		meetingsToken(firstNonBlank(answer.MeetingID, meetingID)), answer.Revision, meetingsToken(answer.OperationID),
		len(answer.Added), len(answer.Removed), len(answer.NotFound), meetingsResolved(answer.Resolved), cfg.user)
	for _, id := range answer.Added {
		fmt.Fprintf(stdout, "change=added mark=%s\n", meetingsToken(id))
	}
	for _, id := range answer.Removed {
		fmt.Fprintf(stdout, "change=removed mark=%s\n", meetingsToken(id))
	}
	for _, id := range answer.NotFound {
		fmt.Fprintf(stdout, "change=not-found id=%s\n", meetingsToken(id))
	}
	if len(answer.Added) > 0 && answer.OperationID != "" {
		// The whole point of stamping a batch: an agent run that went wrong is
		// one op away from gone, and the op is printed where the run will see it.
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
	// "." and ".." are the only ids PathEscape leaves able to move the request:
	// resolved against the app root they are dot segments, and would send the
	// request to some other route entirely.
	if meetingID == "" || meetingID == "." || meetingID == ".." {
		fmt.Fprintf(stderr, "%s configuration error: %q is not a meeting id; run `cassini meetings list` to find one\n", verb, meetingID)
		return "", 2, false
	}
	return meetingID, 0, true
}

// readMeetingsAnnotateOps reads the ops document and returns its ops array
// untouched, with the exit code to use if it cannot.
//
// The ops themselves are not validated here. The app validates them — by
// running `cassini annotate apply`, which owns that contract — and answers 400
// with the reason; a second validator in this client would be one more copy of
// the contract to keep in step. What is checked is only what makes the request
// itself wrong: a file that cannot be read (usage, 2), or one that is not a
// single {"ops": [...]} document with at least one op, or is too large to send
// (invalid ops, 4 — the same code the app's refusal earns).
func readMeetingsAnnotateOps(path string) (json.RawMessage, int, error) {
	source, name := meetingsStdin, "stdin"
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return nil, 2, fmt.Errorf("read --ops: %w", err)
		}
		defer file.Close()
		source, name = file, path
	}
	raw, err := io.ReadAll(io.LimitReader(source, maxAnnotateBodyBytes+1))
	if err != nil {
		return nil, 2, fmt.Errorf("read --ops %s: %w", name, err)
	}
	if len(raw) > maxAnnotateBodyBytes {
		return nil, meetingsExitInvalid, fmt.Errorf("--ops %s is larger than %d KiB, the most the app accepts in one request; split it into several", name, maxAnnotateBodyBytes>>10)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	// Strict on the envelope. A member other than "ops" — an expectRevision
	// typed into the file, say — would otherwise be silently dropped, and the
	// batch would run without the guard its author thought it had. Those are
	// flags here.
	decoder.DisallowUnknownFields()
	var doc struct {
		Ops json.RawMessage `json:"ops"`
	}
	if err := decoder.Decode(&doc); err != nil {
		return nil, meetingsExitInvalid, fmt.Errorf(`--ops %s is not an {"ops": [...]} document: %v`, name, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, meetingsExitInvalid, fmt.Errorf("--ops %s holds more than one JSON document", name)
	}
	var ops []json.RawMessage
	if err := json.Unmarshal(doc.Ops, &ops); err != nil {
		return nil, meetingsExitInvalid, fmt.Errorf(`--ops %s: "ops" must be an array of ops`, name)
	}
	if len(ops) == 0 {
		return nil, meetingsExitInvalid, fmt.Errorf("--ops %s holds no ops, so there is nothing to apply", name)
	}
	return doc.Ops, 0, nil
}

func (c *meetingsClient) annotationsURL(ref string) (*url.URL, error) {
	root, err := c.appRootURL()
	if err != nil {
		return nil, err
	}
	target, err := root.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("build annotations URL: %w", err)
	}
	return target, nil
}

func (c *meetingsClient) meetingAnnotationsURL(meetingID string) (*url.URL, error) {
	return c.annotationsURL(meetingsAnnotationsMeetingPath + url.PathEscape(meetingID))
}

// tags GETs the caller's vocabulary. A 404 means the app has no tags at all and
// a 503 that its index is still being built; both come back as their own errors.
func (c *meetingsClient) tags(ctx context.Context) ([]byte, meetingsTagsAnswer, error) {
	target, err := c.annotationsURL(meetingsAnnotationsTagsPath)
	if err != nil {
		return nil, meetingsTagsAnswer{}, err
	}
	body, _, err := c.readListing(ctx, target)
	if err != nil {
		switch meetingsHTTPStatus(err) {
		case http.StatusNotFound:
			return nil, meetingsTagsAnswer{}, errMeetingsTagsUnavailable
		case http.StatusServiceUnavailable:
			return nil, meetingsTagsAnswer{}, errMeetingsTagsNotReady
		}
		return nil, meetingsTagsAnswer{}, err
	}
	var answer meetingsTagsAnswer
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, meetingsTagsAnswer{}, fmt.Errorf("parse tags from %s: %w", meetingsTargetLabel(target), err)
	}
	return body, answer, nil
}

func (c *meetingsClient) meetingAnnotations(ctx context.Context, meetingID string) ([]byte, meetingsAnnotationsAnswer, error) {
	target, err := c.meetingAnnotationsURL(meetingID)
	if err != nil {
		return nil, meetingsAnnotationsAnswer{}, err
	}
	body, _, err := c.readListing(ctx, target)
	if err != nil {
		return nil, meetingsAnnotationsAnswer{}, err
	}
	var answer meetingsAnnotationsAnswer
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, meetingsAnnotationsAnswer{}, fmt.Errorf("parse annotations from %s: %w", meetingsTargetLabel(target), err)
	}
	return body, answer, nil
}

func (c *meetingsClient) annotate(ctx context.Context, meetingID string, body []byte) ([]byte, meetingsAnnotateAnswer, error) {
	target, err := c.meetingAnnotationsURL(meetingID)
	if err != nil {
		return nil, meetingsAnnotateAnswer{}, err
	}
	// A client of its own, for meetingsWriteTimeout, on the same transport as
	// the reads so --insecure means the same thing here, and refusing redirects
	// for the same reason they do.
	writer := &http.Client{
		Timeout:       meetingsWriteTimeout,
		Transport:     c.json.Transport,
		CheckRedirect: refuseMeetingsRedirect,
	}
	resp, err := c.post(ctx, target, body, writer)
	if err != nil {
		return nil, meetingsAnnotateAnswer{}, err
	}
	defer resp.Body.Close()
	answerBody, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
	if err != nil || len(answerBody) > maxCatalogBytes {
		return nil, meetingsAnnotateAnswer{}, fmt.Errorf("%w (%s)", errMeetingsAnnotateAnswerUnreadable, meetingsTargetLabel(target))
	}
	var answer meetingsAnnotateAnswer
	if err := json.Unmarshal(answerBody, &answer); err != nil {
		return nil, meetingsAnnotateAnswer{}, fmt.Errorf("%w (%s): %v", errMeetingsAnnotateAnswerUnreadable, meetingsTargetLabel(target), err)
	}
	return answerBody, answer, nil
}

// annotationsOffered tells apart the two things a 404 on a meeting's
// annotations can mean: an app with no tags at all, or a meeting this caller
// cannot read. Asking the tags route settles it without revealing anything —
// it answers per caller and never 404s for a denial — and it is asked only
// once a 404 has already happened.
func (c *meetingsClient) annotationsOffered(ctx context.Context) bool {
	_, _, err := c.tags(ctx)
	return !errors.Is(err, errMeetingsTagsUnavailable)
}

// meetingsTagNarrowing is what the vocabulary says about a --tag before the app
// is asked to narrow by it.
//
// Asking first is not optional. An app older than tags does not know the
// parameter and ignores it, so a tagged search against one would answer the
// untagged question under the tagged one's name: the failure `meetings search`
// already refuses to commit by never falling back to a local scan. The tags
// route exists exactly where tag narrowing does, so its 404 is the refusal. The
// same answer then buys two honest sentences: whether any meeting the caller can
// read carries the tag at all, and how many of their meetings the tag index has
// not read.
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

// report writes what a caller must know about a tag-narrowed answer, as lines
// under key: note= beside the text output, and warning= on stderr beside
// --json, so the path an agent reads is not the one that never hears it.
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

// reportAnnotationsError phrases a failure from the annotation routes, or from
// the tag check in front of a narrowed search or list, and returns the exit
// code. What it does not recognise goes to reportMeetingsError, so the shared
// statuses read the same everywhere in this family.
func reportAnnotationsError(ctx context.Context, client *meetingsClient, stderr io.Writer, verb string, cfg meetingsConfig, err error) int {
	switch {
	case errors.Is(err, errMeetingsTagsUnavailable):
		fmt.Fprintf(stderr, "meetings %s failed: this Cassini app does not offer tags and marks; ask an administrator to update it\n", verb)
		return 1
	case errors.Is(err, errMeetingsTagsNotReady):
		fmt.Fprintf(stderr, "meetings %s failed: the app is still building its tag index from the recordings; wait a moment and retry\n", verb)
		return 1
	case errors.Is(err, errMeetingsAnnotateAnswerUnreadable):
		fmt.Fprintf(stderr, "meetings %s failed: %v\n", verb, err)
		fmt.Fprintf(stderr, "hint=check what the meeting now carries with `cassini meetings annotations <meeting-id>` before re-running anything\n")
		return 1
	}

	reason := meetingsServerReason(err)
	switch meetingsHTTPStatus(err) {
	case http.StatusNotFound:
		if !client.annotationsOffered(ctx) {
			fmt.Fprintf(stderr, "meetings %s failed: this Cassini app does not offer tags and marks; ask an administrator to update it\n", verb)
			return 1
		}
		// Absent or unreadable: the shared wording, which never says which.
		return reportMeetingsError(stderr, verb, cfg, err)
	case http.StatusBadRequest:
		if verb == "annotate" {
			fmt.Fprintf(stderr, "meetings annotate failed: the app refused these ops: %s\n", reason)
			return meetingsExitInvalid
		}
		fmt.Fprintf(stderr, "meetings %s failed: the app refused the request: %s\n", verb, reason)
		return 2
	case http.StatusRequestEntityTooLarge:
		fmt.Fprintf(stderr, "meetings %s failed: the batch is too large for the app (at most %d KiB and 200 ops per request); split it into several\n", verb, maxAnnotateBodyBytes>>10)
		return meetingsExitInvalid
	case http.StatusConflict:
		switch reason {
		case "revision-conflict":
			fmt.Fprintf(stderr, "meetings %s failed: the meeting's marks are no longer at the revision you expected, because someone else changed them; re-read them with `cassini meetings annotations <meeting-id>` and decide again\n", verb)
			return meetingsExitRevision
		case "unresolved":
			fmt.Fprintf(stderr, "meetings %s failed: this meeting's marks were made against different audio (the recording changed after they were made), so no new mark can be added to it\n", verb)
			return meetingsExitUnresolved
		case "conflict":
			fmt.Fprintf(stderr, "meetings %s failed: the recording kept changing while the app tried to write it, and it gave up after retrying; nothing was written, and re-running the same ops is safe\n", verb)
			return 1
		}
		fmt.Fprintf(stderr, "meetings %s failed: the app reported a conflict: %s\n", verb, reason)
		return 1
	case http.StatusMethodNotAllowed:
		fmt.Fprintf(stderr, "meetings %s failed: the app did not accept this request on its annotations route; its route declarations may predate tags (they take effect only when the app's version changes), so ask an administrator to update it\n", verb)
		return 1
	case http.StatusServiceUnavailable:
		fmt.Fprintf(stderr, "meetings %s failed: the app is still building its tag index from the recordings; wait a moment and retry\n", verb)
		return 1
	case 0:
		if verb == "annotate" {
			// No answer at all: the batch may or may not have landed. Saying so
			// is the only honest report, and the ops being idempotent is what
			// makes it survivable.
			fmt.Fprintf(stderr, "meetings annotate failed: %v\n", err)
			fmt.Fprintf(stderr, "hint=the batch may or may not have been committed; marking is idempotent, so check with `cassini meetings annotations <meeting-id>` or re-run the same ops\n")
			return 1
		}
	}
	return reportMeetingsError(stderr, verb, cfg, err)
}

// meetingsServerReason is the app's own explanation of a refusal, from its
// {"error": "..."} body, flattened to one line. It falls back to the start of
// the raw body, which is what a proxy's error page sends.
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

// writeMeetingsAnswerJSON re-emits the app's answer as it was sent, indented,
// so the server's payload stays the single contract — as `list --json` keeps
// the catalog's.
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
	switch target.Kind {
	case portable.AnnotationTargetMeeting:
		return "meeting"
	case portable.AnnotationTargetTimeRange:
		if target.StartMS != nil && target.EndMS != nil {
			return formatMeetingsSpan(*target.StartMS, *target.EndMS)
		}
	}
	return meetingsToken(target.Kind)
}

func meetingsResolved(value *bool) string {
	if value == nil {
		return "-"
	}
	return meetingsYesNo(*value)
}

// meetingsToken renders a server-supplied value that sits mid-line in a
// key=value record. A value that is one plain token passes through; any other
// is Go-quoted.
//
// blankMeetingsDash is not enough here. It keeps a value on one line, but a
// space survives it, and that is fine only for the last field of a line — a
// title. Mid-line, a Nextcloud user id with a space in it (Nextcloud allows
// them) or a tag label would run into the next field, and one shaped like
// "x resolved=yes" would add a fact. Quoting keeps every value one field;
// strconv.Quote also escapes the control, separator and bidi characters
// oneLineField flattens.
func meetingsToken(value string) string {
	if value == "" {
		return "-"
	}
	for _, r := range value {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) || r == '"' || r == '=' || r == ',' {
			return strconv.Quote(value)
		}
	}
	return value
}
