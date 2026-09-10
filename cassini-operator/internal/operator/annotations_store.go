package operator

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// The marks projection (D-737): annotations.sqlite3, a SIDECAR beside
// jobs.sqlite3 and search.sqlite3, for the reasons search.sqlite3 is one
// (search_store.go) — its own WAL and pool so a vocabulary read never queues
// behind the publish writer, disposable so deleting it costs a rebuild and
// nothing else, and an outage here is an outage for tags only.
//
// Its own file rather than tables in search.sqlite3, which the shaping doc
// first proposed: tag narrowing happens on the visible set BEFORE the search
// statement, so nothing ever joins the two, and a schema bump here must not
// force a full transcript re-index there.
//
// THE INVARIANT, inherited from search and stated again because it is the
// whole design:
//
//	The recording is the record. Marks live inside each .opus as
//	manifest.annotations; this file is a copy, rebuildable from the archive,
//	and any state in it that cannot be rebuilt from the archive is a bug.
//
// That is why every write path commits the FILE first and records here second,
// and why a failure here costs coverage, never a mark. It is also why the one
// piece of installation state held here — the tag namespace — is ADOPTED from
// the archive's own files on a fresh volume rather than minted: minted blindly,
// a rebuilt projection would give the same archive a second namespace, and the
// same word would stop meaning the same tag.
//
// And, as in search, access control is absent from this file entirely. It knows
// which meeting a mark belongs to and nothing about who may read it; every read
// takes the caller's visible set as an ARGUMENT, resolved per request from
// Nextcloud and never cached here.
const (
	// annotationsStoreFilename sits beside the job database in the same state dir.
	annotationsStoreFilename = "annotations.sqlite3"

	// annotationsSchemaVersion is stamped into PRAGMA user_version. Bump it for
	// ANY change to the schema OR to what ingest keeps from a document — a
	// reader that starts understanding a new target kind changes the row set
	// without changing the DDL, and only a bump makes old files re-read.
	annotationsSchemaVersion = 1

	// annotationsStateIndexed: the meeting's marks are present and current.
	annotationsStateIndexed = "indexed"
	// annotationsStateUnavailable: the meeting is known, but its marks are NOT.
	//
	// The difference between a partial vocabulary and a false one, exactly as
	// searchStateUnavailable is for search: "3 meetings are tagged hiring" is a
	// claim only when every visible meeting's marks were read.
	annotationsStateUnavailable = "unavailable"

	// annotationsMetaNamespace is the annotations_meta key for the tag namespace.
	annotationsMetaNamespace = "tagNamespace"

	// The document format and target kinds this reader understands. Duplicated
	// from the CLI's portable package because the operator cannot import it
	// (annotate_cli.go); the strings are the format's contract, not either
	// module's.
	annotationsDocFormatV1     = "cassini.annotations.v1"
	annotationsTargetMeeting   = "meeting"
	annotationsTargetTimeRange = "time-range"

	// Reasons recorded against a meeting whose marks could not be read. For the
	// operator, never shown to a caller.
	annotationsReasonUnsupported = "annotations-format-unsupported"
	annotationsReasonUnreadable  = "annotations-unreadable"
)

