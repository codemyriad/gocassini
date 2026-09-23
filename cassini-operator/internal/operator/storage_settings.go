package operator

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const storageSettingsFileName = "storage_settings.json"

// Only the first-run acknowledgement is persisted. Old mode fields in this
// file are ignored so an upgrade cannot select a second permission model.
type StorageSettings struct {
	FirstRunAcknowledged bool `json:"first_run_acknowledged,omitempty"`
}

func storageSettingsPath(cfg Config) string {
	return filepath.Join(filepath.Dir(cfg.DBPath), storageSettingsFileName)
}

func LoadStorageSettings(path string) (StorageSettings, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return StorageSettings{}, nil
	}
	if err != nil {
		return StorageSettings{}, err
	}
	var settings StorageSettings
	err = json.Unmarshal(raw, &settings)
	return settings, err
}

func AcknowledgeStorageFirstRun(path string) error {
	settings, err := LoadStorageSettings(path)
	if err != nil {
		return err
	}
	settings.FirstRunAcknowledged = true
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type firstRunState struct {
	mu           sync.Mutex
	path         string
	acknowledged bool
}

var ncStorage firstRunState

func (s *firstRunState) setPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = path
}

func (s *firstRunState) settingsPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

func (s *firstRunState) setFirstRunAcknowledged(value bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acknowledged = value
}

func (s *firstRunState) acknowledgedFirstRun() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acknowledged
}
