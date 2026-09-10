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
	"slices"
	"strings"
	"time"

	"gocassini/internal/portable"
)

// `cassini annotate` reads and writes the tags and marks a recording carries in
// its own manifest (manifest.annotations), on retag's stage-verify-rename path:
// the audio is copied, never re-encoded; nothing outside `annotations` changes;
// a batch is one write, and a batch that changes nothing writes nothing.
//
// The operator runs this binary rather than importing it, so the result
// document and the exit codes are a contract with
// cassini-operator/internal/operator/annotate_cli.go. Change both sides together.

const annotateResultFormat = "cassini.annotate.result.v1"

const (
	annotateExitRuntime    = 1
	annotateExitUsage      = 2
	annotateExitRevision   = 3 // --expect-revision did not match
	annotateExitInvalid    = 4 // invalid ops, or a document that would not validate
	annotateExitUnresolved = 5 // a mark on marks bound to different audio
)

// annotateResult is what every subcommand prints with --json. Every member is
// always present — lists empty, `resolved` null — so "none" never reads as
// "not reported".
type annotateResult struct {
	Format string `json:"format"`
	// Annotations is the document as read back out of the file; null when none.
	Annotations json.RawMessage `json:"annotations"`
	Revision    int             `json:"revision"`
	// OperationID is stamped on the marks apply adds; empty for show and carry.
	OperationID string `json:"operationId"`
	// Added and Removed are net: an item added and removed in one batch is in neither.
	Added    []string `json:"added"`
	Removed  []string `json:"removed"`
	NotFound []string `json:"notFound"`
	Carried  int      `json:"carried"`
	// Resolved is null when there are no annotations to judge.
	Resolved *bool `json:"resolved"`
	// AudioOpusSHA256 is the file's audio digest: the recording's identity.
	AudioOpusSHA256 string `json:"audioOpusSha256"`
	// ContainerSHA256 is the file as it now is on disk; it moves with every write.
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

// annotateFailure carries the exit code a failure maps to; any other error is
// a runtime failure.
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

// runAnnotate dispatches the subcommands. The operator sends the ops on stdin
// (`--ops -`) so a request body never touches the disk.
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
ops or a document that would not validate, 5 a mark on marks bound to different
audio (they can still be removed or relabelled). Nothing is written unless the
exit is 0.
`)
}

// parseAnnotateArgs parses flags and file arguments in any order and checks the
// number of files. It answers the exit code to stop with, or -1 to go on.
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
		// After a bare "--" everything is a file, even if it looks like a flag.
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
// For exit 4 the operator shows stderr to the caller, so those messages
// describe the ops and never name a local path.
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
		// A reader ignores a format it does not know; the recording is fine.
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
	// A blank id is a caller that failed to pass one, not an anonymous mark.
	if strings.TrimSpace(actorID) == "" {
		return usage("--actor-id is required")
	}
	if tagNamespace != "" && !portable.IsAnnotationTagNamespace(tagNamespace) {
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

	// The actor kind and operation id come from the request on the operator
	// path, so a bad one is the caller's invalid input (exit 4), not a usage error.
	invalid := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "%s: "+format+"\n", append([]any{fs.Name()}, args...)...)
		return annotateExitInvalid
	}
	if actorKind != portable.AnnotationActorPerson && actorKind != portable.AnnotationActorAgent {
		return invalid("--actor-kind must be %s or %s, got %q", portable.AnnotationActorPerson, portable.AnnotationActorAgent, actorKind)
	}
	if operationID != "" && !portable.IsAnnotationID(operationID) {
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
			CreatedAt:   time.Now().UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z"),
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

// annotateApply applies one batch and writes it once, answering whether the
// file was rewritten. A batch that changes nothing gets no new revision and no
// rewrite — only a copy when --out names another file. Every refusal happens
// before the output is touched.
func annotateApply(ctx context.Context, req annotateApplyRequest) (annotateResult, bool, error) {
	source, err := readAnnotateSource(req.inputPath)
	if err != nil {
		return annotateResult{}, false, err
	}
	if source.unsupported != nil {
		// A reader may ignore marks it cannot read; a writer must not replace them.
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
	// Marks bound to other audio can be removed or relabelled, but a new mark
	// would put two timelines under one binding. Their ranges mean nothing
	// against this audio, so they are checked for structure only.
	durationMS := source.manifest.Audio.DurationMS
	unresolved := source.doc != nil && len(source.doc.Items) > 0 && !source.doc.Resolved(digest)
	if unresolved {
		if slices.ContainsFunc(req.ops, func(op annotateOp) bool { return op.Op == annotateOpMark }) {
			return annotateResult{}, false, annotateFail(annotateExitUnresolved,
				"the file's marks were made against different audio (bound to %s, this audio is %s); they can be removed or relabelled, but no mark can be added until they are migrated, and nothing was written",
				source.doc.AudioOpusSHA256, digest)
		}
		durationMS = math.MaxInt64
	}
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
	// The binding and namespace are the file's, never the request's. Only marks
	// that survive from other audio keep another binding.
	doc.AudioOpusSHA256 = digest
	doc.TagNamespace = req.tagNamespace
	if source.doc != nil {
		doc.TagNamespace = source.doc.TagNamespace
		if unresolved && len(doc.Items) > 0 {
			doc.AudioOpusSHA256 = source.doc.AudioOpusSHA256
		}
	} else if doc.TagNamespace == "" {
		if doc.TagNamespace, err = newAnnotationTagNamespace(); err != nil {
			return annotateResult{}, false, err
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
	written, err := writeAnnotateDocument(ctx, source, resolvedOut, doc)
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
	// The sealed file is checked against the seal digest before it leaves;
	// writing over it would turn that into a check of carry's own output.
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
// file. The document moves as it is, revision included: carry is not a batch,
// so a client that read revision 4 before a rerun can still write against it
// after. Marks keep the binding of the audio they were drawn on, so across
// changed audio they arrive unresolved rather than silently re-pinned.
// Annotations already on the sealed file are replaced.
func annotateCarry(ctx context.Context, deliveredPath, sealedPath, outPath string) (annotateResult, error) {
	delivered, err := readAnnotateSource(deliveredPath)
	if err != nil {
		return annotateResult{}, fmt.Errorf("read the delivered copy: %w", err)
	}
	if delivered.unsupported != nil {
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
	// A document with no marks has nothing to be wrong about.
	if len(doc.Items) == 0 {
		doc.AudioOpusSHA256 = digest
	}
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
	written, err := writeAnnotateDocument(ctx, sealed, resolvedOut, doc)
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

// annotationBoundDuration is the duration of the audio a document's ranges were
// drawn on. When no file at hand carries that audio the bound is unknown, and
// only the document's structure can be checked.
func annotationBoundDuration(doc *portable.Annotations, files ...annotateSource) int64 {
	for _, file := range files {
		if doc.Resolved(file.audioDigest()) {
			return file.manifest.Audio.DurationMS
		}
	}
	return math.MaxInt64
}

// ---------------------------------------------------------------- reading

type annotateSource struct {
	path     string
	tags     map[string]string
	manifest portable.Manifest
	// doc is nil when the file carries no annotations, or carries them in a
	// format this build does not know, which unsupported then says.
	doc         *portable.Annotations
	unsupported error
}

func (s annotateSource) audioDigest() string { return s.manifest.Integrity.OpusSHA256 }

func readAnnotateSource(path string) (annotateSource, error) {
	manifest, tags, err := readPortableMeetingManifest(path)
	if err != nil {
		return annotateSource{}, err
	}
	source := annotateSource{path: path, tags: tags, manifest: manifest}
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

// describeAnnotateSource fills in what a file that was not rewritten carries.
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
// resolvedOut, and answers the member as read back out of the written file.
// commitPortableManifestRewrite verifies the staged audio; this verifies, still
// on the staged file, that the manifest outside annotations is the source's
// (compared as generic JSON, so fields no struct models are covered), that the
// annotations are exactly doc, and that every tag outside the main payload
// survived — the transcripts are chunk sets of their own, which the payload
// read-back never looks at.
func writeAnnotateDocument(ctx context.Context, source annotateSource, resolvedOut string, doc *portable.Annotations) (json.RawMessage, error) {
	intendedJSON, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode annotations: %w", err)
	}
	intended, err := decodePortableMeetingDocument(intendedJSON)
	if err != nil {
		return nil, err
	}
	rawJSON, err := decodePortableMeetingPayload(source.tags)
	if err != nil {
		return nil, err
	}
	document, err := decodePortableMeetingDocument(rawJSON)
	if err != nil {
		return nil, err
	}
	document["annotations"] = json.RawMessage(intendedJSON)
	// A second decode to compare against: the edit above mutates the first.
	pristine, err := decodePortableMeetingDocument(rawJSON)
	if err != nil {
		return nil, err
	}
	delete(pristine, "annotations")

	var writtenAnnotations json.RawMessage
	verify := func(_, written portable.Manifest, writtenTags map[string]string) error {
		writtenRaw, err := decodePortableMeetingPayload(writtenTags)
		if err != nil {
			return fmt.Errorf("verify annotated file: %w", err)
		}
		writtenDocument, err := decodePortableMeetingDocument(writtenRaw)
		if err != nil {
			return fmt.Errorf("verify annotated file: %w", err)
		}
		writtenMember := writtenDocument["annotations"]
		delete(writtenDocument, "annotations")
		if !reflect.DeepEqual(writtenDocument, pristine) {
			return errors.New("verify annotated file: the manifest changed outside annotations")
		}
		if !reflect.DeepEqual(writtenMember, any(intended)) {
			return errors.New("verify annotated file: the written annotations are not the intended document")
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
// the main payload's chunk set and counters, the plain mirrors retagOpusTags
// re-derives, and ffmpeg's own `encoder` stamp.
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
// survived the rewrite unchanged and that no CASSINI_* tag appeared. ffmpeg runs
// with -map_metadata -1, so a tag not carried forward is deleted — and the
// per-transcript chunk sets are tags.
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

// copyPortableMeetingFile places a copy of src at resolvedOut through a stage
// file and a rename, so a reader never sees a partial copy.
func copyPortableMeetingFile(src, resolvedOut string) error {
	stagePath, err := createPortableStagePath(resolvedOut)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(stagePath) }()
	if err := copyFile(src, stagePath, 0o644); err != nil {
		return fmt.Errorf("copy portable meeting file: %w", err)
	}
	return commitPortableMeetingOutput(stagePath, resolvedOut)
}

// annotateFileSHA256 is the container digest: the sha256 of the file on disk.
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

// newAnnotationTagNamespace mints a random (version 4) urn:uuid, for a file's
// first write when neither the file nor the operator supplies a namespace.
func newAnnotationTagNamespace() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("mint tag namespace: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

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
