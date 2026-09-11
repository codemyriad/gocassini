package operator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A tag's colour and icon are presentation, kept per installation and keyed by
// tag id (D-746) — never in the .opus, so recolouring never rewrites a
// recording. Unlike annotations.sqlite3 this file is the only copy, so an index
// rebuild leaves it alone.

const tagStylesFilename = "tag-styles.json"

var (
	tagColors = map[string]bool{
		"slate": true, "red": true, "orange": true, "amber": true, "green": true, "teal": true,
		"cyan": true, "blue": true, "indigo": true, "violet": true, "purple": true, "pink": true,
	}
	// "" is no icon.
	tagIcons = map[string]bool{
		"": true, "star": true, "flag": true, "bolt": true, "bookmark": true, "check": true, "alert": true,
		"bug": true, "heart": true, "question": true, "lightbulb": true, "target": true, "users": true,
		"calendar": true, "money": true, "lock": true, "chat": true,
	}
)

// tagStyle is one tag's entry. UpdatedBy and UpdatedAtUTC say who last
// recoloured it, changed its icon, or finished renaming or merging into it.
type tagStyle struct {
	Color        string `json:"color"`
	Icon         string `json:"icon"`
	UpdatedBy    string `json:"updatedBy,omitempty"`
	UpdatedAtUTC string `json:"updatedAtUtc,omitempty"`
}

func (style tagStyle) changedBy(caller string) tagStyle {
	style.UpdatedBy, style.UpdatedAtUTC = caller, time.Now().UTC().Format(time.RFC3339)
	return style
}

type tagStylesFile struct {
	Version int                 `json:"version"`
	Tags    map[string]tagStyle `json:"tags"`
}

// tagStyleStore is tag-styles.json. Reads take no lock: every write replaces
// the file by rename, so a reader never sees half of one.
type tagStyleStore struct {
	mu   sync.Mutex
	path string
}

// newTagStyleStore keeps the file beside settings.json; nil without a data dir.
func newTagStyleStore(cfg Config) *tagStyleStore {
	if strings.TrimSpace(cfg.DBPath) == "" {
		return nil
	}
	return &tagStyleStore{path: filepath.Join(filepath.Dir(cfg.DBPath), tagStylesFilename)}
}

func (s *tagStyleStore) load() (map[string]tagStyle, error) {
	if s == nil {
		return map[string]tagStyle{}, nil
	}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]tagStyle{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read tag styles: %w", err)
	}
	var file tagStylesFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	if file.Tags == nil {
		file.Tags = map[string]tagStyle{}
	}
	return file.Tags, nil
}

// update applies change and writes the result back atomically.
func (s *tagStyleStore) update(change func(map[string]tagStyle)) error {
	if s == nil {
		return errors.New("no data directory for tag styles")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	styles, err := s.load()
	if err != nil {
		return err
	}
	change(styles)
	for id, style := range styles {
		if style == (tagStyle{}) {
			delete(styles, id)
		}
	}
	data, err := json.MarshalIndent(tagStylesFile{Version: 1, Tags: styles}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode tag styles: %w", err)
	}
	if err := writeFileAtomic(s.path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write tag styles: %w", err)
	}
	return nil
}

// withStyles fills each tag's colour, icon and last change. Presentation never
// costs the answer: an unreadable file is logged and the tags go out plain.
func (s *annotationService) withStyles(tags []tagVocabularyEntry) {
	styles, err := s.styles.load()
	if err != nil {
		s.logf("annotations: %v — tags are served without their colours", err)
		return
	}
	for i := range tags {
		style := styles[tags[i].TagID]
		tags[i].Color, tags[i].Icon = style.Color, style.Icon
		tags[i].ChangedBy, tags[i].ChangedAtUTC = style.UpdatedBy, style.UpdatedAtUTC
	}
}
