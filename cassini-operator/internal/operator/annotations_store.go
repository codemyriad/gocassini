package operator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// The tag index (D-737): annotations.sqlite3, a disposable copy of the marks
// inside each delivered .opus — a sidecar for search.sqlite3's reasons, and a
// separate file so a schema bump here never forces a transcript re-index there.
//
// The recording is the record: every write commits the file first and records
// here second, so a failure here costs coverage, never a mark. As in search,
// nothing here knows who may read what; every read takes the caller's visible
// set as an argument and binds it into the statement.
const (
	annotationsStoreFilename = "annotations.sqlite3"

	// annotationsSchemaVersion: bump for any change to the schema or to what
	// ingest keeps from a document. A mismatched file is deleted and rebuilt.
	annotationsSchemaVersion = 2

	annotationsStateIndexed = "indexed"
	// annotationsStateUnavailable: the meeting is known but its marks could not
	// be read, so it counts against coverage rather than as untagged.
	annotationsStateUnavailable = "unavailable"

	// The format's contract, duplicated from the CLI's portable package, which
	// the operator cannot import.
	annotationsDocFormatV1     = "cassini.annotations.v1"
	annotationsTargetMeeting   = "meeting"
	annotationsTargetTimeRange = "time-range"

	// annotationsMetaBuilt is the annotations_meta key a completed rebuild sets.
	annotationsMetaBuilt = "built"
)

// annotationsNamespaceRE is the only namespace shape offered to a writer: a
// lowercase urn:uuid, as the CLI's validation requires.
var annotationsNamespaceRE = regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// errAnnotationIndexBuilding is Namespace refusing while the first rebuild of a
// never-built index runs: against an index that does not yet know the archive,
// a label lookup would miss and mint a second id for a word already in use.
var errAnnotationIndexBuilding = errors.New("the tag index has not finished its first rebuild")

const annotationsSchemaSQL = `
CREATE TABLE IF NOT EXISTS annotations_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- References only: the caller's own catalog supplies titles, dates and rooms.
CREATE TABLE IF NOT EXISTS meeting_annotations (
  opus_name        TEXT PRIMARY KEY, -- path.Base of the delivered .opus
  state            TEXT NOT NULL,
  revision         INTEGER NOT NULL DEFAULT 0,
  -- 0 when the marks were made against other audio: they still tag the
  -- meeting, but their ranges are never cited against this audio.
  resolved         INTEGER NOT NULL DEFAULT 1,
  namespace        TEXT NOT NULL DEFAULT '',
  -- The bytes these rows came from, so a rebuild skips an unchanged file.
  container_sha256 TEXT NOT NULL DEFAULT ''
);

-- Per meeting, because relabel is per file: one id can carry several labels.
CREATE TABLE IF NOT EXISTS annotation_tag (
  opus_name    TEXT NOT NULL REFERENCES meeting_annotations(opus_name) ON DELETE CASCADE,
  tag_id       TEXT NOT NULL,
  label        TEXT NOT NULL,
  -- foldTagLabel(label): SQLite's NOCASE folds ASCII only.
  label_folded TEXT NOT NULL,
  PRIMARY KEY (opus_name, tag_id)
);

CREATE TABLE IF NOT EXISTS annotation_item (
  opus_name TEXT NOT NULL REFERENCES meeting_annotations(opus_name) ON DELETE CASCADE,
  tag_id    TEXT NOT NULL,
  kind      TEXT NOT NULL,
  start_ms  INTEGER,
  end_ms    INTEGER
);

CREATE INDEX IF NOT EXISTS annotation_item_by_meeting ON annotation_item(opus_name, tag_id);
CREATE INDEX IF NOT EXISTS annotation_tag_by_label ON annotation_tag(label_folded);
`

// annotationStore implements annotationIndex for the write paths, and serves
// the vocabulary, tag narrowing and the marks a search hit cites.
type annotationStore struct {
	sidecarDB
	// rebuildPending is set while the first rebuild of a never-built index runs.
	rebuildPending atomic.Bool
}

var _ annotationIndex = (*annotationStore)(nil)

