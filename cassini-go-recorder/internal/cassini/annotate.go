package cassini

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"

	"gocassini/internal/portable"
)

// `cassini annotate` reads and writes the tags and marks a recording carries
// inside its own .opus, as manifest.annotations (D-737).
//
// Marks live in the file rather than beside it so that a recording stays
// complete when it leaves the app — downloaded, shared, handed to someone with
// no Nextcloud. That puts every mark on the same write path as the room and the
// summary: decode the payload, edit the JSON, rebuild the OpusTags, copy the
// audio, verify, rename. This command is that path for annotations, built on
// retag's commitPortableManifestRewrite and held to the same rules:
//
//   - THE AUDIO IS NOT RE-ENCODED, and the output is checked against the audio
//     digest before it replaces anything. The audio digest excludes OpusTags,
//     so annotating never moves it: it stays the recording's identity and the
//     binding every time range is pinned to.
//   - NOTHING OUTSIDE `annotations` CHANGES. The manifest is edited as a generic
//     JSON document, and the staged file is read back and compared with the one
//     it came from — the manifest minus annotations, and every tag outside the
//     main payload — before it is committed. The transcripts live in those
//     tags; a chunk set lost in the muxer would leave a file that plays and can
//     no longer be read as a meeting.
//   - A BATCH IS ONE WRITE. The operator re-uploads the whole file per commit
//     (~14 MB for an hour), so the ops a request carries are applied together
//     and written once — and a batch that changes nothing writes nothing.
//   - A RETRY IS SAFE. Marking what is already marked is a no-op, and removing
//     what is already gone is reported rather than refused, so a request sent
//     twice after a lost response lands once. `If-Match` on the upload, not
//     `revision`, is what serialises concurrent writers.
//
// The operator runs this binary rather than importing it — the two are separate
// modules — so the result document and the exit codes below are a contract
// with cassini-operator/internal/operator/annotate_cli.go. Change both sides
// together or neither.

// annotateResultFormat names the result document every subcommand prints.
const annotateResultFormat = "cassini.annotate.result.v1"

// The exit codes are part of the operator contract: each one maps to a distinct
// HTTP answer, so a failure must land on the right one rather than on 1.
const (
	annotateExitRuntime    = 1
	annotateExitUsage      = 2
	annotateExitRevision   = 3 // --expect-revision did not match; nothing written
	annotateExitInvalid    = 4 // the ops, or the document they would produce, are invalid; nothing written
	annotateExitUnresolved = 5 // the marks are bound to different audio; apply refuses; nothing written
)

