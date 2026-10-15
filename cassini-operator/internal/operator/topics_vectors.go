package operator

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"time"
)

// topics.sqlite3 holds passage vectors (docs/proposals/automatic-tagging,
// "Disposable: topics.sqlite3"). Like search.sqlite3 it is deleted and
// rebuilt on a schema change; its random generation tells the reconciler that
// every meeting must be embedded again. It holds no titles, rooms, user ids
// or text.
const (
	topicsVectorsFilename      = "topics.sqlite3"
	topicsVectorsSchemaVersion = 2
)

const topicsVectorsSchemaSQL = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS passage_set (
  opus_name TEXT PRIMARY KEY, embed_key TEXT NOT NULL, model_revision TEXT NOT NULL,
  chunker_version TEXT NOT NULL, recipe_version TEXT NOT NULL, built_at INTEGER NOT NULL,
  -- What the embed reported with these vectors, so a skipped embed reports
  -- the same: the transcript digest and the detected language.
  payload_sha256 TEXT NOT NULL DEFAULT '', language TEXT NOT NULL DEFAULT '',
  language_confidence REAL NOT NULL DEFAULT 0, detector_version TEXT NOT NULL DEFAULT '',
  -- sha256 of each word of the speakers' names, to keep names out of draft
  -- topic names; never the names themselves.
  speaker_terms TEXT NOT NULL DEFAULT '[]');
CREATE TABLE IF NOT EXISTS passage (
  opus_name TEXT NOT NULL REFERENCES passage_set ON DELETE CASCADE,
  ordinal INTEGER NOT NULL, start_ms INTEGER NOT NULL, end_ms INTEGER NOT NULL,
  word_count INTEGER NOT NULL, token_count INTEGER NOT NULL, boundary TEXT NOT NULL,
  vector BLOB NOT NULL,
  PRIMARY KEY (opus_name, ordinal));
`

type topicsVectorStore struct {
	sidecarDB
	lifecycle  *Store
	generation string
}

func topicsVectorsPath(dbPath string) string { return sidecarPath(dbPath, topicsVectorsFilename) }

func openTopicsVectorStore(path string, logger *log.Logger) (*topicsVectorStore, error) {
	db, err := openSidecarDB(path, "topic vectors", topicsVectorsSchemaSQL, topicsVectorsSchemaVersion, logger)
	if err != nil {
		return nil, err
	}
	s := &topicsVectorStore{sidecarDB: db}
	err = db.inTx(context.Background(), func(tx *sql.Tx) error {
		err := tx.QueryRow(`SELECT value FROM meta WHERE key='generation'`).Scan(&s.generation)
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		s.generation = hex.EncodeToString(b[:])
		_, err = tx.Exec(`INSERT INTO meta(key,value) VALUES('generation',?)`, s.generation)
		return err
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// topicPassageSet is one meeting's stored passages.
type topicPassageSet struct {
	EmbedKey           string
	ModelRevision      string
	ChunkerVersion     string
	RecipeVersion      string
	PayloadSHA256      string
	Language           string
	LanguageConfidence float64
	DetectorVersion    string
	SpeakerTermHashes  []string
	Passages           []topicStoredPassage
}

type topicStoredPassage struct {
	topicPassage
	Ordinal    int
	TokenCount int
	Boundary   string
}

func (p topicPassageSet) passages() []topicPassage {
	out := make([]topicPassage, len(p.Passages))
	for i := range p.Passages {
		out[i] = p.Passages[i].topicPassage
	}
	return out
}

// lockMeeting takes the meeting's projection lock and refuses a retired
// meeting, as every projection writer does.
func (s *topicsVectorStore) lockMeeting(ctx context.Context, name string) (func(), error) {
	release, err := meetingProjectionLocks.acquire(ctx, name)
	if err != nil {
		return nil, err
	}
	if s.lifecycle != nil {
		if err := (ExAppConfig{lifecycle: s.lifecycle}).meetingNotRetired(ctx, name); err != nil {
			release()
			return nil, err
		}
	}
	return release, nil
}

// replaceMeeting makes the meeting's passages exactly set, in one transaction.
func (s *topicsVectorStore) replaceMeeting(ctx context.Context, name string, set topicPassageSet) error {
	release, err := s.lockMeeting(ctx, name)
	if err != nil {
		return err
	}
	defer release()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM passage_set WHERE opus_name=?`, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO passage_set(opus_name,embed_key,model_revision,chunker_version,recipe_version,built_at,payload_sha256,language,language_confidence,detector_version,speaker_terms) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			name, set.EmbedKey, set.ModelRevision, set.ChunkerVersion, set.RecipeVersion, time.Now().Unix(), set.PayloadSHA256, set.Language, set.LanguageConfidence, set.DetectorVersion, speakerTermsJSON(set.SpeakerTermHashes)); err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO passage(opus_name,ordinal,start_ms,end_ms,word_count,token_count,boundary,vector) VALUES(?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, p := range set.Passages {
			if _, err := stmt.ExecContext(ctx, name, p.Ordinal, p.StartMS, p.EndMS, p.WordCount, p.TokenCount, p.Boundary, encodeTopicVector(p.Vector)); err != nil {
				return err
			}
		}
		return nil
	})
}

