package transcribe

import (
	"reflect"
	"strings"
	"testing"
)

func editsBase() ([]Segment, []RosterEntry, map[string]SpeakerTurnSet) {
	base := MergeAndSortSegments([][]Segment{
		{{SpeakerID: "room", Words: []Word{
			{Text: "hello", StartMS: 0, EndMS: 400},
			{Text: "there", StartMS: 500, EndMS: 900},
			{Text: "yes", StartMS: 3000, EndMS: 3300},
			{Text: "ok", StartMS: 6000, EndMS: 6200},
		}}},
		{{SpeakerID: "ben", Words: []Word{{Text: "hi", StartMS: 1500, EndMS: 1800}}}},
	})
	roster := []RosterEntry{{ID: "room", Label: "Meeting room"}, {ID: "ben", Label: "Ben"}}
	sets := map[string]SpeakerTurnSet{"room": {
		Format: SpeakerTurnsFormat, SpeakerID: "room",
		Turns: []SpeakerTurnEntry{{0, 1000, 3}, {2900, 3400, 0}, {5900, 6300, 7}},
	}}
	return base, roster, sets
}

func edits(splits []string, merges []SpeakerMerge, labels ...SpeakerLabel) SpeakerEdits {
	doc := SpeakerEdits{Format: SpeakerEditsFormat, Revision: 1, Merges: merges, Labels: labels}
	for _, s := range splits {
		doc.Splits = append(doc.Splits, SpeakerSplit{SpeakerID: s})
	}
	return doc
}

func rosterLabels(r []RosterEntry) []string {
	var out []string
	for _, e := range r {
		out = append(out, e.ID+"="+e.Label)
	}
	return out
}

func TestApplySpeakerEditsEmptyDocumentReturnsTheBase(t *testing.T) {
	base, roster, sets := editsBase()
	got, gotRoster, _, err := ApplySpeakerEdits(base, roster, edits(nil, nil), sets)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, base) || !reflect.DeepEqual(gotRoster, roster) {
		t.Fatalf("empty edits changed the transcript:\n%+v\n%+v", got, gotRoster)
	}
}

func TestApplySpeakerEditsSplitsMergesAndNames(t *testing.T) {
	base, roster, sets := editsBase()
	doc := edits([]string{"room"},
		[]SpeakerMerge{{From: "room~3", Into: "room~1"}},
		SpeakerLabel{SpeakerID: "room~1", Label: "Mira"},
		SpeakerLabel{SpeakerID: "room~9", Label: "Nobody yet"},
	)
	got, gotRoster, report, err := ApplySpeakerEdits(base, roster, doc, sets)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"room~1=Mira", "ben=Ben", "room~2=Meeting room · Speaker 2"}
	if !reflect.DeepEqual(rosterLabels(gotRoster), want) {
		t.Fatalf("roster %v want %v", rosterLabels(gotRoster), want)
	}
	var speakers []string
	for _, s := range got {
		speakers = append(speakers, s.SpeakerID+":"+s.Text)
	}
	wantSegs := []string{"room~1:hello there", "ben:hi", "room~2:yes", "room~1:ok"}
	if !reflect.DeepEqual(speakers, wantSegs) {
		t.Fatalf("segments %v want %v", speakers, wantSegs)
	}
	if report.Merged != 1 || len(report.Splits) != 1 || len(report.Missing) != 0 {
		t.Fatalf("report %+v", report)
	}
}

func TestApplySpeakerEditsUndoAndRedoRestoreNames(t *testing.T) {
	base, roster, sets := editsBase()
	named := SpeakerLabel{SpeakerID: "room~2", Label: "Leo"}
	split := edits([]string{"room"}, nil, named)
	_, first, _, err := ApplySpeakerEdits(base, roster, split, sets)
	if err != nil {
		t.Fatal(err)
	}
	// Undoing the split keeps the label dormant...
	undone, undoneRoster, _, err := ApplySpeakerEdits(base, roster, edits(nil, nil, named), sets)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(undone, base) || !reflect.DeepEqual(undoneRoster, roster) {
		t.Fatal("undoing the split must restore the original transcript")
	}
	// ...and splitting again brings the same voice back under the same name.
	_, again, _, err := ApplySpeakerEdits(base, roster, split, sets)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, again) {
		t.Fatalf("redo changed names: %v vs %v", rosterLabels(first), rosterLabels(again))
	}
}

func TestApplySpeakerEditsReportsSplitsWithoutTurns(t *testing.T) {
	base, roster, _ := editsBase()
	got, _, report, err := ApplySpeakerEdits(base, roster, edits([]string{"room"}, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report.Missing, []string{"room"}) || !reflect.DeepEqual(got, base) {
		t.Fatalf("missing turns: %+v", report)
	}
}

func TestParseSpeakerEditsIsStrict(t *testing.T) {
	cases := map[string]string{
		"unknown field":   `{"format":"cassini.speaker-edits.v1","revision":1,"splits":[],"merges":[],"labels":[],"extra":1}`,
		"wrong format":    `{"format":"x","revision":1}`,
		"split a voice":   `{"format":"cassini.speaker-edits.v1","revision":1,"splits":[{"speakerId":"a~1"}]}`,
		"merge a device":  `{"format":"cassini.speaker-edits.v1","revision":1,"merges":[{"from":"a","into":"b"}]}`,
		"cross-device":    `{"format":"cassini.speaker-edits.v1","revision":1,"merges":[{"from":"a~1","into":"b~1"}]}`,
		"into a device":   `{"format":"cassini.speaker-edits.v1","revision":1,"merges":[{"from":"a~1","into":"b"}]}`,
		"chained merge":   `{"format":"cassini.speaker-edits.v1","revision":1,"merges":[{"from":"a~1","into":"a~2"},{"from":"a~2","into":"a~3"}]}`,
		"long label":      `{"format":"cassini.speaker-edits.v1","revision":1,"labels":[{"speakerId":"a~1","label":"` + strings.Repeat("x", 65) + `"}]}`,
		"padded label":    `{"format":"cassini.speaker-edits.v1","revision":1,"labels":[{"speakerId":"a~1","label":" Mira"}]}`,
		"duplicate label": `{"format":"cassini.speaker-edits.v1","revision":1,"labels":[{"speakerId":"a~1","label":"A"},{"speakerId":"a~1","label":"B"}]}`,
	}
	for name, doc := range cases {
		if _, err := ParseSpeakerEdits([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ok := `{"format":"cassini.speaker-edits.v1","revision":2,"splits":[{"speakerId":"a"}],"merges":[{"from":"a~3","into":"a~1"}],"labels":[{"speakerId":"a~1","label":"Mira"}]}`
	if _, err := ParseSpeakerEdits([]byte(ok)); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
}

func TestApplySpeakerEditsLeavesADeviceWholeWhenOnlyOneVoiceIsFound(t *testing.T) {
	base, roster, sets := editsBase()
	one := sets["room"]
	one.Turns = []SpeakerTurnEntry{{0, 7000, 0}}
	got, gotRoster, report, err := ApplySpeakerEdits(base, roster, edits([]string{"room"}, nil), map[string]SpeakerTurnSet{"room": one})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report.Inconclusive, []string{"room"}) {
		t.Fatalf("report %+v", report)
	}
	if !reflect.DeepEqual(got, base) || !reflect.DeepEqual(gotRoster, roster) {
		t.Fatal("an inconclusive split must leave the transcript as it was")
	}
}
