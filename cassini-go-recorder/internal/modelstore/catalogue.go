// Package modelstore owns model bytes independently from application versions.
// Its catalogue is shipped with the binary; none of its read/import operations
// need a remote index, and only Acquire constructs an HTTP request.
package modelstore

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

//go:embed catalogue.json
var catalogueJSON []byte

//go:embed NOTICE.txt
var Notices string

type File struct {
	Path     string `json:"path"`
	Artifact string `json:"artifact"`
	Required bool   `json:"required"`
}

// Model kinds. The catalogue says what each model is for, so nothing has to
// guess it from an id: a speech model transcribes and needs its pinned VAD,
// the VAD is that dependency, and a diarization model separates the voices
// on one participant's track (docs/speaker-separation.md) and needs neither.
const (
	KindSpeech      = "speech"
	KindVAD         = "vad"
	KindDiarization = "diarization"
)

type Model struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
	License     string `json:"license"`
	SourceURL   string `json:"source_url"`
	Revision    string `json:"source_sha256"`
	VADRevision string `json:"vad_revision,omitempty"`
	Files       []File `json:"files"`
}
type Artifact struct {
	Key                string `json:"key"`
	URL                string `json:"url"`
	Size               int64  `json:"size"`
	SHA256             string `json:"sha256"`
	UncompressedSize   int64  `json:"uncompressed_size"`
	UncompressedSHA256 string `json:"uncompressed_sha256"`
	Encoding           string `json:"encoding"`
}
type Catalogue struct {
	SchemaVersion int        `json:"schema_version"`
	Models        []Model    `json:"models"`
	Artifacts     []Artifact `json:"artifacts"`
}

func Shipped() Catalogue {
	var c Catalogue
	if err := json.Unmarshal(catalogueJSON, &c); err != nil {
		panic(err)
	}
	return c
}
func (c Catalogue) Model(id, revision string) (Model, error) {
	for _, m := range c.Models {
		if m.ID == id && (revision == "" || revision == m.Revision) {
			return m, nil
		}
	}
	return Model{}, fmt.Errorf("unsupported model/revision %q/%q; use a matching Cassini catalogue", id, revision)
}
func (c Catalogue) Artifact(key string) (Artifact, error) {
	for _, a := range c.Artifacts {
		if a.Key == key {
			return a, nil
		}
	}
	return Artifact{}, fmt.Errorf("catalogue artifact %q is missing", key)
}
func (c Catalogue) Validate() error {
	if c.SchemaVersion != 2 {
		return fmt.Errorf("unsupported catalogue version %d", c.SchemaVersion)
	}
	seen := map[string]bool{}
	for _, m := range c.Models {
		key := m.ID + "/" + m.Revision
		if !safeName(m.ID) || !digest(m.Revision) || seen[key] || len(m.Files) == 0 {
			return fmt.Errorf("invalid catalogue model %q", m.ID)
		}
		switch m.Kind {
		case KindSpeech:
			if !digest(m.VADRevision) {
				return fmt.Errorf("missing pinned VAD revision for %s", m.ID)
			}
			v, e := c.Model(VADModelID, m.VADRevision)
			if e != nil {
				return e
			}
			if v.Kind != KindVAD {
				return fmt.Errorf("%s pins %s, which is not a VAD model", m.ID, VADModelID)
			}
		case KindVAD:
			if m.ID != VADModelID || m.VADRevision != "" {
				return fmt.Errorf("invalid VAD model %q", m.ID)
			}
		case KindDiarization:
			// Diarization runs on its own; a VAD pin would be installed and
			// packed for nothing.
			if m.VADRevision != "" {
				return fmt.Errorf("diarization model %s pins a VAD revision", m.ID)
			}
		default:
			return fmt.Errorf("catalogue model %s has unknown kind %q", m.ID, m.Kind)
		}
		seen[key] = true
		paths := map[string]bool{}
		for _, f := range m.Files {
			a, err := c.Artifact(f.Artifact)
			if err != nil {
				return err
			}
			if !safeName(f.Path) || paths[f.Path] || a.Size <= 0 || a.UncompressedSize <= 0 || !digest(a.SHA256) || !digest(a.UncompressedSHA256) || a.Encoding != "zstd-seekable" {
				return fmt.Errorf("invalid catalogue file %s/%s", m.ID, f.Path)
			}
			if !strings.HasPrefix(a.URL, "https://dist.gocassini.com/models/files/"+a.SHA256+"/") {
				return fmt.Errorf("unexpected catalogue origin for %s", f.Path)
			}
			paths[f.Path] = true
		}
	}
	_, err := c.Model(VADModelID, "")
	return err
}

// VADModelID is the voice activity detector every speech model pins.
const VADModelID = "silero-vad"

func digest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == sha256.Size && s == strings.ToLower(s)
}
func safeName(s string) bool {
	return s != "" && s != "." && s != ".." && filepath.Base(s) == s && !strings.ContainsAny(s, "/\\\x00")
}
func (c Catalogue) Sizes(m Model) (download, installed int64) {
	for _, f := range m.Files {
		a, _ := c.Artifact(f.Artifact)
		download += a.Size
		installed += a.UncompressedSize
	}
	return
}
