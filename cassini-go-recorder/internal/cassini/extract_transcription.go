package cassini

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"gocassini/internal/portable"
)

// Extraction is explicit about immutable policy evidence. It never changes input.
func runExtractTranscription(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "transcription" {
		fmt.Fprintln(stderr, "usage: cassini extract transcription --out FILE --age-anchor RFC3339 INPUT")
		return 2
	}
	fs := flag.NewFlagSet("extract transcription", flag.ContinueOnError)
	fs.SetOutput(stderr)
	refreshFrom := fs.String("refresh-from", "", "existing retained document for a same-audio publication")
	annotationsFile := fs.String("annotations-file", "", "captured desired annotations JSON")
	out := fs.String("out", "", "new retained document path")
	anchor := fs.String("age-anchor", "", "immutable original recording timestamp (RFC3339)")
	source := fs.String("anchor-source", "recording-completed", "original timestamp provenance")
	documentID := fs.String("document-id", "", "stable document ID; generated if absent")
	token := fs.String("state-token", "", "captured annotation checkpoint token")
	revision := fs.Int64("annotation-revision", 0, "captured annotation revision")
	policy := fs.Int64("policy-revision", 0, "retention policy revision")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *out == "" {
		fs.Usage()
		return 2
	}
	date, err := time.Parse(time.RFC3339Nano, *anchor)
	if err != nil {
		fmt.Fprintln(stderr, "valid --age-anchor required")
		return 2
	}
	input, err := os.Open(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer input.Close()
	raw, report, err := portable.ExportTranscription(input, portable.TranscriptionOptions{DocumentID: *documentID, AgeAnchor: date, AnchorSource: *source, EvictedAt: time.Now(), PolicyRevision: *policy, Checkpoint: portable.AnnotationCheckpoint{StateToken: *token, Revision: *revision}})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *annotationsFile != "" {
		annotations, readErr := os.ReadFile(*annotationsFile)
		if readErr != nil {
			fmt.Fprintln(stderr, readErr)
			return 1
		}
		raw, err = portable.RewriteTranscriptionAnnotations(raw, annotations, portable.AnnotationCheckpoint{StateToken: *token, Revision: *revision})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if *refreshFrom != "" {
		existing, readErr := os.ReadFile(*refreshFrom)
		if readErr != nil {
			fmt.Fprintln(stderr, readErr)
			return 1
		}
		raw, err = portable.RefreshTranscription(existing, raw)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	file, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ok := false
	defer func() {
		file.Close()
		if !ok {
			os.Remove(*out)
		}
	}()
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	if err == nil {
		err = file.Close()
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ok = true
	if err = json.NewEncoder(stdout).Encode(report); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
