package inspect

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

// Marks are optional and versioned on their own, so inspect reports them and
// never fails over them: a file whose marks it cannot use is still the good
// recording it otherwise is. These tests hold that, and hold that the one line
// inspect prints says which case the file is in.

const annotationsTestAudioSHA = "8e1f7499c6d5fba88c3bd9b69ecd3de1b07ae0cff65152c942c5e99062d01cbc"

// fixtureAnnotations returns an annotations member of the given kind:
// "resolved" (bound to audioSHA), "unresolved" (bound to other audio), or
// "unsupported" (a format this build does not read).
func fixtureAnnotations(t *testing.T, kind, audioSHA string) json.RawMessage {
	t.Helper()
	switch kind {
	case "unsupported":
		return json.RawMessage(`{"format":"cassini.annotations.v9","anything":true}`)
	case "resolved", "unresolved":
	default:
		t.Fatalf("unknown annotations fixture kind %q", kind)
	}
	binding := audioSHA
	if kind == "unresolved" {
		binding = strings.Repeat("e", 64)
	}
	start, end := int64(20), int64(120)
	doc := portable.Annotations{
		Format:          portable.AnnotationsFormatV1,
		Revision:        3,
		AudioOpusSHA256: binding,
		TagNamespace:    "urn:uuid:07e4eab6-4f5f-4cc4-8913-46432c5cd726",
		Tags:            []portable.AnnotationTag{{ID: "tag_a", Label: "budget"}, {ID: "tag_b", Label: "hiring"}},
		Items: []portable.AnnotationItem{
			{ID: "mk_1", TagID: "tag_b", Target: portable.AnnotationTarget{Kind: portable.AnnotationTargetMeeting},
				CreatedAtUTC: "2026-09-10T11:23:54Z", Actor: portable.AnnotationActor{Kind: portable.AnnotationActorPerson, ID: "alice"}, OperationID: "op_1"},
			{ID: "mk_2", TagID: "tag_a", Target: portable.AnnotationTarget{Kind: portable.AnnotationTargetTimeRange, StartMS: &start, EndMS: &end},
				CreatedAtUTC: "2026-09-10T11:24:10Z", Actor: portable.AnnotationActor{Kind: portable.AnnotationActorAgent, ID: "alice"}, OperationID: "op_2"},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal annotations fixture: %v", err)
	}
	return raw
}

func annotatedManifest(member json.RawMessage, durationMS int64) portable.Manifest {
	return portable.Manifest{
		Audio:       portable.Audio{DurationMS: durationMS},
		Integrity:   portable.Integrity{OpusSHA256: annotationsTestAudioSHA},
		Annotations: member,
	}
}

func TestPrintPortableAnnotations(t *testing.T) {
	cases := []struct {
		name       string
		member     json.RawMessage
		durationMS int64
		want       []string
		deny       []string
	}{
		{
			name: "no member prints nothing", member: nil, durationMS: 200,
			deny: []string{"annotations"},
		},
		{
			name: "a null member prints nothing", member: json.RawMessage("null"), durationMS: 200,
			deny: []string{"annotations"},
		},
		{
			name:       "resolved marks",
			member:     fixtureAnnotations(t, "resolved", annotationsTestAudioSHA),
			durationMS: 200,
			want:       []string{"annotations format=cassini.annotations.v1 revision=3 tags=2 marks=2 resolved=yes status=ok\n"},
			deny:       []string{"warning="},
		},
		{
			name:       "marks made against other audio",
			member:     fixtureAnnotations(t, "unresolved", annotationsTestAudioSHA),
			durationMS: 200,
			want: []string{
				"resolved=no status=ok\n",
				"warning=the marks were made against other audio (annotations.audioOpusSha256=" + strings.Repeat("e", 64) + ")",
			},
		},
		{
			// Their ranges were drawn on other audio, so this file's length
			// cannot make them invalid.
			name:       "unresolved marks longer than this audio",
			member:     fixtureAnnotations(t, "unresolved", annotationsTestAudioSHA),
			durationMS: 100,
			want:       []string{"resolved=no status=ok\n"},
			deny:       []string{"do not validate"},
		},
		{
			name:       "a format this build does not read",
			member:     fixtureAnnotations(t, "unsupported", annotationsTestAudioSHA),
			durationMS: 200,
			want:       []string{"annotations format=cassini.annotations.v9 status=unsupported-format\n"},
			deny:       []string{"warning=", "revision="},
		},
		{
			name:       "a v1 member that does not parse",
			member:     json.RawMessage(`{"format":"cassini.annotations.v1","revision":"four"}`),
			durationMS: 200,
			want: []string{
				"annotations format=cassini.annotations.v1 status=unreadable\n",
				"warning=the annotations could not be read, so this file shows no marks",
			},
		},
		{
			name:       "a mark past the end of the audio",
			member:     fixtureAnnotations(t, "resolved", annotationsTestAudioSHA),
			durationMS: 100,
			want: []string{
				"resolved=yes status=invalid\n",
				"warning=the annotations do not validate: annotations.items[1].target: endMs 120 is past the end of the audio (100)",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			printPortableAnnotations(&out, annotatedManifest(tc.member, tc.durationMS))
			got := out.String()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("output lacks %q:\n%s", want, got)
				}
			}
			for _, deny := range tc.deny {
				if strings.Contains(got, deny) {
					t.Errorf("output must not contain %q:\n%s", deny, got)
				}
			}
		})
	}
}

