package transcribe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SpeakerEditsFormat identifies the people's edits to a meeting's speakers.
const SpeakerEditsFormat = "cassini.speaker-edits.v1"

// SpeakerEdits is the desired state of a meeting's speakers, as people asked
// for it: which participants' devices were shared and must be split into
// voices, which voices are really the same person, and what to call them.
// It is applied to the original transcript every time, so applying it is
// idempotent and removing an entry undoes it exactly.
type SpeakerEdits struct {
	Format   string         `json:"format"`
	Revision int            `json:"revision"`
	Splits   []SpeakerSplit `json:"splits"`
	Merges   []SpeakerMerge `json:"merges"`
	Labels   []SpeakerLabel `json:"labels"`
}

// SpeakerSplit marks a participant's stream as shared by several people.
type SpeakerSplit struct {
	SpeakerID string `json:"speakerId"`
}

// SpeakerMerge says voice From is the same person as speaker Into.
type SpeakerMerge struct {
	From string `json:"from"`
	Into string `json:"into"`
}

// SpeakerLabel names a speaker or voice. A label for an id that is not in the
// current result is kept but has no effect, so a name survives undoing and
// redoing a split.
type SpeakerLabel struct {
	SpeakerID string `json:"speakerId"`
	Label     string `json:"label"`
}

const maxSpeakerLabelRunes = 64

// ParseSpeakerEdits decodes and validates an edits document strictly.
func ParseSpeakerEdits(data []byte) (SpeakerEdits, error) {
	var doc SpeakerEdits
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return doc, fmt.Errorf("speaker edits: %w", err)
	}
	return doc, doc.Validate()
}

// Validate checks the document's own consistency.
func (d SpeakerEdits) Validate() error {
	if d.Format != SpeakerEditsFormat {
		return fmt.Errorf("speaker edits: unsupported format %q", d.Format)
	}
	if d.Revision < 0 {
		return fmt.Errorf("speaker edits: negative revision")
	}
	seen := map[string]bool{}
	for _, s := range d.Splits {
		if s.SpeakerID == "" || IsVoiceID(s.SpeakerID) {
			return fmt.Errorf("speaker edits: cannot split %q", s.SpeakerID)
		}
		if seen[s.SpeakerID] {
			return fmt.Errorf("speaker edits: %q is split twice", s.SpeakerID)
		}
		seen[s.SpeakerID] = true
	}
	merged := map[string]bool{}
	for _, m := range d.Merges {
		// A voice can only be the same person as another voice of the same
		// device: those are the only two speakers one recording cannot tell
		// apart by microphone.
		if !IsVoiceID(m.From) || !IsVoiceID(m.Into) || m.From == m.Into || VoiceParent(m.From) != VoiceParent(m.Into) {
			return fmt.Errorf("speaker edits: invalid merge %q into %q", m.From, m.Into)
		}
		if merged[m.From] {
			return fmt.Errorf("speaker edits: %q is merged twice", m.From)
		}
		merged[m.From] = true
	}
	for _, m := range d.Merges {
		if merged[m.Into] {
			return fmt.Errorf("speaker edits: %q is merged into %q, which is itself merged", m.From, m.Into)
		}
	}
	labelled := map[string]bool{}
	for _, l := range d.Labels {
		if l.SpeakerID == "" {
			return fmt.Errorf("speaker edits: label without speaker")
		}
		if labelled[l.SpeakerID] {
			return fmt.Errorf("speaker edits: %q is labelled twice", l.SpeakerID)
		}
		labelled[l.SpeakerID] = true
		if err := ValidateSpeakerLabel(l.Label); err != nil {
			return err
		}
	}
	return nil
}

// ValidateSpeakerLabel applies the annotation label rules: 1-64 runes, no
// control characters, no leading or trailing space.
func ValidateSpeakerLabel(label string) error {
	n := utf8.RuneCountInString(label)
	if n == 0 || n > maxSpeakerLabelRunes {
		return fmt.Errorf("speaker label must be 1-%d characters", maxSpeakerLabelRunes)
	}
	if strings.TrimSpace(label) != label {
		return fmt.Errorf("speaker label must not start or end with a space")
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return fmt.Errorf("speaker label must not contain control characters")
		}
	}
	return nil
}

// IsVoiceID reports whether id names a voice split from a shared device.
func IsVoiceID(id string) bool { return strings.Contains(id, "~") }

// VoiceParent returns the participant a voice was split from, or "".
func VoiceParent(id string) string {
	if i := strings.LastIndex(id, "~"); i > 0 {
		return id[:i]
	}
	return ""
}

// voiceNumber returns n for "<parent>~n", or 0.
func voiceNumber(id string) int {
	var n int
	if i := strings.LastIndex(id, "~"); i > 0 {
		fmt.Sscanf(id[i+1:], "%d", &n)
	}
	return n
}

// DefaultVoiceLabel is the label a voice gets until someone names it.
func DefaultVoiceLabel(parentLabel string, n int) string {
	return fmt.Sprintf("%s · Speaker %d", parentLabel, n)
}