// These mirror portable's own id and namespace patterns, which it does not
// export. They exist so a bad flag is refused up front with a message naming
// the flag; ValidateAnnotations still runs on every document before it is
// written, so a drift between the two can only make a write stricter, never let
// a bad document through.
var (
	annotateIDPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	annotateNamespacePattern = regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// annotateResult is what every `cassini annotate … --json` prints. Every member
// is always present — lists empty rather than absent, `resolved` null rather
// than absent — so a reader never has to tell "none" from "not reported".
type annotateResult struct {
	Format string `json:"format"`
	// Annotations is the document the file now carries, as read back out of
	// the file; null when it carries none.
	Annotations json.RawMessage `json:"annotations"`
	// Revision is the document's revision; 0 when the file carries none.
	Revision int `json:"revision"`
	// OperationID is the id apply stamps on the marks this batch adds. Empty
	// for show and carry, which add none.
	OperationID string `json:"operationId"`
	// Added and Removed are the item ids the batch actually added and removed,
	// net: an item added and removed within one batch is in neither.
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	// NotFound lists the ids an op named that matched nothing in the file.
	NotFound []string `json:"notFound"`
	// Carried is how many marks carry moved onto the sealed file.
	Carried int `json:"carried"`
	// Resolved says whether the marks were made against this file's audio.
	// Null when there are no annotations to judge.
	Resolved *bool `json:"resolved"`
	// AudioOpusSHA256 is the FILE's audio digest, integrity.opusAudioSha256 —
	// the recording's identity. The marks' own binding is inside Annotations.
	AudioOpusSHA256 string `json:"audioOpusSha256"`
	// ContainerSHA256 is the sha256 of the file as it now is on disk. It moves
	// with every commit and every carry: never identity, never the seal.
	ContainerSHA256 string `json:"containerSha256"`
}

func newAnnotateResult(audioDigest string) annotateResult {
	return annotateResult{
		Format:          annotateResultFormat,
		Added:           []string{},
		Removed:         []string{},
		NotFound:        []string{},
		AudioOpusSHA256: audioDigest,
	}
}

// annotateFailure carries the exit code a failure maps to. Any error that is
// not one is a runtime failure.
type annotateFailure struct {
	code int
	err  error
}

func (f *annotateFailure) Error() string { return f.err.Error() }
func (f *annotateFailure) Unwrap() error { return f.err }

func annotateFail(code int, format string, args ...any) error {
	return &annotateFailure{code: code, err: fmt.Errorf(format, args...)}
}

func annotateExitCodeFor(err error) int {
	var failure *annotateFailure
	if errors.As(err, &failure) {
		return failure.code
	}
	return annotateExitRuntime
}

// runAnnotate dispatches the three subcommands. stdin is where `--ops -` reads
// from; the operator sends the ops that way so a request body never touches
// the disk.
func runAnnotate(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printAnnotateUsage(stderr)
		return annotateExitUsage
	}
	switch args[0] {
	case "help", "-h", "--help":
		printAnnotateUsage(stdout)
		return 0
	case "show":
		return runAnnotateShow(args[1:], stdout, stderr)
	case "apply":
		return runAnnotateApply(ctx, args[1:], stdin, stdout, stderr)
	case "carry":
		return runAnnotateCarry(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown annotate subcommand %q\n\n", args[0])
		printAnnotateUsage(stderr)
		return annotateExitUsage
	}
}

func printAnnotateUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  cassini annotate show  <file.opus> [--json]
  cassini annotate apply <file.opus> --ops <ops.json|-> --actor-id <id>
                         [--actor-kind person|agent] [--operation-id <id>]
                         [--expect-revision N] [--tag-namespace <urn:uuid:...>]
                         [--out <path>] [--json]
  cassini annotate carry <delivered.opus> <sealed.opus> --out <path> [--json]

Read and write the tags and marks a portable .opus carries in its manifest
(manifest.annotations). The audio is copied, never re-encoded, nothing outside
the annotations changes, and every write is verified before it replaces a file.

apply takes {"ops":[...]}, applied in order as one batch and written once:
  {"op":"mark","tag":{"label":"hiring"},"target":{"kind":"meeting"}}
  {"op":"mark","tag":{"id":"tag_..."},"target":{"kind":"time-range","startMs":869000,"endMs":884000}}
  {"op":"unmark","itemId":"mk_..."}
  {"op":"unmark-tag","tagId":"tag_..."}            (optionally with "target")
  {"op":"undo-operation","operationId":"op_..."}
  {"op":"relabel","tagId":"tag_...","label":"new name"}
A mark finds its tag by id, else by label (trimmed, case-insensitive), else
defines it. Marking what is already marked, or removing what is already gone,
changes nothing, so a batch can safely be sent twice. --out defaults to the
input, rewritten in place.

carry writes the delivered copy's marks into a copy of the sealed file at --out.
The sealed file is never modified. Marks made against different audio are
carried as they are and reported unresolved.

Exit codes: 0 ok, 1 runtime, 2 usage, 3 --expect-revision mismatch, 4 invalid
ops or a document that would not validate, 5 the file's marks are bound to
different audio (apply refuses). Nothing is written unless the exit is 0.
`)
}

// parseAnnotateArgs parses flags and file arguments in any order — the operator
// writes `apply <in> --ops - …`, a person may put the flags first — and checks
// the number of files. It answers the exit code to stop with, or -1 to go on.
func parseAnnotateArgs(fs *flag.FlagSet, args []string, files int) ([]string, int) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, 0
			}
			return nil, annotateExitUsage
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		// fs.Parse consumes a bare "--" and stops: everything after it is a
		// file, even if it looks like a flag.
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			positional = append(positional, rest...)
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	if len(positional) != files {
		fmt.Fprintf(fs.Output(), "%s takes %d file argument(s), got %d\n\n", fs.Name(), files, len(positional))
		fs.Usage()
		return nil, annotateExitUsage
	}
	return positional, -1
}

// finishAnnotate prints the result or the failure and answers the exit code.
// A failure's message goes to stderr: for exit 4 the operator shows it to the
// caller, so those messages describe the ops and never name a local path.
func finishAnnotate(name string, result annotateResult, err error, emitJSON bool, stdout, stderr io.Writer, human func(io.Writer)) int {
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return annotateExitCodeFor(err)
	}
	if emitJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintf(stderr, "%s: write result: %v\n", name, err)
			return annotateExitRuntime
		}
		return 0
	}
	human(stdout)
	return 0
}

// ---------------------------------------------------------------- show

func runAnnotateShow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini annotate show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	emitJSON := fs.Bool("json", false, "print the result document instead of a summary")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini annotate show ./Meeting.opus [--json]

Print the tags and marks a portable .opus carries. Changes nothing.

`)
		fs.PrintDefaults()
	}
	files, code := parseAnnotateArgs(fs, args, 1)
	if code >= 0 {
		return code
	}
	result, err := annotateShow(files[0], stderr)
	return finishAnnotate(fs.Name(), result, err, *emitJSON, stdout, stderr, func(w io.Writer) {
		fmt.Fprintf(w, "%s\n", files[0])
		printAnnotateDocumentSummary(w, result)
	})
}

