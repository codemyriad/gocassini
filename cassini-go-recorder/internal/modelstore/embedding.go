package modelstore

import (
	"fmt"
	"path/filepath"
)

// InstalledEmbedder returns the absolute paths of an installed embedding
// model's files. Like the diarizer's lookup it never acquires anything and
// trusts the receipt: the bytes were verified against the catalogue when they
// were installed, so nothing rehashes 300 MB per meeting.
func (s *Store) InstalledEmbedder(m Model) (EmbeddingFiles, error) {
	if m.Kind != KindEmbedding {
		return EmbeddingFiles{}, fmt.Errorf("%s is a %s model, not an embedding model", m.ID, m.Kind)
	}
	files, err := embeddingFiles(m)
	if err != nil {
		return EmbeddingFiles{}, err
	}
	if err := s.Installed(m); err != nil {
		return EmbeddingFiles{}, err
	}
	dir := s.Dir(m)
	return EmbeddingFiles{
		Graph:     filepath.Join(dir, files.Graph),
		Data:      filepath.Join(dir, files.Data),
		Tokenizer: filepath.Join(dir, files.Tokenizer),
	}, nil
}