// annotationReads is the concrete store behind rt.annotations, or nil — the
// read routes then answer 503, never an empty result.
func (rt *Runtime) annotationReads() *annotationStore {
	if rt == nil {
		return nil
	}
	store, _ := rt.annotations.(*annotationStore)
	return store
}

func openAnnotationStore(path string, logger *log.Logger) (*annotationStore, error) {
	db, err := openSidecarDB(path, "annotations index", annotationsSchemaSQL, annotationsSchemaVersion, logger)
	if err != nil {
		return nil, err
	}
	return &annotationStore{sidecarDB: db}, nil
}

// foldTagLabel is what "the same label" means everywhere in the operator:
// trimmed, then lower-cased by Go, which folds Unicode as well as ASCII.
func foldTagLabel(label string) string {
	return strings.ToLower(strings.TrimSpace(label))
}

// --- ingest -----------------------------------------------------------------

// projectedDocument is the minimal v1 reader: tolerant where the writer is
// strict, because one malformed mark must cost that mark, not the meeting.
type projectedDocument struct {
	Format          string `json:"format"`
	Revision        int    `json:"revision"`
	AudioOpusSHA256 string `json:"audioOpusSha256"`
	TagNamespace    string `json:"tagNamespace"`
	Tags            []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"tags"`
	Items []struct {
		ID     string `json:"id"`
		TagID  string `json:"tagId"`
		Target struct {
			Kind    string `json:"kind"`
			StartMS *int64 `json:"startMs"`
			EndMS   *int64 `json:"endMs"`
		} `json:"target"`
	} `json:"items"`
}

type projectedTag struct{ id, label string }

type projectedItem struct {
	tagID, kind    string
	startMS, endMS sql.NullInt64
}

// projectedMeeting is what one file contributes to the index.
type projectedMeeting struct {
	unreadable bool
	revision   int
	resolved   bool
	namespace  string
	tags       []projectedTag
	items      []projectedItem
}

// projectAnnotations reads one file's document into rows. Absent or null is a
// meeting with no marks. A document in another format may carry marks this
// reader cannot see, so it is unreadable rather than a confident zero. Inside a
// v1 document, an entry it cannot interpret is skipped on its own.
func projectAnnotations(raw json.RawMessage, cliResolved *bool, fileAudio string) projectedMeeting {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return projectedMeeting{resolved: true}
	}
	var doc projectedDocument
	if err := json.Unmarshal(trimmed, &doc); err != nil || doc.Format != annotationsDocFormatV1 {
		return projectedMeeting{unreadable: true}
	}

	out := projectedMeeting{revision: doc.Revision, namespace: strings.TrimSpace(doc.TagNamespace)}
	// The CLI's verdict when it gave one; otherwise the same comparison.
	if cliResolved != nil {
		out.resolved = *cliResolved
	} else {
		audio := strings.ToLower(strings.TrimSpace(doc.AudioOpusSHA256))
		out.resolved = audio != "" && audio == strings.ToLower(strings.TrimSpace(fileAudio))
	}

	defined := make(map[string]bool, len(doc.Tags))
	for _, tag := range doc.Tags {
		id, label := strings.TrimSpace(tag.ID), strings.TrimSpace(tag.Label)
		if id == "" || label == "" || defined[id] {
			continue
		}
		defined[id] = true
		out.tags = append(out.tags, projectedTag{id: id, label: label})
	}
	seen := make(map[string]bool, len(doc.Items))
	for _, item := range doc.Items {
		id, tagID := strings.TrimSpace(item.ID), strings.TrimSpace(item.TagID)
		if id == "" || seen[id] || !defined[tagID] {
			continue
		}
		row := projectedItem{tagID: tagID, kind: item.Target.Kind}
		switch item.Target.Kind {
		case annotationsTargetMeeting:
		case annotationsTargetTimeRange:
			start, end := item.Target.StartMS, item.Target.EndMS
			if start == nil || end == nil || *start < 0 || *end <= *start {
				continue
			}
			row.startMS = sql.NullInt64{Int64: *start, Valid: true}
			row.endMS = sql.NullInt64{Int64: *end, Valid: true}
		default:
			// A kind this reader cannot say the coverage of.
			continue
		}
		seen[id] = true
		out.items = append(out.items, row)
	}
	return out
}

// Record replaces everything known about opusName with what its file now
// carries (annotationIndex). Live writes race — revision 5's slower Record must
// not replace 6's rows — so a lower revision than the recorded one is skipped.
func (s *annotationStore) Record(ctx context.Context, opusName string, result annotateResult) error {
	_, err := s.record(ctx, opusName, result, true)
	return err
}

// record is Record reporting the state the meeting ended in. The rebuild
// passes onlyIfNewer=false: it read the delivered file itself, so what it saw
// is the truth even if older.
func (s *annotationStore) record(ctx context.Context, opusName string, result annotateResult, onlyIfNewer bool) (string, error) {
	projected := projectAnnotations(result.Annotations, result.Resolved, result.AudioOpusSHA256)
	if projected.revision == 0 {
		projected.revision = result.Revision
	}
	return s.replace(ctx, opusName, projected, result.ContainerSHA256, onlyIfNewer)
}

// MarkUnavailable records opusName as unreadable (annotationIndex), with no
// container digest, so a rebuild reads the file again rather than skipping it.
func (s *annotationStore) MarkUnavailable(ctx context.Context, opusName, _ string) error {
	_, err := s.replace(ctx, opusName, projectedMeeting{unreadable: true}, "", false)
	return err
}

// replace swaps a meeting's rows in one transaction — replace, never merge, so
// a mark removed from the file leaves the index too. "" state means skipped.
func (s *annotationStore) replace(ctx context.Context, opusName string, m projectedMeeting, container string, onlyIfNewer bool) (string, error) {
	name := strings.TrimSpace(opusName)
	if name == "" {
		return "", errors.New("opus name must not be empty")
	}
	state := annotationsStateIndexed
	if m.unreadable {
		state = annotationsStateUnavailable
	}
	skipped := false
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if onlyIfNewer {
			var current int
			switch err := tx.QueryRowContext(ctx,
				`SELECT revision FROM meeting_annotations WHERE opus_name = ?`, name).Scan(&current); {
			case errors.Is(err, sql.ErrNoRows):
			case err != nil:
				return fmt.Errorf("read the recorded revision: %w", err)
			case current > m.revision:
				skipped = true
				return nil
			}
		}
		if err := deleteAnnotationRows(ctx, tx, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO meeting_annotations (opus_name, state, revision, resolved, namespace, container_sha256)
VALUES (?, ?, ?, ?, ?, ?)`, name, state, m.revision, m.resolved, m.namespace,
			strings.ToLower(strings.TrimSpace(container))); err != nil {
			return fmt.Errorf("record meeting annotations: %w", err)
		}
		for _, tag := range m.tags {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO annotation_tag (opus_name, tag_id, label, label_folded) VALUES (?, ?, ?, ?)`,
				name, tag.id, tag.label, foldTagLabel(tag.label)); err != nil {
				return fmt.Errorf("insert tag: %w", err)
			}
		}
		for _, item := range m.items {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO annotation_item (opus_name, tag_id, kind, start_ms, end_ms) VALUES (?, ?, ?, ?, ?)`,
				name, item.tagID, item.kind, item.startMS, item.endMS); err != nil {
				return fmt.Errorf("insert mark: %w", err)
			}
		}
		return nil
	})
	if err != nil || skipped {
		return "", err
	}
	return state, nil
}