func annotateShow(path string, stderr io.Writer) (annotateResult, error) {
	source, err := readAnnotateSource(path)
	if err != nil {
		return annotateResult{}, err
	}
	if source.unsupported != nil {
		// The format's own rule: a reader that does not know the format ignores
		// the whole member. Said out loud, but not a failure — the recording is
		// still perfectly readable.
		fmt.Fprintf(stderr, "cassini annotate show: %v; reporting no marks\n", source.unsupported)
	}
	result := newAnnotateResult(source.audioDigest())
	describeAnnotateSource(&result, source)
	if result.ContainerSHA256, err = annotateFileSHA256(path); err != nil {
		return annotateResult{}, err
	}
	return result, nil
}

// ---------------------------------------------------------------- apply

func runAnnotateApply(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var (
		opsPath        string
		actorID        string
		actorKind      string
		operationID    string
		tagNamespace   string
		expectRevision int
		outPath        string
		emitJSON       bool
	)
	fs := flag.NewFlagSet("cassini annotate apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opsPath, "ops", "", `the ops document, {"ops":[...]}: a file, or - for stdin (required)`)
	fs.StringVar(&actorID, "actor-id", "", "who is marking: the authenticated user id (required)")
	fs.StringVar(&actorKind, "actor-kind", portable.AnnotationActorPerson, "person or agent")
	fs.StringVar(&operationID, "operation-id", "", "stamp the marks this batch adds with this id (minted when absent)")
	fs.StringVar(&tagNamespace, "tag-namespace", "", "urn:uuid namespace for the file's first write; ignored once the file has one")
	fs.IntVar(&expectRevision, "expect-revision", 0, "refuse (exit 3) unless the file is at this revision; 0 means it carries none")
	fs.StringVar(&outPath, "out", "", "write the result here instead of rewriting the input in place")
	fs.BoolVar(&emitJSON, "json", false, "print the result document instead of a summary")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini annotate apply ./Meeting.opus --ops ./ops.json --actor-id alice
  echo '{"ops":[...]}' | cassini annotate apply ./Meeting.opus --ops - --actor-id alice --json

Apply a batch of ops to the tags and marks a portable .opus carries, as one
rewrite. See "cassini annotate --help" for the ops and the exit codes.

`)
		fs.PrintDefaults()
	}
	files, code := parseAnnotateArgs(fs, args, 1)
	if code >= 0 {
		return code
	}
	provided := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })

	usage := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "annotate apply configuration error: "+format+"\n", args...)
		return annotateExitUsage
	}
	if strings.TrimSpace(opsPath) == "" {
		return usage("--ops is required (a file, or - for stdin)")
	}
	// Refused rather than defaulted: every mark names who made it, and on the
	// operator path that is the authenticated caller. A blank id is a caller
	// that failed to pass one, not an anonymous mark.
	if strings.TrimSpace(actorID) == "" {
		return usage("--actor-id is required")
	}
	if tagNamespace != "" && !annotateNamespacePattern.MatchString(tagNamespace) {
		return usage("--tag-namespace %q is not urn:uuid:<lowercase uuid>", tagNamespace)
	}
	var expect *int
	if provided["expect-revision"] {
		if expectRevision < 0 {
			return usage("--expect-revision must be 0 or greater, got %d", expectRevision)
		}
		expect = &expectRevision
	}
	if outPath == "" {
		outPath = files[0]
	}
	if !isPortableMeetingOutput(outPath) {
		return usage("--out must be a .opus file, got %s", outPath)
	}

	// The actor kind and the operation id come from the request on the
	// operator path, so a bad one is the caller's invalid input (exit 4, shown
	// to them), not a misuse of this command.
	invalid := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "%s: "+format+"\n", append([]any{fs.Name()}, args...)...)
		return annotateExitInvalid
	}
	if actorKind != portable.AnnotationActorPerson && actorKind != portable.AnnotationActorAgent {
		return invalid("--actor-kind must be %s or %s, got %q", portable.AnnotationActorPerson, portable.AnnotationActorAgent, actorKind)
	}
	if operationID != "" && !annotateIDPattern.MatchString(operationID) {
		return invalid("--operation-id %q is not a valid id", operationID)
	}

	var raw []byte
	var err error
	if opsPath == "-" {
		raw, err = io.ReadAll(stdin)
	} else {
		raw, err = os.ReadFile(opsPath)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: read --ops: %v\n", fs.Name(), err)
		return annotateExitRuntime
	}
	// Parsed before the recording is even opened: a malformed batch should cost
	// nothing and change nothing.
	ops, err := parseAnnotateOps(raw)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", fs.Name(), err)
		return annotateExitCodeFor(err)
	}
	if operationID == "" {
		if operationID, err = portable.NewAnnotationOperationID(); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", fs.Name(), err)
			return annotateExitRuntime
		}
	}

	request := annotateApplyRequest{
		inputPath: files[0],
		outPath:   outPath,
		ops:       ops,
		stamp: annotateStamp{
			ActorKind:   actorKind,
			ActorID:     actorID,
			OperationID: operationID,
			// Whole seconds, UTC, ending Z — the format's createdAtUtc shape.
			CreatedAt: time.Now().UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z"),
		},
		tagNamespace:   tagNamespace,
		expectRevision: expect,
	}
	result, rewritten, err := annotateApply(ctx, request)
	return finishAnnotate(fs.Name(), result, err, emitJSON, stdout, stderr, func(w io.Writer) {
		if rewritten {
			fmt.Fprintf(w, "annotated -> %s\n", outPath)
		} else {
			fmt.Fprintf(w, "unchanged -> %s (the batch changed nothing, so the file was not rewritten)\n", outPath)
		}
		fmt.Fprintf(w, "  operation %s: added %d, removed %d\n", result.OperationID, len(result.Added), len(result.Removed))
		if len(result.NotFound) > 0 {
			fmt.Fprintf(w, "  not found: %s\n", strings.Join(result.NotFound, ", "))
		}
		printAnnotateDocumentSummary(w, result)
	})
}

type annotateApplyRequest struct {
	inputPath      string
	outPath        string
	ops            []annotateOp
	stamp          annotateStamp
	tagNamespace   string
	expectRevision *int
}

// annotateApply applies one batch and writes it once. It answers whether the
// file was rewritten: a batch that changes nothing is not a commit, so it gets
// no new revision and no rewrite — only a byte-for-byte copy when --out names
// another file, because a caller that asked for an output must find one there.
//
// Every refusal happens before the output is touched, so exits 3, 4 and 5 leave
// both the input and --out exactly as they were.
func annotateApply(ctx context.Context, req annotateApplyRequest) (annotateResult, bool, error) {
	source, err := readAnnotateSource(req.inputPath)
	if err != nil {
		return annotateResult{}, false, err
	}
	if source.unsupported != nil {
		// Writing a v1 document here would silently destroy marks a newer
		// writer made. A reader may ignore them; a writer must not.
		return annotateResult{}, false, fmt.Errorf("%v; refusing to replace marks this build cannot read", source.unsupported)
	}
	digest := source.audioDigest()
	current := 0
	if source.doc != nil {
		current = source.doc.Revision
	}
	if req.expectRevision != nil && *req.expectRevision != current {
		return annotateResult{}, false, annotateFail(annotateExitRevision,
			"the file is at revision %d, not the expected %d; nothing was written", current, *req.expectRevision)
	}
	// Marks bound to other audio have time ranges that mean nothing against
	// this audio. Adding to such a document, or editing it, would mix two
	// timelines under one binding; migrating them is future work.
	if source.doc != nil && !source.doc.Resolved(digest) {
		return annotateResult{}, false, annotateFail(annotateExitUnresolved,
			"the file's marks were made against different audio (bound to %s, this audio is %s); apply will not change them until they are migrated, and nothing was written",
			source.doc.AudioOpusSHA256, digest)
	}
	durationMS := source.manifest.Audio.DurationMS
	outcome, err := applyAnnotationOps(source.doc, req.ops, durationMS, req.stamp)
	if err != nil {
		return annotateResult{}, false, err
	}

	result := newAnnotateResult(digest)
	result.OperationID = req.stamp.OperationID
	result.NotFound = outcome.NotFound

	if !outcome.Changed {
		resolvedOut, err := preparePortableMeetingOutput(req.outPath)
		if err != nil {
			return annotateResult{}, false, err
		}
		if same, err := sameFilePath(source.path, resolvedOut); err != nil {
			return annotateResult{}, false, err
		} else if !same {
			if err := copyPortableMeetingFile(source.path, resolvedOut); err != nil {
				return annotateResult{}, false, err
			}
		}
		describeAnnotateSource(&result, source)
		if result.ContainerSHA256, err = annotateFileSHA256(resolvedOut); err != nil {
			return annotateResult{}, false, err
		}
		return result, false, nil
	}

	doc := outcome.Doc
	doc.Format = portable.AnnotationsFormatV1
	doc.Revision = current + 1
	if source.doc != nil {
		// The binding and the namespace are the file's, never the request's: a
		// namespace that changed would split one tag into two across the
		// archive, and the binding is the audio these marks were made against.
		doc.AudioOpusSHA256 = source.doc.AudioOpusSHA256
		doc.TagNamespace = source.doc.TagNamespace
	} else {
		doc.AudioOpusSHA256 = digest
		doc.TagNamespace = req.tagNamespace
		if doc.TagNamespace == "" {
			if doc.TagNamespace, err = newAnnotationTagNamespace(); err != nil {
				return annotateResult{}, false, err
			}
		}
	}
	if err := portable.ValidateAnnotations(doc, durationMS); err != nil {
		return annotateResult{}, false, &annotateFailure{
			code: annotateExitInvalid,
			err:  fmt.Errorf("the batch would leave annotations that do not validate: %w", err),
		}
	}

	resolvedOut, err := preparePortableMeetingOutput(req.outPath)
	if err != nil {
		return annotateResult{}, false, err
	}
	written, err := writeAnnotateDocument(ctx, source, resolvedOut, doc, durationMS)
	if err != nil {
		return annotateResult{}, false, err
	}
	resolved := doc.Resolved(digest)
	result.Annotations = written
	result.Revision = doc.Revision
	result.Resolved = &resolved
	result.Added = outcome.Added
	result.Removed = outcome.Removed
	if result.ContainerSHA256, err = annotateFileSHA256(resolvedOut); err != nil {
		return annotateResult{}, false, err
	}
	return result, true, nil
}

// ---------------------------------------------------------------- carry

func runAnnotateCarry(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var outPath string
	var emitJSON bool
	fs := flag.NewFlagSet("cassini annotate carry", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&outPath, "out", "", "output .opus (required; must not be the sealed file)")
	fs.BoolVar(&emitJSON, "json", false, "print the result document instead of a summary")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini annotate carry ./delivered.opus ./sealed.opus --out ./outgoing.opus [--json]

Write the marks the delivered copy carries into a copy of the sealed file, so
a republish does not replace every mark with a file that has none. The sealed
file is never modified. Marks made against different audio are carried with
their original binding and reported unresolved.

`)
		fs.PrintDefaults()
	}
	files, code := parseAnnotateArgs(fs, args, 2)
	if code >= 0 {
		return code
	}
	delivered, sealed := files[0], files[1]
	if strings.TrimSpace(outPath) == "" {
		fmt.Fprintln(stderr, "annotate carry configuration error: --out is required")
		return annotateExitUsage
	}
	if !isPortableMeetingOutput(outPath) {
		fmt.Fprintf(stderr, "annotate carry configuration error: --out must be a .opus file, got %s\n", outPath)
		return annotateExitUsage
	}
	// The sealed file is the pipeline's artifact, checked against the seal
	// digest before it leaves. Writing over it would turn that check into a
	// check of carry's own output.
	if same, err := sameFilePath(sealed, outPath); err != nil {
		fmt.Fprintf(stderr, "annotate carry configuration error: %v\n", err)
		return annotateExitUsage
	} else if same {
		fmt.Fprintln(stderr, "annotate carry configuration error: --out must differ from the sealed file; carry never modifies it")
		return annotateExitUsage
	}

	result, err := annotateCarry(ctx, delivered, sealed, outPath)
	return finishAnnotate(fs.Name(), result, err, emitJSON, stdout, stderr, func(w io.Writer) {
		if result.Carried == 0 && result.Annotations == nil {
			fmt.Fprintf(w, "nothing to carry -> %s (a copy of the sealed file)\n", outPath)
		} else {
			fmt.Fprintf(w, "carried %d mark(s) -> %s\n", result.Carried, outPath)
		}
		printAnnotateDocumentSummary(w, result)
	})
}

