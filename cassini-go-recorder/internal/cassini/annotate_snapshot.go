package cassini

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"gocassini/internal/portable"
	"io"
	"math"
)

// snapshot embeds a previously validated complete document without regenerating
// IDs, actor stamps or revisions. The caller owns serialization and If-Match.
func runAnnotateSnapshot(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("annotate snapshot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "output recording")
	_ = fs.Bool("json", false, "JSON result")
	if err := fs.Parse(args); err != nil {
		return annotateExitUsage
	}
	if fs.NArg() != 1 || *out == "" {
		fmt.Fprintln(stderr, "snapshot --out output.opus --json input.opus < document.json")
		return annotateExitUsage
	}
	source, err := readAnnotateSource(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return annotateExitRuntime
	}
	if source.unsupported != nil {
		fmt.Fprintln(stderr, source.unsupported)
		return annotateExitInvalid
	}
	var doc portable.Annotations
	// Full snapshots can exceed the mutation request limit. Apply the shared
	// document validator below so any accepted document can also be embedded.
	decoder := json.NewDecoder(stdin)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&doc); err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = fmt.Errorf("trailing document content")
		}
	}
	duration := source.manifest.Audio.DurationMS
	if !doc.Resolved(source.audioDigest()) {
		duration = math.MaxInt64
	}
	if err == nil {
		err = portable.ValidateAnnotations(&doc, duration)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return annotateExitInvalid
	}
	resolvedOut, err := preparePortableMeetingOutput(*out)
	if err == nil {
		_, err = writeAnnotateDocument(ctx, source, resolvedOut, &doc)
	}
	var result annotateResult
	if err == nil {
		result, err = annotateShow(resolvedOut, stderr)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return annotateExitRuntime
	}
	if err = json.NewEncoder(stdout).Encode(result); err != nil {
		return annotateExitRuntime
	}
	return 0
}