// deleteAnnotationRows drops a meeting and its rows — explicitly, so
// correctness does not hang on the per-connection foreign_keys pragma.
func deleteAnnotationRows(ctx context.Context, tx *sql.Tx, opusName string) error {
	for _, stmt := range []string{
		`DELETE FROM annotation_item WHERE opus_name = ?`,
		`DELETE FROM annotation_tag WHERE opus_name = ?`,
		`DELETE FROM meeting_annotations WHERE opus_name = ?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, opusName); err != nil {
			return fmt.Errorf("drop meeting annotations: %w", err)
		}
	}
	return nil
}

// --- the installation's vocabulary -----------------------------------------

// ResolveLabel maps a label to the tag id the caller's visible meetings already
// use for it, within the archive's namespace (annotationIndex). Hidden meetings
// are never consulted: getting an id back would reveal that a meeting the
// caller cannot open carries the word. Several ids with one label — files
// tagged before the index existed — resolve to the one on the most meetings,
// then the smallest id.
func (s *annotationStore) ResolveLabel(ctx context.Context, label string, visible []string) (string, bool, error) {
	folded := foldTagLabel(label)
	if folded == "" {
		return "", false, nil
	}
	namespace, err := s.archiveNamespace(ctx)
	if err != nil {
		return "", false, err
	}
	query := `
SELECT t.tag_id
  FROM annotation_tag t
  JOIN meeting_annotations m ON m.opus_name = t.opus_name AND m.state = ?2
  JOIN json_each(?3) v ON v.value = t.opus_name
 WHERE t.label_folded = ?1`
	args := []any{folded, annotationsStateIndexed, namesJSON(visible)}
	if namespace != "" {
		query += ` AND m.namespace = ?4`
		args = append(args, namespace)
	}
	query += `
 GROUP BY t.tag_id
 ORDER BY COUNT(*) DESC, t.tag_id
 LIMIT 1`
	var tagID string
	switch err := s.db.QueryRowContext(ctx, query, args...).Scan(&tagID); {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("resolve tag label: %w", err)
	}
	return tagID, true, nil
}

// Namespace is the installation's tag namespace (annotationIndex): the one most
// indexed files carry, or "" when none does — the CLI then keeps the file's own
// or mints one. Nothing is stored, so a rebuilt index answers the same.
func (s *annotationStore) Namespace(ctx context.Context) (string, error) {
	if s.rebuildPending.Load() {
		return "", errAnnotationIndexBuilding
	}
	return s.archiveNamespace(ctx)
}

// archiveNamespace is the well-formed namespace carried by the most indexed
// files, ties to the smallest; "" when none carries one.
func (s *annotationStore) archiveNamespace(ctx context.Context) (string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT namespace FROM meeting_annotations
 WHERE state = ? AND namespace != ''
 GROUP BY namespace
 ORDER BY COUNT(*) DESC, namespace`, annotationsStateIndexed)
	if err != nil {
		return "", fmt.Errorf("read archive namespaces: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var namespace string
		if err := rows.Scan(&namespace); err != nil {
			return "", fmt.Errorf("scan archive namespace: %w", err)
		}
		if annotationsNamespaceRE.MatchString(namespace) {
			return namespace, nil
		}
	}
	return "", rows.Err()
}

// --- reads for one caller ---------------------------------------------------

// annotationCoverage is how many of the caller's visible meetings the index
// holds marks for; Indexed below Visible means a partial answer.
type annotationCoverage struct {
	Visible int `json:"visible"`
	Indexed int `json:"indexed"`
}

// tagVocabularyEntry is one tag as the caller's visible meetings use it.
type tagVocabularyEntry struct {
	TagID     string `json:"tagId"`
	Namespace string `json:"namespace"`
	// Label is the one most of those meetings give the tag, ties to the
	// smallest: relabel is per file.
	Label    string `json:"label"`
	Meetings int    `json:"meetings"`
	Marks    int    `json:"marks"`
	// The rest is tag-styles.json's, "" where it has none (withStyles).
	Color        string `json:"color"`
	Icon         string `json:"icon"`
	ChangedBy    string `json:"changedBy"`
	ChangedAtUTC string `json:"changedAtUtc"`
}

func (s *annotationStore) Coverage(ctx context.Context, visible []string) (annotationCoverage, error) {
	names := uniqueNames(visible)
	coverage := annotationCoverage{Visible: len(names)}
	if len(names) == 0 {
		return coverage, nil
	}
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM meeting_annotations m
  JOIN json_each(?1) v ON v.value = m.opus_name
 WHERE m.state = ?2`, namesJSON(names), annotationsStateIndexed).Scan(&coverage.Indexed); err != nil {
		return annotationCoverage{}, fmt.Errorf("read annotations coverage: %w", err)
	}
	return coverage, nil
}

// Vocabulary lists the tags marked on the caller's visible meetings, counted
// over those meetings only, keyed by (namespace, tag id). A tag whose every
// mark is of a kind this reader does not know has nothing to point at and is
// not listed.
func (s *annotationStore) Vocabulary(ctx context.Context, visible []string) ([]tagVocabularyEntry, error) {
	names := uniqueNames(visible)
	if len(names) == 0 {
		return []tagVocabularyEntry{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT m.namespace, t.tag_id, t.label, COUNT(*)
  FROM annotation_item i
  JOIN json_each(?1) v       ON v.value = i.opus_name
  JOIN meeting_annotations m ON m.opus_name = i.opus_name AND m.state = ?2
  JOIN annotation_tag t      ON t.opus_name = i.opus_name AND t.tag_id = i.tag_id
 GROUP BY i.opus_name, t.tag_id`, namesJSON(names), annotationsStateIndexed)
	if err != nil {
		return nil, fmt.Errorf("read tag vocabulary: %w", err)
	}
	defer rows.Close()

	type key struct{ namespace, tagID string }
	type tally struct {
		entry  tagVocabularyEntry
		labels map[string]int
	}
	tallies := map[key]*tally{}
	for rows.Next() {
		var namespace, tagID, label string
		var marks int
		if err := rows.Scan(&namespace, &tagID, &label, &marks); err != nil {
			return nil, fmt.Errorf("scan tag vocabulary: %w", err)
		}
		k := key{namespace, tagID}
		t := tallies[k]
		if t == nil {
			t = &tally{entry: tagVocabularyEntry{TagID: tagID, Namespace: namespace}, labels: map[string]int{}}
			tallies[k] = t
		}
		// One row per (meeting, tag).
		t.entry.Meetings++
		t.entry.Marks += marks
		t.labels[label]++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read tag vocabulary: %w", err)
	}

	out := make([]tagVocabularyEntry, 0, len(tallies))
	for _, t := range tallies {
		t.entry.Label = mostCommonLabel(t.labels)
		out = append(out, t.entry)
	}
	sort.Slice(out, func(i, j int) bool {
		fi, fj := foldTagLabel(out[i].Label), foldTagLabel(out[j].Label)
		if fi != fj {
			return fi < fj
		}
		if out[i].TagID != out[j].TagID {
			return out[i].TagID < out[j].TagID
		}
		return out[i].Namespace < out[j].Namespace
	})
	return out, nil
}