// annotateCarry writes the delivered copy's marks into a copy of the sealed
// file.
//
// The document moves as it is — its revision, namespace and binding included.
// Carry is not a batch: nobody made a new mark, and a client that read revision
// 4 before the rerun can still write with --expect-revision 4 after it. When
// the rerun changed the audio, the binding still names the audio the marks
// were made for, so they arrive unresolved rather than silently re-pinned to a
// timeline they were never drawn on.
//
// When the sealed file already carries annotations they are replaced: nothing
// produces them at publish yet, and merging is D-743's to define.
func annotateCarry(ctx context.Context, deliveredPath, sealedPath, outPath string) (annotateResult, error) {
	delivered, err := readAnnotateSource(deliveredPath)
	if err != nil {
		return annotateResult{}, fmt.Errorf("read the delivered copy: %w", err)
	}
	if delivered.unsupported != nil {
		// Dropping them would be the very loss carry exists to prevent.
		return annotateResult{}, fmt.Errorf("the delivered copy: %v; refusing to publish over marks this build cannot carry", delivered.unsupported)
	}
	sealed, err := readAnnotateSource(sealedPath)
	if err != nil {
		return annotateResult{}, fmt.Errorf("read the sealed file: %w", err)
	}
	digest := sealed.audioDigest()
	result := newAnnotateResult(digest)

	if delivered.doc == nil {
		resolvedOut, err := preparePortableMeetingOutput(outPath)
		if err != nil {
			return annotateResult{}, err
		}
		if err := copyPortableMeetingFile(sealed.path, resolvedOut); err != nil {
			return annotateResult{}, err
		}
		describeAnnotateSource(&result, sealed)
		if result.ContainerSHA256, err = annotateFileSHA256(resolvedOut); err != nil {
			return annotateResult{}, err
		}
		return result, nil
	}

	doc := cloneAnnotations(delivered.doc)
	doc.Canonicalize()
	bound := annotationBoundDuration(doc, sealed, delivered)
	if err := portable.ValidateAnnotations(doc, bound); err != nil {
		return annotateResult{}, &annotateFailure{
			code: annotateExitInvalid,
			err:  fmt.Errorf("the delivered copy's annotations do not validate: %w", err),
		}
	}
	resolvedOut, err := preparePortableMeetingOutput(outPath)
	if err != nil {
		return annotateResult{}, err
	}
	written, err := writeAnnotateDocument(ctx, sealed, resolvedOut, doc, bound)
	if err != nil {
		return annotateResult{}, err
	}
	resolved := doc.Resolved(digest)
	result.Annotations = written
	result.Revision = doc.Revision
	result.Carried = len(doc.Items)
	result.Resolved = &resolved
	if result.ContainerSHA256, err = annotateFileSHA256(resolvedOut); err != nil {
		return annotateResult{}, err
	}
	return result, nil
}