// meeting reads one meeting's passages; ok is false when none are stored.
func (s *topicsVectorStore) meeting(ctx context.Context, name string) (topicPassageSet, bool, error) {
	var set topicPassageSet
	var speakers string
	err := s.db.QueryRowContext(ctx, `SELECT embed_key,model_revision,chunker_version,recipe_version,payload_sha256,language,language_confidence,detector_version,speaker_terms FROM passage_set WHERE opus_name=?`, name).
		Scan(&set.EmbedKey, &set.ModelRevision, &set.ChunkerVersion, &set.RecipeVersion, &set.PayloadSHA256, &set.Language, &set.LanguageConfidence, &set.DetectorVersion, &speakers)
	_ = json.Unmarshal([]byte(speakers), &set.SpeakerTermHashes)
	if errors.Is(err, sql.ErrNoRows) {
		return set, false, nil
	}
	if err != nil {
		return set, false, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ordinal,start_ms,end_ms,word_count,token_count,boundary,vector FROM passage WHERE opus_name=? ORDER BY ordinal`, name)
	if err != nil {
		return set, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var p topicStoredPassage
		var blob []byte
		if err := rows.Scan(&p.Ordinal, &p.StartMS, &p.EndMS, &p.WordCount, &p.TokenCount, &p.Boundary, &blob); err != nil {
			return set, false, err
		}
		if p.Vector, err = decodeTopicVector(blob); err != nil {
			return set, false, fmt.Errorf("%s passage %d: %w", name, p.Ordinal, err)
		}
		set.Passages = append(set.Passages, p)
	}
	return set, true, rows.Err()
}

// embedKey is the stored key of a meeting's passages, "" when none.
func (s *topicsVectorStore) embedKey(ctx context.Context, name string) (string, error) {
	var key string
	err := s.db.QueryRowContext(ctx, `SELECT embed_key FROM passage_set WHERE opus_name=?`, name).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return key, err
}

// forgetMeeting drops a meeting's passages. The caller holds the meeting's
// projection lock (retirement takes it once for every projection).
func (s *topicsVectorStore) forgetMeeting(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM passage_set WHERE opus_name=?`, name)
	return err
}

// size is the file's size in bytes, its WAL included.
func (s *topicsVectorStore) size() int64 {
	var total int64
	for _, suffix := range []string{"", "-wal"} {
		if info, err := os.Stat(s.path + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}

func speakerTermsJSON(hashes []string) string {
	if hashes == nil {
		hashes = []string{}
	}
	raw, _ := json.Marshal(hashes)
	return string(raw)
}

// encodeTopicVector stores a vector as little-endian float32s.
func encodeTopicVector(v []float32) []byte {
	out := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(f))
	}
	return out
}

func decodeTopicVector(b []byte) ([]float32, error) {
	if len(b)%4 != 0 || len(b) == 0 {
		return nil, fmt.Errorf("vector blob of %d bytes", len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out, nil
}

// setSpeakerTerms replaces a meeting's speaker-term hashes when its embed was
// skipped: a speaker rename changes them, not the words.
func (s *topicsVectorStore) setSpeakerTerms(ctx context.Context, name, embedKey string, hashes []string) error {
	release, err := s.lockMeeting(ctx, name)
	if err != nil {
		return err
	}
	defer release()
	_, err = s.db.ExecContext(ctx, `UPDATE passage_set SET speaker_terms=? WHERE opus_name=? AND embed_key=?`, speakerTermsJSON(hashes), name, embedKey)
	return err
}