// DeviceLabelFromVoiceLabel reads the device's label back out of a voice's
// default label, or "" when the voice has been named.
func DeviceLabelFromVoiceLabel(label string) string {
	i := strings.LastIndex(label, " · Speaker ")
	if i <= 0 {
		return ""
	}
	n := label[i+len(" · Speaker "):]
	if n == "" || strings.Trim(n, "0123456789") != "" {
		return ""
	}
	return label[:i]
}

// RosterEntry is one speaker as written to transcript.words.v1.json.
type RosterEntry struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// SpeakerEditsReport says what applying an edits document did.
type SpeakerEditsReport struct {
	Splits []SplitResult
	// Missing lists splits that could not run because no turns were given.
	Missing []string
	// Inconclusive lists splits that found fewer than two voices with words.
	// The device stays one speaker: a split that separates nobody must not
	// read as a claim that several people were there.
	Inconclusive []string
	Merged       int
}

// ApplySpeakerEdits applies an edits document to the original transcript.
// baseRoster holds the original speakers and labels; turnSets holds the
// frozen diarizer output per split participant. Applying an empty document
// returns the base unchanged.
func ApplySpeakerEdits(base []Segment, baseRoster []RosterEntry, doc SpeakerEdits, turnSets map[string]SpeakerTurnSet) ([]Segment, []RosterEntry, SpeakerEditsReport, error) {
	var report SpeakerEditsReport
	if err := doc.Validate(); err != nil {
		return nil, nil, report, err
	}
	segments := base
	for _, split := range doc.Splits {
		set, ok := turnSets[split.SpeakerID]
		if !ok {
			report.Missing = append(report.Missing, split.SpeakerID)
			continue
		}
		if set.SpeakerID != split.SpeakerID {
			return nil, nil, report, fmt.Errorf("speaker edits: turns for %q given for %q", set.SpeakerID, split.SpeakerID)
		}
		next, res := SplitSpeakerSegments(segments, split.SpeakerID, set.SpeakerTurns())
		report.Splits = append(report.Splits, res)
		if len(res.SubSpeakerIDs) < 2 {
			report.Inconclusive = append(report.Inconclusive, split.SpeakerID)
			continue
		}
		segments = next
	}

	if len(doc.Merges) > 0 {
		into := map[string]string{}
		for _, m := range doc.Merges {
			into[m.From] = m.Into
		}
		present := map[string]bool{}
		for _, seg := range segments {
			present[seg.SpeakerID] = true
		}
		var perSpeaker [][]Segment
		for _, seg := range segments {
			if target, ok := into[seg.SpeakerID]; ok && present[target] {
				seg.SpeakerID = target
				report.Merged++
			}
			perSpeaker = append(perSpeaker, []Segment{seg})
		}
		if report.Merged > 0 {
			segments = MergeAndSortSegments(perSpeaker)
		}
	}

	baseLabels := map[string]string{}
	for _, r := range baseRoster {
		baseLabels[r.ID] = r.Label
	}
	overrides := map[string]string{}
	for _, l := range doc.Labels {
		overrides[l.SpeakerID] = l.Label
	}
	labelFor := func(id string) string {
		if l, ok := overrides[id]; ok {
			return l
		}
		if l, ok := baseLabels[id]; ok {
			return l
		}
		if parent := VoiceParent(id); parent != "" {
			parentLabel := baseLabels[parent]
			if parentLabel == "" {
				parentLabel = parent
			}
			return DefaultVoiceLabel(parentLabel, voiceNumber(id))
		}
		return id
	}
	// The roster keeps the original order, with each split device replaced
	// in place by its voices in voice order, so a device's voices sit
	// together for every reader of speakers[], not only the People panel.
	present := map[string]bool{}
	var firstSeen []string
	for _, seg := range segments {
		if seg.SpeakerID != "" && !present[seg.SpeakerID] {
			present[seg.SpeakerID] = true
			firstSeen = append(firstSeen, seg.SpeakerID)
		}
	}
	voicesOf := map[string][]string{}
	for _, id := range firstSeen {
		if parent := VoiceParent(id); parent != "" {
			voicesOf[parent] = append(voicesOf[parent], id)
		}
	}
	for _, ids := range voicesOf {
		sort.Slice(ids, func(i, j int) bool { return voiceNumber(ids[i]) < voiceNumber(ids[j]) })
	}
	var roster []RosterEntry
	listed := map[string]bool{}
	add := func(id string) {
		if !listed[id] {
			listed[id] = true
			roster = append(roster, RosterEntry{ID: id, Label: labelFor(id)})
		}
	}
	for _, r := range baseRoster {
		if present[r.ID] {
			add(r.ID)
		}
		for _, voice := range voicesOf[r.ID] {
			add(voice)
		}
	}
	for _, id := range firstSeen {
		add(id)
	}
	return segments, roster, report, nil
}