// annotationsNamespaceRE is the only namespace shape adopted from a file: a
// lowercase urn:uuid, as the writer's validation requires. A file carrying
// anything else is still indexed; its namespace simply cannot become the
// installation's.
var annotationsNamespaceRE = regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// annotationsSchemaSQL is the whole schema, design doc §4 plus two columns the
// doc's DDL lacked. There are no migrations, by design: a version mismatch
// deletes the file and it is rebuilt (openAnnotationStore).
const annotationsSchemaSQL = `
-- Installation state that is not about any one meeting. Today: the tag
-- namespace, adopted from the archive or minted once (see Namespace).
CREATE TABLE IF NOT EXISTS annotations_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- What the projection knows about each meeting. References only, like
-- search's meeting_index: no title, date or room — the caller's own catalog
-- supplies those at request time.
CREATE TABLE IF NOT EXISTS meeting_annotations (
  -- path.Base of the DELIVERED .opus: THE join key, the string the per-caller
  -- visibility scan returns.
  opus_name        TEXT PRIMARY KEY,
  state            TEXT NOT NULL,
  -- Why a meeting is unavailable, for the operator rather than the caller.
  reason           TEXT NOT NULL DEFAULT '',
  revision         INTEGER NOT NULL DEFAULT 0,
  -- The binding: the audio digest the marks were made against.
  audio_sha256     TEXT NOT NULL DEFAULT '',
  -- 0 when the binding is not this file's audio. Such marks still say the
  -- meeting was tagged, but their time ranges must not be drawn against this
  -- audio — so a search hit never cites one.
  resolved         INTEGER NOT NULL DEFAULT 1,
  -- The document's tagNamespace. Not in the design doc's DDL: kept per meeting
  -- so a rebuild can adopt the archive's namespace even from files whose every
  -- mark has since been removed (they still carry the namespace).
  namespace        TEXT NOT NULL DEFAULT '',
  -- Which bytes these rows came from, so a rebuild can skip a meeting whose
  -- file has not changed with one PROPFIND and no download. Never identity,
  -- never compared with the seal (design doc §5). Empty when unknown.
  container_sha256 TEXT NOT NULL DEFAULT '',
  indexed_at       TEXT NOT NULL
);

-- One row per tag DEFINITION per meeting. The same id can carry a different
-- label in different files (relabel is per file), so the label lives here and
-- not in a global tag table.
CREATE TABLE IF NOT EXISTS annotation_tag (
  opus_name    TEXT NOT NULL REFERENCES meeting_annotations(opus_name) ON DELETE CASCADE,
  namespace    TEXT NOT NULL,
  tag_id       TEXT NOT NULL,
  label        TEXT NOT NULL,
  -- strings.ToLower of the trimmed label, and what every label match uses.
  -- Not in the design doc's DDL, which indexed label COLLATE NOCASE: SQLite's
  -- NOCASE folds ASCII only, so "Übergabe" and "übergabe" would be two tags.
  -- Go's folding is what the rest of this code means by case-insensitive.
  label_folded TEXT NOT NULL,
  PRIMARY KEY (opus_name, tag_id)
);

-- One row per mark this reader understands. Items of an unknown kind are not
-- stored: a reader that does not understand a target cannot say what it covers.
CREATE TABLE IF NOT EXISTS annotation_item (
  opus_name    TEXT NOT NULL REFERENCES meeting_annotations(opus_name) ON DELETE CASCADE,
  item_id      TEXT NOT NULL,
  tag_id       TEXT NOT NULL,
  kind         TEXT NOT NULL,
  start_ms     INTEGER,
  end_ms       INTEGER,
  actor_kind   TEXT NOT NULL,
  actor_id     TEXT NOT NULL,
  operation_id TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  PRIMARY KEY (opus_name, item_id)
);

CREATE INDEX IF NOT EXISTS annotation_item_by_tag ON annotation_item(tag_id);
CREATE INDEX IF NOT EXISTS annotation_tag_by_label ON annotation_tag(label_folded);
`

// annotationStore is the projection. It implements annotationIndex — the seam
// the write endpoint and the publish path record through — and also serves the
// read routes: the vocabulary, tag narrowing, and the marks a search hit cites.
type annotationStore struct {
	// rebuildPending is set while the runtime's first rebuild of a never-built
	// index runs; Namespace refuses meanwhile (errAnnotationIndexBuilding).
	rebuildPending atomic.Bool
	db             *sql.DB
	path           string
}

var _ annotationIndex = (*annotationStore)(nil)

// annotationStorePath is where the projection lives for a given job-database
// path, or "" when that cannot be answered — refused for a blank or relative
// path for the reason searchStorePath gives (a test's zero Config once put an
// index beside the source).
func annotationStorePath(dbPath string) string {
	trimmed := strings.TrimSpace(dbPath)
	if trimmed == "" || !filepath.IsAbs(trimmed) {
		return ""
	}
	return filepath.Join(filepath.Dir(trimmed), annotationsStoreFilename)
}

// annotationReads is the concrete projection behind rt.annotations, for the
// read routes that need more than the write seam. Nil when the store could not
// be opened — or when something other than this store stands behind the seam,
// as it can in a test; the read routes then answer 503, never an empty result.
func (rt *Runtime) annotationReads() *annotationStore {
	if rt == nil {
		return nil
	}
	store, _ := rt.annotations.(*annotationStore)
	return store
}