// meetingTagMarks is how one meeting carries one tag.
type meetingTagMarks struct {
	TagID     string `json:"tagId"`
	Whole     bool   `json:"whole"`
	Stretches int    `json:"stretches"`
}

// meetingTags is, per visible meeting whose marks are resolved, the tags it
// carries: on the whole meeting, and on how many stretches.
func (s *annotationStore) meetingTags(ctx context.Context, visible []string) (map[string][]meetingTagMarks, error) {
	out := map[string][]meetingTagMarks{}
	names := uniqueNames(visible)
	if len(names) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT i.opus_name, i.tag_id, MAX(i.kind = ?3), SUM(i.kind = ?4)
  FROM annotation_item i
  JOIN json_each(?1) v       ON v.value = i.opus_name
  JOIN meeting_annotations m ON m.opus_name = i.opus_name AND m.state = ?2 AND m.resolved = 1
 GROUP BY i.opus_name, i.tag_id
 ORDER BY i.opus_name, i.tag_id`, namesJSON(names), annotationsStateIndexed, annotationsTargetMeeting, annotationsTargetTimeRange)
	if err != nil {
		return nil, fmt.Errorf("read meeting tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var entry meetingTagMarks
		if err := rows.Scan(&name, &entry.TagID, &entry.Whole, &entry.Stretches); err != nil {
			return nil, fmt.Errorf("scan meeting tags: %w", err)
		}
		out[name] = append(out[name], entry)
	}
	return out, rows.Err()
}

func mostCommonLabel(labels map[string]int) string {
	best, bestCount := "", -1
	for label, count := range labels {
		if count > bestCount || (count == bestCount && label < best) {
			best, bestCount = label, count
		}
	}
	return best
}

// taggedMeetings is the meetings in visible carrying at least one mark of tag,
// matched by id exactly or by label case-insensitively. The CROSS JOIN makes
// the visible set the outer loop, so the cost does not grow with hidden
// meetings carrying the tag, and a tag only they carry narrows to the same
// empty set as one nobody uses.
func (s *annotationStore) taggedMeetings(ctx context.Context, tag string, visible []string) (map[string]bool, error) {
	out := map[string]bool{}
	trimmed := strings.TrimSpace(tag)
	names := uniqueNames(visible)
	if trimmed == "" || len(names) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT i.opus_name
  FROM json_each(?4) v
 CROSS JOIN annotation_item i ON i.opus_name = v.value
  JOIN annotation_tag t      ON t.opus_name = i.opus_name AND t.tag_id = i.tag_id
  JOIN meeting_annotations m ON m.opus_name = i.opus_name AND m.state = ?3
 WHERE t.tag_id = ?1 OR t.label_folded = ?2`,
		trimmed, foldTagLabel(trimmed), annotationsStateIndexed, namesJSON(names))
	if err != nil {
		return nil, fmt.Errorf("resolve tag: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan tagged meeting: %w", err)
		}
		out[name] = true
	}
	return out, rows.Err()
}