// annotationBoundDuration is the duration of the audio a document's time
// ranges were made against — the bound their endMs must respect. When none of
// the files at hand carries that audio its duration is not known here, so the
// bound cannot be checked; the document's structure still is.
func annotationBoundDuration(doc *portable.Annotations, files ...annotateSource) int64 {
	for _, file := range files {
		if doc.Resolved(file.audioDigest()) {
			return file.manifest.Audio.DurationMS
		}
	}
	return math.MaxInt64
}

// ---------------------------------------------------------------- reading

// annotateSource is one portable .opus as annotate reads it: the tags, the
// payload twice over (the raw JSON for the generic edit, the struct for the
// named fields), and the annotations it carries.
type annotateSource struct {
	path     string
	tags     map[string]string
	rawJSON  []byte
	manifest portable.Manifest
	// doc is nil when the file carries no annotations — or carries them in a
	// format this build does not know, which unsupported then says.
	doc         *portable.Annotations
	unsupported error
}

func (s annotateSource) audioDigest() string { return s.manifest.Integrity.OpusSHA256 }

func readAnnotateSource(path string) (annotateSource, error) {
	tags, err := portableMeetingTags(path)
	if err != nil {
		return annotateSource{}, err
	}
	rawJSON, err := decodePortableMeetingPayload(tags)
	if err != nil {
		return annotateSource{}, err
	}
	manifest, err := portable.DecodePublishedManifest(rawJSON)
	if err != nil {
		return annotateSource{}, err
	}
	// The same cross-check readPortableMeetingManifest makes. The audio digest
	// is the recording's identity and the binding of every mark; a file whose
	// tag and manifest disagree about it cannot say which audio it is.
	if tagged := portableTagValue(tags, "CASSINI_AUDIO_OPUS_SHA256"); tagged != manifest.Integrity.OpusSHA256 {
		return annotateSource{}, fmt.Errorf("portable Opus audio digest disagrees between tags and manifest: tag=%s manifest=%s",
			tagged, manifest.Integrity.OpusSHA256)
	}
	source := annotateSource{path: path, tags: tags, rawJSON: rawJSON, manifest: manifest}
	doc, err := portable.ParseAnnotations(manifest.Annotations)
	switch {
	case errors.Is(err, portable.ErrAnnotationsFormatUnsupported):
		source.unsupported = err
	case err != nil:
		return annotateSource{}, fmt.Errorf("the file carries annotations that cannot be read: %w", err)
	default:
		source.doc = doc
	}
	return source, nil
}