// openAnnotationStore opens the projection, deleting and recreating it when the
// file on disk was written by a different schema version — search's rule, for
// search's reason: migrations would be machinery protecting data that is
// regenerable by definition, and an older binary meeting a newer file must
// rebuild rather than refuse to start.
func openAnnotationStore(path string, logger *log.Logger) (*annotationStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("annotations index path must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir annotations index dir: %w", err)
	}
	store, err := openAnnotationStoreAt(path)
	if err != nil {
		return nil, err
	}
	version, err := store.userVersion()
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if version == annotationsSchemaVersion {
		return store, nil
	}
	// A brand-new file reads 0 and is simply stamped; anything else is a real
	// mismatch, discarded loudly. The namespace goes with it — which is safe
	// only because a rebuild adopts it back from the archive's files.
	if version != 0 && logger != nil {
		logger.Printf("annotations index at %s is schema v%d, this build writes v%d — deleting and rebuilding (run %s to refill it)",
			path, version, annotationsSchemaVersion, backfillAnnotationsCommand)
	}
	if err := store.Close(); err != nil {
		return nil, fmt.Errorf("close stale annotations index: %w", err)
	}
	if err := removeSearchStoreFiles(path); err != nil {
		return nil, err
	}
	store, err = openAnnotationStoreAt(path)
	if err != nil {
		return nil, err
	}
	if err := store.applySchema(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

// openAnnotationStoreAt opens the file with search's pragmas: WAL so a reader
// never blocks the writer, a real busy_timeout, foreign keys on.
func openAnnotationStoreAt(path string) (*annotationStore, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open annotations index: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping annotations index: %w", err)
	}
	return &annotationStore{db: db, path: path}, nil
}

func (s *annotationStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *annotationStore) userVersion() (int, error) {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read annotations index version: %w", err)
	}
	return version, nil
}

// applySchema creates the tables and stamps the version LAST, so a crash midway
// leaves a file the next open treats as fresh.
func (s *annotationStore) applySchema() error {
	if _, err := s.db.Exec(annotationsSchemaSQL); err != nil {
		return fmt.Errorf("create annotations schema: %w", err)
	}
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", annotationsSchemaVersion)); err != nil {
		return fmt.Errorf("stamp annotations schema version: %w", err)
	}
	return nil
}

// foldTagLabel is what "the same label" means everywhere in the operator:
// trimmed, then lower-cased by Go, which folds Unicode as well as ASCII. No
// normalisation beyond that — neither module carries golang.org/x/text, so a
// precomposed and a decomposed "Ü" stay different labels (design doc §3).
func foldTagLabel(label string) string {
	return strings.ToLower(strings.TrimSpace(label))
}

// --- ingest -----------------------------------------------------------------

