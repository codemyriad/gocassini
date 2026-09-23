package modelstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

type Readiness struct {
	Fingerprint string `json:"fingerprint"`
	Device      string `json:"device"`
	Revision    string `json:"revision"`
}

func (s *Store) probePath(m Model, device string) string {
	h := sha256.Sum256([]byte(device))
	return filepath.Join(s.Dir(m), ".probe-"+hex.EncodeToString(h[:8])+".json")
}
func (s *Store) Ready(m Model, device, fingerprint string) bool {
	if s.Complete(m) != nil {
		return false
	}
	data, err := os.ReadFile(s.probePath(m, device))
	if err != nil {
		return false
	}
	var r Readiness
	return json.Unmarshal(data, &r) == nil && r.Device == device && r.Fingerprint == fingerprint && r.Revision == m.Revision
}
func (s *Store) MarkReady(m Model, device, fingerprint string) error {
	if err := s.Complete(m); err != nil {
		return err
	}
	return writeJSON(s.probePath(m, device), Readiness{fingerprint, device, m.Revision})
}