// The format string is the file's to choose, and it lands in a key=value line.
// One carrying a space or a newline must not be able to add facts to the
// report.
func TestPrintPortableAnnotationsQuotesAForgedFormat(t *testing.T) {
	forged := json.RawMessage(`{"format":"v9 resolved=yes\nportable_meeting=forged cassini=ok"}`)
	var out bytes.Buffer
	printPortableAnnotations(&out, annotatedManifest(forged, 200))
	got := out.String()
	if lines := strings.Count(got, "\n"); lines != 1 {
		t.Fatalf("a forged format produced %d lines, want 1:\n%s", lines, got)
	}
	if !strings.Contains(got, `format="v9 resolved=yes\nportable_meeting=forged cassini=ok" status=unsupported-format`) {
		t.Errorf("the forged value was not quoted:\n%s", got)
	}
}

func TestInspectPathReportsAnnotations(t *testing.T) {
	requireFFMediaTools(t)
	for _, tc := range []struct {
		kind string
		want string
	}{
		{"resolved", "annotations format=cassini.annotations.v1 revision=3 tags=2 marks=2 resolved=yes status=ok\n"},
		{"unsupported", "annotations format=cassini.annotations.v9 status=unsupported-format\n"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			path := createPortableOpusFixture(t, filepath.Join(t.TempDir(), "marked.opus"), portableFixtureOptions{
				words: []string{"Hello", "team"}, annotations: tc.kind,
			})
			var out bytes.Buffer
			if err := InspectPath(&out, path); err != nil {
				t.Fatalf("a file's annotations must never fail inspect: %v\n%s", err, out.String())
			}
			got := out.String()
			if !strings.Contains(got, tc.want) {
				t.Errorf("inspect output lacks %q:\n%s", tc.want, got)
			}
			// The recording itself is unaffected, whatever its marks are.
			if !strings.Contains(got, "cassini=ok") {
				t.Errorf("the annotations changed the recording's status:\n%s", got)
			}
		})
	}
}

// A file with no marks prints no annotations line at all, as a file with no
// origin prints no origin line: a row of dashes would read as a failed lookup.
func TestInspectPathWithoutAnnotationsPrintsNoLine(t *testing.T) {
	requireFFMediaTools(t)
	path := createPortableOpusFixture(t, filepath.Join(t.TempDir(), "unmarked.opus"), portableFixtureOptions{
		words: []string{"Hello"},
	})
	var out bytes.Buffer
	if err := InspectPath(&out, path); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if strings.Contains(out.String(), "annotations") {
		t.Errorf("an unmarked file printed an annotations line:\n%s", out.String())
	}
}