// projectedDocument is the minimal v1 reader. Deliberately NOT the CLI's type,
// which the operator cannot import, and deliberately tolerant where the
// writer is strict: this reads what is in the archive, and one malformed mark
// must cost that mark, not the meeting.
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
		CreatedAtUTC string `json:"createdAtUtc"`
		Actor        struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"actor"`
		OperationID string `json:"operationId"`
	} `json:"items"`
}

type projectedTag struct {
	id, label string
}

type projectedItem struct {
	id, tagID, kind                string
	startMS, endMS                 sql.NullInt64
	actorKind, actorID, opID, when string
}

// projectedMeeting is what one file contributes: either its marks, or the
// reason they could not be read.
type projectedMeeting struct {
	unreadable string // a reason; empty when the document was read
	revision   int
	audio      string
	resolved   bool
	namespace  string
	tags       []projectedTag
	items      []projectedItem
}

// projectAnnotations reads one file's document into rows.
//
// Absent or null is a meeting with no marks, which is an ordinary indexed
// state. A document in a format this reader does not know is NOT that: the
// file may carry marks we cannot see, so the meeting is recorded unavailable
// and the vocabulary reports partial coverage rather than a confident zero.
// Within a v1 document, anything this reader cannot interpret is skipped one
// entry at a time — a tag with no id or label, an item of an unknown kind, a
// range that is empty or negative, an item naming a tag the document does not
// define.
func projectAnnotations(raw json.RawMessage, cliResolved *bool, fileAudio string) projectedMeeting {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return projectedMeeting{resolved: true}
	}
	var probe struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return projectedMeeting{unreadable: annotationsReasonUnreadable, resolved: true}
	}
	if probe.Format != annotationsDocFormatV1 {
		return projectedMeeting{unreadable: fmt.Sprintf("%s: %q", annotationsReasonUnsupported, truncateReason(probe.Format)), resolved: true}
	}
	var doc projectedDocument
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return projectedMeeting{unreadable: annotationsReasonUnreadable, resolved: true}
	}

	out := projectedMeeting{
		revision:  doc.Revision,
		audio:     strings.ToLower(strings.TrimSpace(doc.AudioOpusSHA256)),
		namespace: strings.TrimSpace(doc.TagNamespace),
	}
	// The CLI's verdict when it gave one; otherwise the same comparison it
	// makes, against the audio digest it reported for the file.
	if cliResolved != nil {
		out.resolved = *cliResolved
	} else {
		file := strings.ToLower(strings.TrimSpace(fileAudio))
		out.resolved = out.audio != "" && out.audio == file
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
		row := projectedItem{
			id: id, tagID: tagID, kind: item.Target.Kind,
			actorKind: item.Actor.Kind, actorID: item.Actor.ID,
			opID: item.OperationID, when: item.CreatedAtUTC,
		}
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
			// A kind this reader does not know. It cannot say what the mark
			// covers, so it cannot narrow or cite by it.
			continue
		}
		seen[id] = true
		out.items = append(out.items, row)
	}
	return out
}

// truncateReason bounds a value copied out of a file into a reason.
func truncateReason(value string) string {
	value = oneLineListField(value)
	if len(value) > 64 {
		return value[:64]
	}
	return value
}

// Record replaces everything the projection knows about opusName with what its
// file now carries (annotationIndex).
//
// Replace, never merge: a mark removed from the file must leave the projection
// too. One transaction, so a failure leaves the previous rows intact rather than
// a mixture of two revisions.
func (s *annotationStore) Record(ctx context.Context, opusName string, result annotateResult) error {
	// Live writes race: two writers commit revisions 5 and 6, and 5's slower
	// Record must not replace 6's rows. The rebuild passes false — it read the
	// delivered file itself, so what it saw is the truth even if older.
	_, err := s.record(ctx, opusName, result, true)
	return err
}

// record is Record, reporting which state the meeting ended in — a document in
// an unknown format records it unavailable, and a rebuild counts that
// separately from a meeting it indexed.
func (s *annotationStore) record(ctx context.Context, opusName string, result annotateResult, onlyIfNewer bool) (string, error) {
	name := strings.TrimSpace(opusName)
	if name == "" {
		return "", errors.New("opus name must not be empty")
	}
	projected := projectAnnotations(result.Annotations, result.Resolved, result.AudioOpusSHA256)
	state, reason := annotationsStateIndexed, ""
	if projected.unreadable != "" {
		state, reason = annotationsStateUnavailable, projected.unreadable
	}
	revision := projected.revision
	if revision == 0 {
		revision = result.Revision
	}
	container := strings.ToLower(strings.TrimSpace(result.ContainerSHA256))

	skipped := false
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if onlyIfNewer {
			var current int
			switch err := tx.QueryRowContext(ctx,
				`SELECT revision FROM meeting_annotations WHERE opus_name = ?`, name).Scan(&current); {
			case errors.Is(err, sql.ErrNoRows):
			case err != nil:
				return fmt.Errorf("read the recorded revision: %w", err)
			case current > revision:
				skipped = true
				return nil
			}
		}
		if err := deleteAnnotationRows(ctx, tx, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO meeting_annotations (opus_name, state, reason, revision, audio_sha256, resolved, namespace, container_sha256, indexed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			name, state, reason, revision, projected.audio, boolInt(projected.resolved),
			projected.namespace, container, nowUTCString()); err != nil {
			return fmt.Errorf("record meeting annotations: %w", err)
		}
		if len(projected.tags) > 0 {
			stmt, err := tx.PrepareContext(ctx,
				`INSERT INTO annotation_tag (opus_name, namespace, tag_id, label, label_folded) VALUES (?, ?, ?, ?, ?)`)
			if err != nil {
				return fmt.Errorf("prepare tag insert: %w", err)
			}
			defer stmt.Close()
			for _, tag := range projected.tags {
				if _, err := stmt.ExecContext(ctx, name, projected.namespace, tag.id, tag.label, foldTagLabel(tag.label)); err != nil {
					return fmt.Errorf("insert tag: %w", err)
				}
			}
		}
		if len(projected.items) > 0 {
			stmt, err := tx.PrepareContext(ctx, `