// describeAnnotateSource fills in what a file carries, for a file that was not
// rewritten: show, a batch that changed nothing, a carry with nothing to carry.
func describeAnnotateSource(result *annotateResult, source annotateSource) {
	if source.doc == nil {
		return
	}
	result.Annotations = append(json.RawMessage(nil), source.manifest.Annotations...)
	result.Revision = source.doc.Revision
	resolved := source.doc.Resolved(source.audioDigest())
	result.Resolved = &resolved
}

// ---------------------------------------------------------------- writing

// writeAnnotateDocument writes doc as the annotations of a copy of source at
// resolvedOut, through retag's stage-verify-rename commit, and answers the
// annotations member as read back out of the written file.
//
// The verification is the design's four checks, run against the STAGED file
// before it replaces anything:
//
//  1. the audio digest is the source's (commitPortableManifestRewrite has just
//     recomputed it from the staged audio);
//  2. the manifest minus annotations is the source's, compared as generic JSON
//     so fields no Go struct models are covered too;
//  3. the annotations are exactly the intended document;
//  4. they validate — against boundDurationMS, the duration of the audio the
//     marks were made against.
//
// And one more, because a manifest is not the whole file: every tag outside the
// main payload is the source's. The transcript bodies are chunk sets of their
// own, which the payload read-back never looks at.
func writeAnnotateDocument(ctx context.Context, source annotateSource, resolvedOut string, doc *portable.Annotations, boundDurationMS int64) (json.RawMessage, error) {
	intendedJSON, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode annotations: %w", err)
	}
	intended, err := decodeAnnotateGenericJSON(intendedJSON)
	if err != nil {
		return nil, err
	}
	document, err := decodePortableMeetingDocument(source.rawJSON)
	if err != nil {
		return nil, err
	}
	document["annotations"] = json.RawMessage(intendedJSON)
	// A second, untouched decode of the source — the edit above mutates the
	// first — to compare the written manifest with.
	pristine, err := decodePortableMeetingDocument(source.rawJSON)
	if err != nil {
		return nil, err
	}
	delete(pristine, "annotations")

	var writtenAnnotations json.RawMessage
	verify := func(_, written portable.Manifest, writtenTags map[string]string) error {
		if written.Integrity.OpusSHA256 != source.audioDigest() {
			return fmt.Errorf("verify annotated file: audio digest %s is not the source's %s", written.Integrity.OpusSHA256, source.audioDigest())
		}
		writtenRaw, err := decodePortableMeetingPayload(writtenTags)
		if err != nil {
			return fmt.Errorf("verify annotated file: %w", err)
		}
		writtenDocument, err := decodePortableMeetingDocument(writtenRaw)
		if err != nil {
			return fmt.Errorf("verify annotated file: %w", err)
		}
		writtenMember, ok := writtenDocument["annotations"]
		if !ok {
			return errors.New("verify annotated file: the written manifest carries no annotations")
		}
		delete(writtenDocument, "annotations")
		if !reflect.DeepEqual(writtenDocument, pristine) {
			return errors.New("verify annotated file: the manifest changed outside annotations")
		}
		if !reflect.DeepEqual(writtenMember, intended) {
			return errors.New("verify annotated file: the written annotations are not the intended document")
		}
		parsed, err := portable.ParseAnnotations(written.Annotations)
		if err != nil || parsed == nil {
			return fmt.Errorf("verify annotated file: the written annotations do not parse: %v", err)
		}
		if err := portable.ValidateAnnotations(parsed, boundDurationMS); err != nil {
			return fmt.Errorf("verify annotated file: %w", err)
		}
		if err := verifyAnnotateTagsPreserved(source.tags, writtenTags); err != nil {
			return err
		}
		writtenAnnotations = append(json.RawMessage(nil), written.Annotations...)
		return nil
	}
	if err := commitPortableManifestRewrite(ctx, source.path, resolvedOut, document, source.tags, verify); err != nil {
		return nil, err
	}
	return writtenAnnotations, nil
}

