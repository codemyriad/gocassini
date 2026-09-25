package operator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Retention dates deliberately have no duration-in-seconds representation.
type retentionPolicy struct {
	Forever bool   `json:"forever"`
	Count   int    `json:"count,omitempty"`
	Unit    string `json:"unit,omitempty"`
}
type retentionGroup struct {
	FineInitialized bool                       `json:"fine_initialized"`
	Mode            string                     `json:"mode"`
	Policy          retentionPolicy            `json:"policy"`
	Fine            map[string]retentionPolicy `json:"fine"`
}
type retentionSettings struct {
	Version    int             `json:"version"`
	Revision   int             `json:"revision"`
	Recordings retentionPolicy `json:"recordings"`
	History    retentionGroup  `json:"history"`
	Current    retentionPolicy `json:"current"`
	Logs       retentionPolicy `json:"logs"`
}

var historyKinds = []string{"failed_capture", "failed_build", "superseded", "failed_publish"}

func defaultRetentionSettings() retentionSettings {
	group := func(keys []string) retentionGroup {
		g := retentionGroup{Mode: "group", Policy: retentionPolicy{Forever: true}, Fine: map[string]retentionPolicy{}}
		for _, k := range keys {
			g.Fine[k] = retentionPolicy{Forever: true}
		}
		return g
	}
	return retentionSettings{Version: 2, Recordings: retentionPolicy{Forever: true}, History: group(historyKinds), Current: retentionPolicy{Forever: true}, Logs: retentionPolicy{Forever: true}}
}
func (g retentionGroup) policyFor(kind string) retentionPolicy {
	if g.Mode == "fine" {
		return g.Fine[kind]
	}
	return g.Policy
}
func (p retentionPolicy) validate() error {
	if p.Forever {
		if p.Count != 0 || p.Unit != "" {
			return errors.New("keep forever cannot include a count or unit")
		}
		return nil
	}
	if p.Count < 1 || p.Count > 9999 {
		return errors.New("retention count must be an integer from 1 to 9999")
	}
	if p.Unit != "days" && p.Unit != "weeks" && p.Unit != "months" {
		return errors.New("retention unit must be days, weeks or months")
	}
	return nil
}
func utcDate(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
func (p retentionPolicy) deadline(anchor time.Time) time.Time {
	if p.Forever || anchor.IsZero() {
		return time.Time{}
	}
	d := utcDate(anchor)
	switch p.Unit {
	case "days":
		return d.AddDate(0, 0, p.Count)
	case "weeks":
		return d.AddDate(0, 0, p.Count*7)
	case "months":
		first := time.Date(d.Year(), d.Month()+time.Month(p.Count), 1, 0, 0, 0, 0, time.UTC)
		last := first.AddDate(0, 1, -1).Day()
		day := d.Day()
		if day > last {
			day = last
		}
		return first.AddDate(0, 0, day-1)
	}
	return time.Time{}
}
func (p retentionPolicy) due(anchor, now time.Time) bool {
	d := p.deadline(anchor)
	return !d.IsZero() && !utcDate(now).Before(d)
}
func (s retentionSettings) validate() error {
	if s.Version != 2 || s.Revision < 0 {
		return errors.New("unsupported retention settings version or revision")
	}
	if err := s.Recordings.validate(); err != nil {
		return fmt.Errorf("recordings: %w", err)
	}
	g := s.History
	keys := historyKinds
	if g.Mode != "group" && g.Mode != "fine" {
		return errors.New("policy mode must be group or fine")
	}
	if err := g.Policy.validate(); err != nil {
		return err
	}
	if len(g.Fine) != len(keys) {
		return errors.New("all fine-grained policies must be supplied")
	}
	for _, k := range keys {
		p, ok := g.Fine[k]
		if !ok {
			return fmt.Errorf("missing %s policy", k)
		}
		if err := p.validate(); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	if err := s.Current.validate(); err != nil {
		return err
	}
	if err := s.Logs.validate(); err != nil {
		return err
	}

	return nil
}

// The mutex also linearizes policy activation against the worker's deletion start.
type retentionConfig struct {
	mu       sync.Mutex
	path     string
	settings retentionSettings
	loadErr  error
}

func newRetentionConfig(path string) *retentionConfig {
	c := &retentionConfig{path: path, settings: defaultRetentionSettings()}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return c
	}
	if err != nil {
		c.loadErr = err
		return c
	}
	defer f.Close()
	// Version 1's audio policy already controlled whole-bundle deletion.
	// Preserve that deadline when upgrading an existing settings file.
	var stored struct {
		Version    int             `json:"version"`
		Revision   int             `json:"revision"`
		Recordings json.RawMessage `json:"recordings"`
		History    retentionGroup  `json:"history"`
		Current    retentionPolicy `json:"current"`
		Logs       retentionPolicy `json:"logs"`
	}
	dec := json.NewDecoder(io.LimitReader(f, 65537))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&stored); err == nil {
		var extra any
		if dec.Decode(&extra) != io.EOF {
			err = errors.New("trailing retention settings data")
		}
	}
	s := retentionSettings{Version: stored.Version, Revision: stored.Revision, History: stored.History, Current: stored.Current, Logs: stored.Logs}
	if err == nil {
		recordingDecoder := json.NewDecoder(bytes.NewReader(stored.Recordings))
		recordingDecoder.DisallowUnknownFields()
		if stored.Version == 1 {
			var legacy retentionGroup
			err = recordingDecoder.Decode(&legacy)
			if err == nil {
				switch legacy.Mode {
				case "group":
					s.Recordings = legacy.Policy
				case "fine":
					s.Recordings = legacy.Fine["audio"]
				default:
					err = errors.New("unknown legacy recordings policy mode")
				}
				s.Version = 2
			}
		} else {
			err = recordingDecoder.Decode(&s.Recordings)
		}
	}

	if err == nil {
		err = s.validate()
	}
	if err != nil {
		c.loadErr = err
		return c
	}
	c.settings = s
	return c
}
func (c *retentionConfig) save(s retentionSettings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(c.path), ".retention-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), c.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(c.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (rt *Runtime) retentionHandler(w http.ResponseWriter, r *http.Request) {
	c := rt.retention
	if c == nil {
		writeJSONError(w, 503, "retention configuration unavailable")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loadErr != nil {
		writeJSONError(w, 503, "Retention is disabled: repair retention_settings.json and restart: "+c.loadErr.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPut:
		var s retentionSettings
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			writeJSONError(w, 400, "invalid retention settings")
			return
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			writeJSONError(w, 400, "trailing retention settings data")
			return
		}
		if r.Header.Get("If-Match") != fmt.Sprintf("\"%d\"", c.settings.Revision) || s.Revision != c.settings.Revision {
			writeJSONError(w, 412, "Settings changed; reload before saving")
			return
		}
		if err := s.validate(); err != nil {
			writeJSONError(w, 400, err.Error())
			return
		}
		s.Revision++
		if err := c.save(s); err != nil {
			writeJSONError(w, 500, "Could not persist retention settings")
			return
		}
		c.settings = s
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSONError(w, 405, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", c.settings.Revision))
	_ = json.NewEncoder(w).Encode(c.settings)
}