// searchHitMark is one tag a search hit's range overlaps.
type searchHitMark struct {
	TagID string `json:"tagId"`
	Label string `json:"label"`
}

// markSpan is a half-open range in one meeting.
type markSpan struct {
	opusName       string
	startMS, endMS int64
}

// marksOverlapping answers, per span, the tags whose time-range marks overlap
// it; half-open, so touching is not overlapping. An entry is nil when the
// meeting's marks are unknown — not indexed, unreadable, or made against other
// audio, whose ranges would point at the wrong moment — and non-nil, possibly
// empty, otherwise. Whole-meeting marks are never cited.
func (s *annotationStore) marksOverlapping(ctx context.Context, spans []markSpan) ([][]searchHitMark, error) {
	out := make([][]searchHitMark, len(spans))
	if len(spans) == 0 {
		return out, nil
	}
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.opusName)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT m.opus_name, i.tag_id, t.label, i.start_ms, i.end_ms
  FROM meeting_annotations m
  JOIN json_each(?1) v        ON v.value = m.opus_name
  LEFT JOIN annotation_item i ON i.opus_name = m.opus_name AND i.kind = ?3
  LEFT JOIN annotation_tag t  ON t.opus_name = i.opus_name AND t.tag_id = i.tag_id
 WHERE m.state = ?2 AND m.resolved = 1`, namesJSON(names), annotationsStateIndexed, annotationsTargetTimeRange)
	if err != nil {
		return nil, fmt.Errorf("read marks for hits: %w", err)
	}
	defer rows.Close()
	type rangeMark struct {
		tagID, label   string
		startMS, endMS int64
	}
	known := map[string][]rangeMark{}
	for rows.Next() {
		var name string
		var tagID, label sql.NullString
		var start, end sql.NullInt64
		if err := rows.Scan(&name, &tagID, &label, &start, &end); err != nil {
			return nil, fmt.Errorf("scan mark for hit: %w", err)
		}
		marks := known[name]
		if tagID.Valid && label.Valid {
			marks = append(marks, rangeMark{tagID.String, label.String, start.Int64, end.Int64})
		}
		known[name] = marks
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read marks for hits: %w", err)
	}

	for i, span := range spans {
		marks, ok := known[span.opusName]
		if !ok {
			continue
		}
		out[i] = []searchHitMark{}
		seen := map[string]bool{}
		for _, mark := range marks {
			if mark.startMS < span.endMS && span.startMS < mark.endMS && !seen[mark.tagID] {
				seen[mark.tagID] = true
				out[i] = append(out[i], searchHitMark{TagID: mark.tagID, Label: mark.label})
			}
		}
		sort.Slice(out[i], func(a, b int) bool {
			fa, fb := foldTagLabel(out[i][a].Label), foldTagLabel(out[i][b].Label)
			if fa != fb {
				return fa < fb
			}
			return out[i][a].TagID < out[i][b].TagID
		})
	}
	return out, nil
}

// --- changing a tag ---------------------------------------------------------

// tagCarriers is the recordings among visible that carry tagID.
func (s *annotationStore) tagCarriers(ctx context.Context, tagID string, visible []string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT t.opus_name
  FROM annotation_tag t
  JOIN json_each(?2) v ON v.value = t.opus_name
 WHERE t.tag_id = ?1
 ORDER BY t.opus_name`, tagID, namesJSON(visible))
	if err != nil {
		return nil, fmt.Errorf("read the recordings carrying a tag: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan a recording carrying a tag: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (s *annotationStore) TagVisible(ctx context.Context, tagID string, visible []string) (bool, error) {
	names, err := s.tagCarriers(ctx, tagID, visible)
	return len(names) > 0, err
}

// tagLabelledOtherwise reports whether one of visible carries tagID under a
// label other than label.
func (s *annotationStore) tagLabelledOtherwise(ctx context.Context, tagID, label string, visible []string) (bool, error) {
	var one int
	switch err := s.db.QueryRowContext(ctx, `
SELECT 1
  FROM annotation_tag t
  JOIN json_each(?3) v ON v.value = t.opus_name
 WHERE t.tag_id = ?1 AND t.label != ?2
 LIMIT 1`, tagID, label, namesJSON(visible)).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("look up a tag's labels: %w", err)
	}
	return true, nil
}

