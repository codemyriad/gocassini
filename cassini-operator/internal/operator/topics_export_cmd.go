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

// `cassini-operator topics-export`: every topic verdict with its scores, for
// offline analysis by someone with shell access on the server. No route
// serves these numbers (docs/proposals/automatic-tagging, "Why no suggestion
// or verdict counts on ADMIN routes"). Rows carry ids and numbers, never a
// transcript, a label or a definition, and nothing from a retired meeting.
const topicsExportCommand = "topics-export"

const topicsExportFormat = "cassini.topic-verdicts.v1"

type topicsExportHeader struct {
	Format          string  `json:"format"`
	ScorerVersion   int     `json:"scorerVersion"`
	ScorerKey       string  `json:"scorerKey"`
	Threshold       float64 `json:"threshold"`
	Margin          float64 `json:"margin"`
	DetectorVersion string  `json:"detectorVersion"`
	Rows            int     `json:"rows"`
}

type topicsExportRow struct {
	Meeting            string   `json:"meeting"`
	Topic              string   `json:"topic"`
	Verdict            string   `json:"verdict"`
	Source             string   `json:"source"`
	At                 int64    `json:"at"`
	DefinitionSHA      string   `json:"definitionSha"`
	ScorerKey          string   `json:"scorerKey"`
	Rule               string   `json:"rule,omitempty"`
	SupportWords       *int64   `json:"supportWords,omitempty"`
	Peak               *float64 `json:"peak,omitempty"`
	MinMargin          *float64 `json:"minMargin,omitempty"`
	Language           string   `json:"language,omitempty"`
	LanguageConfidence *float64 `json:"languageConfidence,omitempty"`
	DetectorVersion    string   `json:"detectorVersion,omitempty"`
	// The topic's fitted gate, when it has one on this definition, and the
	// model it was fitted under: it applies only while that model is current.
	Gate              *[2]float64 `json:"gate,omitempty"`
	GateModelRevision string      `json:"gateModelRevision,omitempty"`
}

func runTopicsExport(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini-operator "+topicsExportCommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "write here instead of stdout")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Write every topic verdict with its scores as JSON lines.

A header line, then one line per (meeting, topic) verdict: the verdict, who
or what made it (review, direct), the definition and scorer it was made
against, the suggestion's scores and the meeting's detected language. No
transcript text, labels or definitions; nothing from retired meetings.

Usage:
  cassini-operator `+topicsExportCommand+` [--out FILE]

Flags:
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
		fmt.Fprintf(stderr, "%s: unexpected arguments\n", topicsExportCommand)
		return 2
	}
	cfg, code, err := loadConfig(nil, stderr)
	if err != nil || code != 0 {
		if err != nil {
			fmt.Fprintf(stderr, "operator config: %v\n", err)
		}
		return 3
	}
	jobs, err := OpenStore(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(stderr, "open job database: %v\n", err)
		return 3
	}
	defer jobs.Close()
	annotations, err := openAnnotationStore(sidecarPath(cfg.DBPath, annotationsStoreFilename), nil)
	if err != nil {
		fmt.Fprintf(stderr, "open annotations database: %v\n", err)
		return 3
	}
	defer annotations.Close()
	store := newTopicsStore(annotations)
	store.lifecycle = jobs
	w := stdout
	if *out != "" {
		f, err := os.OpenFile(*out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", topicsExportCommand, err)
			return 1
		}
		defer f.Close()
		w = f
	}
	n, err := exportTopicVerdicts(ctx, store, w)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", topicsExportCommand, err)
		return 1
	}
	if *out != "" {
		fmt.Fprintf(stdout, "%d verdict(s) written to %s\n", n, *out)
	}
	return 0
}

// exportTopicVerdicts writes the header and the rows. Rows are read first,
// then each meeting is checked against the lifecycle, so no cursor is open
// while another query runs.
func exportTopicVerdicts(ctx context.Context, store *topicsStore, w io.Writer) (int, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT v.opus_name,v.tag_id,v.verdict,v.source,v.at,COALESCE(v.definition_sha,''),COALESCE(v.scorer_key,''),
 COALESCE(v.rule,''),v.support_words,v.peak,v.min_margin,COALESCE(m.language,''),m.language_confidence,COALESCE(m.detector_version,'')
 FROM topic_verdict v LEFT JOIN topic_meeting m ON m.opus_name=v.opus_name ORDER BY v.opus_name,v.tag_id`)
	if err != nil {
		return 0, err
	}
	var all []topicsExportRow
	for rows.Next() {
		var r topicsExportRow
		if err := rows.Scan(&r.Meeting, &r.Topic, &r.Verdict, &r.Source, &r.At, &r.DefinitionSHA, &r.ScorerKey,
			&r.Rule, &r.SupportWords, &r.Peak, &r.MinMargin, &r.Language, &r.LanguageConfidence, &r.DetectorVersion); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, r)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	gates, err := topicGates(ctx, store.db)
	if err != nil {
		return 0, err
	}
	var kept []topicsExportRow
	for _, r := range all {
		if err := store.notRetired(ctx, r.Meeting); err != nil {
			if errors.Is(err, errMeetingRetired) {
				continue
			}
			return 0, err
		}
		// The gate scoring uses: fitted on this definition and against the
		// current global scorer. Its vector space is in GateModelRevision.
		if g, ok := gates[r.Topic]; ok && g.DefinitionSHA == r.DefinitionSHA && g.GlobalKey == topicsScorerKey() && g.fitted() {
			r.GateModelRevision = g.ModelRevision
			r.Gate = &[2]float64{g.Threshold, g.Margin}
		}
		kept = append(kept, r)
	}
	enc := json.NewEncoder(w)
	if err := enc.Encode(topicsExportHeader{Format: topicsExportFormat, ScorerVersion: topicsScorerVersion, ScorerKey: topicsScorerKey(),
		Threshold: topicScorerV1.Threshold, Margin: topicScorerV1.Margin, DetectorVersion: topicsExpectedDetectorVersion, Rows: len(kept)}); err != nil {
		return 0, err
	}
	for _, r := range kept {
		if err := enc.Encode(r); err != nil {
			return 0, err
		}
	}
	return len(kept), nil
}