INSERT INTO annotation_item (opus_name, item_id, tag_id, kind, start_ms, end_ms, actor_kind, actor_id, operation_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
			if err != nil {
				return fmt.Errorf("prepare mark insert: %w", err)
			}
			defer stmt.Close()
			for _, item := range projected.items {
				if _, err := stmt.ExecContext(ctx, name, item.id, item.tagID, item.kind, item.startMS, item.endMS,
					item.actorKind, item.actorID, item.opID, item.when); err != nil {
					return fmt.Errorf("insert mark: %w", err)
				}
			}
		}
		return nil
	})
	if err != nil || skipped {
		return "", err
	}
	return state, nil
}

// MarkUnavailable records that opusName's marks could not be read
// (annotationIndex), dropping any rows it had and forgetting which bytes they
// came from — so a rebuild reads the file again instead of skipping it.
func (s *annotationStore) MarkUnavailable(ctx context.Context, opusName, reason string) error {
	name := strings.TrimSpace(opusName)
	if name == "" {
		return errors.New("opus name must not be empty")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := deleteAnnotationRows(ctx, tx, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO meeting_annotations (opus_name, state, reason, indexed_at)
VALUES (?, ?, ?, ?)`, name, annotationsStateUnavailable, strings.TrimSpace(reason), nowUTCString()); err != nil {
			return fmt.Errorf("mark annotations unavailable: %w", err)
		}
		return nil
	})
}

// ForgetMeeting removes a meeting from the projection entirely. Housekeeping for
// a recording the archive no longer holds, never an access control.
func (s *annotationStore) ForgetMeeting(ctx context.Context, opusName string) error {
	name := strings.TrimSpace(opusName)
	if name == "" {
		return errors.New("opus name must not be empty")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		return deleteAnnotationRows(ctx, tx, name)
	})
}

// deleteAnnotationRows drops a meeting and its rows. Explicit rather than left
// to ON DELETE CASCADE, so correctness does not hang on a per-connection pragma.
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

// ResolveLabel maps a label to the tag id this installation already uses for it
// anywhere in the archive (annotationIndex).
//
// Across the WHOLE projection, not the caller's visible set, so one word maps to
// one id everywhere — the write endpoint uses this to reuse an id rather than
// mint a second one. Safe because tag ids are random: handing one back reveals
// nothing the caller did not already supply.
//
// Restricted to the installation's namespace once one is known. When several
// ids carry the same label — files tagged before the projection existed can
// each have minted their own — the one on the most meetings wins, then the
// smallest id, so the answer is stable from call to call.
// Only meetings in visible — the caller's own readable set — are consulted.
// Resolving across hidden meetings is an oracle: relabel a tag you own, mark
// the old word again, and getting the old id back reveals that some meeting you
// cannot open carries it. An empty set resolves nothing.
func (s *annotationStore) ResolveLabel(ctx context.Context, label string, visible []string) (string, bool, error) {
	folded := foldTagLabel(label)
	if folded == "" {
		return "", false, nil
	}
	namespace, err := s.effectiveNamespace(ctx)
	if err != nil {
		return "", false, err
	}
	visibleJSON, err := json.Marshal(nonNilStrings(visible))
	if err != nil {
		return "", false, fmt.Errorf("encode visible set: %w", err)
	}
	query := `
SELECT t.tag_id
  FROM annotation_tag t
  JOIN meeting_annotations m ON m.opus_name = t.opus_name AND m.state = ?2
  JOIN json_each(?3) v ON v.value = t.opus_name
 WHERE t.label_folded = ?1`
	args := []any{folded, annotationsStateIndexed, string(visibleJSON)}
	if namespace != "" {
		query += ` AND t.namespace = ?4`
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

// Namespace is this installation's tag namespace (annotationIndex).
//
// Stored once in annotations_meta. When nothing is stored — a fresh or rebuilt
// volume — the namespace the archive's own files carry most is ADOPTED, and one
// is minted only when the projection holds no file that carries one. Adoption
// first is what keeps the same word the same tag across a volume rebuild.
//
// The store is insert-if-absent and re-read, so two concurrent first calls
// agree on one value rather than each minting their own.
func (s *annotationStore) Namespace(ctx context.Context) (string, error) {
	// Checked first, stored namespace or not: while the index does not yet know
	// the archive, a label lookup would miss and mint a second id for a word the
	// archive already tags.
	if s.rebuildPending.Load() {
		return "", errAnnotationIndexBuilding
	}
	stored, err := s.storedNamespace(ctx)
	if err != nil || stored != "" {
		return stored, err
	}
	candidate, err := s.archiveNamespace(ctx)
	if err != nil {
		return "", err
	}
	if candidate == "" {
		if candidate, err = mintTagNamespace(); err != nil {
			return "", err
		}
	}
	return s.storeNamespaceIfAbsent(ctx, candidate)
}

// namespaceAdoption is what a rebuild did about the namespace.
type namespaceAdoption struct {
	// Namespace is what is stored after the call; empty when nothing is stored
	// and the archive carries none (a rebuild never mints).
	Namespace string
	// Adopted is true when this call stored it, from the archive.
	Adopted bool
	// Archive is the namespace the archive's files carry most, when it differs
	// from what was already stored — one installation meant to have one, and a
	// difference is for an operator to look at, not for a rebuild to overwrite.
	Archive string
}

// adoptNamespace stores the archive's own namespace when none is stored yet. It
// never mints: a rebuild of an archive with no marks leaves minting to the
// first write, which is the moment a namespace is actually needed.
func (s *annotationStore) adoptNamespace(ctx context.Context) (namespaceAdoption, error) {
	stored, err := s.storedNamespace(ctx)
	if err != nil {
		return namespaceAdoption{}, err
	}
	archive, err := s.archiveNamespace(ctx)
	if err != nil {
		return namespaceAdoption{}, err
	}
	if stored != "" {
		out := namespaceAdoption{Namespace: stored}
		if archive != "" && archive != stored {
			out.Archive = archive
		}
		return out, nil
	}
	if archive == "" {
		return namespaceAdoption{}, nil
	}
	got, err := s.storeNamespaceIfAbsent(ctx, archive)
	if err != nil {
		return namespaceAdoption{}, err
	}
	return namespaceAdoption{Namespace: got, Adopted: got == archive}, nil
}

// effectiveNamespace is what Namespace would answer, without minting or
// storing anything: the stored one, else the archive's, else "".
func (s *annotationStore) effectiveNamespace(ctx context.Context) (string, error) {
	stored, err := s.storedNamespace(ctx)
	if err != nil || stored != "" {
		return stored, err
	}
	return s.archiveNamespace(ctx)
}

func (s *annotationStore) storedNamespace(ctx context.Context) (string, error) {
	var value string
	switch err := s.db.QueryRowContext(ctx,
		`SELECT value FROM annotations_meta WHERE key = ?`, annotationsMetaNamespace).Scan(&value); {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("read tag namespace: %w", err)
	}
	return value, nil
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

func (s *annotationStore) storeNamespaceIfAbsent(ctx context.Context, candidate string) (string, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO annotations_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`,
		annotationsMetaNamespace, candidate); err != nil {
		return "", fmt.Errorf("store tag namespace: %w", err)
	}
	stored, err := s.storedNamespace(ctx)
	if err != nil {
		return "", err
	}
	if stored == "" {
		return "", errors.New("tag namespace was not stored")
	}
	return stored, nil
}

// mintTagNamespace is a random (version 4) urn:uuid, lowercase as the format
// requires.
func mintTagNamespace() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("mint tag namespace: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// --- reads for one caller ---------------------------------------------------
//
// Every read below takes the caller's visible set and binds it into the
// statement as json_each(?1), search's rule: the empty set is safe by
// construction, and nothing about a meeting outside the set can reach an
// answer — not a tag, not a count.

// annotationCoverage is the honesty field: of the meetings this caller may
// read, how many the projection holds marks for. When Indexed is lower than
// Visible, a vocabulary or a tag narrowing is partial and says so.
type annotationCoverage struct {
	Visible int `json:"visible"`
	Indexed int `json:"indexed"`
}

// tagVocabularyEntry is one tag as the caller's visible meetings use it.
type tagVocabularyEntry struct {
	TagID     string `json:"tagId"`
	Namespace string `json:"namespace"`
	// Label is the label most of those meetings give the tag. Relabel is per
	// file, so the same id can carry different labels; the most common one,
	// ties to the smallest, is the stable answer.
	Label string `json:"label"`
	// Meetings is how many visible meetings carry at least one mark of it.
	Meetings int `json:"meetings"`
	// Marks is how many marks of it those meetings carry.
	Marks int `json:"marks"`
}

// Coverage counts how many of the visible meetings the projection holds marks
// for. Scoped to the caller, never global, for search's reason: a global count
// tells them about meetings they cannot read.
func (s *annotationStore) Coverage(ctx context.Context, visible []string) (annotationCoverage, error) {
	names := uniqueNames(visible)
	coverage := annotationCoverage{Visible: len(names)}
	if len(names) == 0 {
		return coverage, nil
	}
	encoded, err := json.Marshal(names)
	if err != nil {
		return annotationCoverage{}, err
	}
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM meeting_annotations m
  JOIN json_each(?1) v ON v.value = m.opus_name
 WHERE m.state = ?2`, string(encoded), annotationsStateIndexed).Scan(&coverage.Indexed); err != nil {
		return annotationCoverage{}, fmt.Errorf("read annotations coverage: %w", err)
	}
	return coverage, nil
}

// Vocabulary lists the tags marked on the caller's visible meetings, counted
// over those meetings only. A tag carried only by a meeting the caller cannot
// open never appears, and a hidden meeting moves no count.
//
// Keyed by (namespace, tag id), because that pair is what "the same tag" means
// across files. Only tags with at least one mark this reader understands are
// listed; a definition whose every mark is of an unknown kind has nothing to
// point at.
func (s *annotationStore) Vocabulary(ctx context.Context, visible []string) ([]tagVocabularyEntry, error) {
	names := uniqueNames(visible)
	if len(names) == 0 {
		return []tagVocabularyEntry{}, nil
	}
	encoded, err := json.Marshal(names)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT t.namespace, t.tag_id, t.label, COUNT(*)
  FROM annotation_item i
  JOIN json_each(?1) v         ON v.value = i.opus_name
  JOIN meeting_annotations m   ON m.opus_name = i.opus_name AND m.state = ?2
  JOIN annotation_tag t        ON t.opus_name = i.opus_name AND t.tag_id = i.tag_id
 GROUP BY i.opus_name, t.namespace, t.tag_id`, string(encoded), annotationsStateIndexed)
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
		// One row per (meeting, tag): each is one meeting.
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

func mostCommonLabel(labels map[string]int) string {
	best, bestCount := "", -1
	for label, count := range labels {
		if count > bestCount || (count == bestCount && label < best) {
			best, bestCount = label, count
		}
	}
	return best
}

// taggedMeetings is every meeting in the projection carrying at least one mark
// of the tag — matched by id exactly, or by label case-insensitively.
//
// Deliberately NOT scoped to a caller: it is only ever intersected with a
// visible set, in Go, before anything reaches a response (narrowVisibleToTag).
// Whether the tag exists at all is therefore never observable — a tag carried
// only by hidden meetings narrows to the same empty set as a tag nobody uses.
func (s *annotationStore) taggedMeetings(ctx context.Context, tag string) (map[string]struct{}, error) {
	trimmed := strings.TrimSpace(tag)
	out := map[string]struct{}{}
	if trimmed == "" {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT i.opus_name
  FROM annotation_item i
  JOIN annotation_tag t      ON t.opus_name = i.opus_name AND t.tag_id = i.tag_id
  JOIN meeting_annotations m ON m.opus_name = i.opus_name AND m.state = ?3
 WHERE t.tag_id = ?1 OR t.label_folded = ?2`, trimmed, foldTagLabel(trimmed), annotationsStateIndexed)
	if err != nil {
		return nil, fmt.Errorf("resolve tag: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan tagged meeting: %w", err)
		}
		out[name] = struct{}{}
	}
	return out, rows.Err()
}

// narrowVisibleToTag is the caller's visible set, narrowed to the meetings
// carrying a mark of tag, in the visible set's own order — plus the coverage
// that narrowing can claim.
//
// The intersection happens HERE, before any statement runs, and the result is
// what a caller of this binds as its visible set. That is the D-623 rule for
// --room applied to tags: filtering a rank-truncated page of results instead
// (the Option 1 defect) would report "nothing tagged" to a caller holding
// tagged meetings that simply ranked below the page.
func narrowVisibleToTag(ctx context.Context, store *annotationStore, tag string, visible []string) ([]string, annotationCoverage, error) {
	tagged, err := store.taggedMeetings(ctx, tag)
	if err != nil {
		return nil, annotationCoverage{}, err
	}
	coverage, err := store.Coverage(ctx, visible)
	if err != nil {
		return nil, annotationCoverage{}, err
	}
	narrowed := make([]string, 0, len(tagged))
	seen := make(map[string]bool, len(tagged))
	for _, name := range visible {
		if _, ok := tagged[name]; ok && !seen[name] {
			seen[name] = true
			narrowed = append(narrowed, name)
		}
	}
	return narrowed, coverage, nil
}

// searchHitMark is one tag a search hit's range overlaps.
type searchHitMark struct {
	TagID string `json:"tagId"`
	Label string `json:"label"`
}

// markSpan is a range in one meeting, half-open like every range here.
type markSpan struct {
	opusName       string
	startMS, endMS int64
}

// marksOverlapping answers, for each span, the tags whose time-range marks
// overlap it: start < span.end AND span.start < end — half-open on both sides,
// so a mark ending exactly where a span starts does not touch it.
//
// Only meetings whose marks are RESOLVED contribute: an unresolved mark was
// made against different audio, and citing its range against this audio would
// point at the wrong moment. Whole-meeting marks are not ranges and are never
// cited. Every span names a meeting from the caller's own search results, so
// nothing here can reach outside their visible set.
func (s *annotationStore) marksOverlapping(ctx context.Context, spans []markSpan) ([][]searchHitMark, error) {
	out := make([][]searchHitMark, len(spans))
	for i := range out {
		out[i] = []searchHitMark{}
	}
	if len(spans) == 0 {
		return out, nil
	}
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.opusName)
	}
	encoded, err := json.Marshal(uniqueNames(names))
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT i.opus_name, i.tag_id, t.label, i.start_ms, i.end_ms
  FROM annotation_item i
  JOIN json_each(?1) v       ON v.value = i.opus_name
  JOIN meeting_annotations m ON m.opus_name = i.opus_name AND m.state = ?2 AND m.resolved = 1
  JOIN annotation_tag t      ON t.opus_name = i.opus_name AND t.tag_id = i.tag_id
 WHERE i.kind = ?3`, string(encoded), annotationsStateIndexed, annotationsTargetTimeRange)
	if err != nil {
		return nil, fmt.Errorf("read marks for hits: %w", err)
	}
	defer rows.Close()
	type rangeMark struct {
		tagID, label   string
		startMS, endMS int64
	}
	byMeeting := map[string][]rangeMark{}
	for rows.Next() {
		var name string
		var mark rangeMark
		if err := rows.Scan(&name, &mark.tagID, &mark.label, &mark.startMS, &mark.endMS); err != nil {
			return nil, fmt.Errorf("scan mark for hit: %w", err)
		}
		byMeeting[name] = append(byMeeting[name], mark)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read marks for hits: %w", err)
	}

	for i, span := range spans {
		seen := map[string]bool{}
		for _, mark := range byMeeting[span.opusName] {
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

// --- rebuild support --------------------------------------------------------

// recordedAnnotations is what the projection holds about one meeting, for a
// rebuild deciding whether the file needs reading again.
type recordedAnnotations struct {
	state     string
	container string
}

func (s *annotationStore) recordedState(ctx context.Context) (map[string]recordedAnnotations, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT opus_name, state, container_sha256 FROM meeting_annotations`)
	if err != nil {
		return nil, fmt.Errorf("read recorded annotations: %w", err)
	}
	defer rows.Close()
	out := map[string]recordedAnnotations{}
	for rows.Next() {
		var name string
		var state recordedAnnotations
		if err := rows.Scan(&name, &state.state, &state.container); err != nil {
			return nil, fmt.Errorf("scan recorded annotations: %w", err)
		}
		out[name] = state
	}
	return out, rows.Err()
}

// inTx runs fn in a transaction, rolling back on any error.
func (s *annotationStore) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin annotations index transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit annotations index transaction: %w", err)
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

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// annotationsMetaBuilt is the annotations_meta key a completed rebuild sets.
const annotationsMetaBuilt = "built"

// errAnnotationIndexBuilding is Namespace refusing while the first rebuild of a
// never-built index runs. On a new install, a wiped volume or a schema change,
// the projection starts empty; resolving a label or minting a namespace against
// it is how one tag would become two. Writes wait for the rebuild instead.
var errAnnotationIndexBuilding = errors.New("the tag index has not finished its first rebuild")

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