// annotateTagMayMove names the tags a manifest rewrite legitimately changes:
// the main payload's chunk set and its four counters, and the plain mirrors
// retagOpusTags re-derives from the (unchanged) manifest. `encoder` is
// ffmpeg's own stamp, which a different ffmpeg build rewrites.
func annotateTagMayMove(key string) bool {
	if isPayloadChunkTag(key) {
		return true
	}
	switch strings.ToUpper(key) {
	case "CASSINI_PAYLOAD_CHUNK_COUNT", "CASSINI_PAYLOAD_SHA256", "CASSINI_PAYLOAD_RAW_BYTES", "CASSINI_PAYLOAD_GZIP_BYTES",
		"CASSINI_ROOM_ID", "CASSINI_JOB_ID", "CASSINI_ATTEMPT_NUMBER",
		"ENCODER":
		return true
	}
	return false
}

// verifyAnnotateTagsPreserved checks that every tag outside the main payload
// survived the rewrite with its value, and that no CASSINI_* tag appeared from
// nowhere. ffmpeg runs with -map_metadata -1, so a tag not carried forward is
// deleted — and the per-transcript chunk sets are tags.
func verifyAnnotateTagsPreserved(before, after map[string]string) error {
	for key, value := range before {
		if annotateTagMayMove(key) {
			continue
		}
		if got := portableTagValue(after, key); got != strings.TrimSpace(value) {
			return fmt.Errorf("verify annotated file: tag %s did not survive the rewrite unchanged", key)
		}
	}
	for key := range after {
		if !strings.HasPrefix(strings.ToUpper(key), "CASSINI_") || annotateTagMayMove(key) {
			continue
		}
		if portableTagValue(before, key) == "" {
			return fmt.Errorf("verify annotated file: tag %s appeared in the rewrite", key)
		}
	}
	return nil
}