// labelOwner is a tag other than except that one of visible labels label.
func (s *annotationStore) labelOwner(ctx context.Context, label, except string, visible []string) (string, bool, error) {
	var tagID string
	switch err := s.db.QueryRowContext(ctx, `
SELECT t.tag_id
  FROM annotation_tag t
  JOIN json_each(?3) v ON v.value = t.opus_name
 WHERE t.label_folded = ?1 AND t.tag_id != ?2
 ORDER BY t.tag_id
 LIMIT 1`, foldTagLabel(label), except, namesJSON(visible)).Scan(&tagID); {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("look up a tag label: %w", err)
	}
	return tagID, true, nil
}

// tagInUse reports whether any indexed recording, whoever may read it, carries
// tagID. It only ever decides what happens to the tag's style.
func (s *annotationStore) tagInUse(ctx context.Context, tagID string) (bool, error) {
	var one int
	switch err := s.db.QueryRowContext(ctx, `SELECT 1 FROM annotation_tag WHERE tag_id = ? LIMIT 1`, tagID).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("look up a tag: %w", err)
	}
	return true, nil
}

// --- rebuild support --------------------------------------------------------

// recordedContainers is each recorded meeting's container digest ("" when
// unknown), for a rebuild deciding whether a file needs reading again.
func (s *annotationStore) recordedContainers(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT opus_name, container_sha256 FROM meeting_annotations`)
	if err != nil {
		return nil, fmt.Errorf("read recorded annotations: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, container string
		if err := rows.Scan(&name, &container); err != nil {
			return nil, fmt.Errorf("scan recorded annotations: %w", err)
		}
		out[name] = container
	}
	return out, rows.Err()
}

// builtOnce reports whether a rebuild has ever completed on this index.
func (s *annotationStore) builtOnce(ctx context.Context) (bool, error) {
	var value string
	switch err := s.db.QueryRowContext(ctx,
		`SELECT value FROM annotations_meta WHERE key = ?`, annotationsMetaBuilt).Scan(&value); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read the index's build marker: %w", err)
	}
	return true, nil
}

// markBuilt records that a rebuild has completed, with when.
func (s *annotationStore) markBuilt(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO annotations_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		annotationsMetaBuilt, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("record the index's build marker: %w", err)
	}
	return nil
}

func uniqueNames(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// namesJSON binds a set of names for json_each; a []string always encodes.
func namesJSON(names []string) string {
	encoded, _ := json.Marshal(uniqueNames(names))
	return string(encoded)
}