// copyPortableMeetingFile places a byte-for-byte copy of src at resolvedOut,
// through the same stage-and-rename commit the rewrites use, so a reader of
// resolvedOut sees the old file or the whole copy and never a partial one. The
// staged copy is re-hashed before the rename: it is about to be uploaded over a
// recording that, under D-612, cannot be deleted.
func copyPortableMeetingFile(src, resolvedOut string) error {
	stagePath, err := createPortableStagePath(resolvedOut)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(stagePath) }()

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("copy portable meeting file: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(stagePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("copy portable meeting file: %w", err)
	}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, hash), in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy portable meeting file: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy portable meeting file: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("copy portable meeting file: %w", err)
	}
	staged, err := annotateFileSHA256(stagePath)
	if err != nil {
		return err
	}
	if want := hex.EncodeToString(hash.Sum(nil)); staged != want {
		return fmt.Errorf("copy portable meeting file: the staged copy hashes to %s, the source to %s", staged, want)
	}
	return commitPortableMeetingOutput(stagePath, resolvedOut)
}

// annotateFileSHA256 is the container digest: the sha256 of the file as it is
// on disk.
func annotateFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// decodeAnnotateGenericJSON decodes JSON the way decodePortableMeetingDocument
// does — numbers kept as their literal text — so two decodes compare equal
// exactly when the documents do.
func decodeAnnotateGenericJSON(raw []byte) (any, error) {
	document, err := decodePortableMeetingDocument(raw)
	if err != nil {
		return nil, err
	}
	return document, nil
}

// newAnnotationTagNamespace mints a urn:uuid namespace from a random (version
// 4) uuid. A namespace is minted only when neither the file nor the operator
// supplies one — a CLI user annotating a file by hand.
func newAnnotationTagNamespace() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("mint tag namespace: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// printAnnotateDocumentSummary is the human half of every subcommand.
func printAnnotateDocumentSummary(w io.Writer, result annotateResult) {
	doc, err := portable.ParseAnnotations(result.Annotations)
	if err != nil || doc == nil {
		fmt.Fprintln(w, "  no annotations")
		return
	}
	state := "resolved"
	if result.Resolved != nil && !*result.Resolved {
		state = "UNRESOLVED: made against different audio; do not draw them against this recording"
	}
	fmt.Fprintf(w, "  revision %d, %d tag(s), %d mark(s), %s\n", doc.Revision, len(doc.Tags), len(doc.Items), state)
	counts := map[string]int{}
	for _, item := range doc.Items {
		counts[item.TagID]++
	}
	for _, tag := range doc.Tags {
		fmt.Fprintf(w, "    %s (%s): %d mark(s)\n", tag.Label, tag.ID, counts[tag.ID])
	}
}
