package portable

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The annotations member rides through decode and encode untouched, whatever it
// holds. That is what lets every metadata rewrite that is not about marks —
// retag, summarize, a republish — keep them: each one decodes the manifest,
// changes something else, and encodes it again. A member this build cannot read
// must survive that too, or an older CLI would quietly strip a newer file's
// marks the first time it touched the file for any other reason.

func TestAnnotationsSurviveDecodeAndEncode(t *testing.T) {
	v1, err := json.Marshal(validAnnotations())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		member string
	}{
		{"a v1 document", string(v1)},
		{"a format this reader does not know", `{"format":"cassini.annotations.v9","marks":{"nested":[1,2,3]},"note":"<kept>"}`},
		{"a v1 document this reader cannot parse", `{"format":"cassini.annotations.v1","revision":"four"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := publishedManifestFixture(Meeting{Title: "Marked"})
			manifest.Annotations = json.RawMessage(tc.member)

			decoded, err := DecodePublishedManifest(encodePublishedFixture(t, manifest).Main.JSON)
			if err != nil {
				t.Fatalf("a recording must open whatever its annotations member holds: %v", err)
			}
			requireSameJSON(t, "after one decode", decoded.Annotations, tc.member)

			decoded.Meeting.Title = "Retitled"
			again, err := DecodePublishedManifest(encodePublishedFixture(t, decoded).Main.JSON)
			if err != nil {
				t.Fatalf("decode the rewritten manifest: %v", err)
			}
			if again.Meeting.Title != "Retitled" {
				t.Fatalf("the rewrite did not take: title=%q", again.Meeting.Title)
			}
			requireSameJSON(t, "after a rewrite of something else", again.Annotations, tc.member)
		})
	}
}

func TestAnnotationsParseBackToTheDocumentWritten(t *testing.T) {
	want := validAnnotations()
	member, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	manifest := publishedManifestFixture(Meeting{Title: "Marked"})
	manifest.Annotations = member

	decoded, err := DecodePublishedManifest(encodePublishedFixture(t, manifest).Main.JSON)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, err := ParseAnnotations(decoded.Annotations)
	if err != nil {
		t.Fatalf("parse the decoded member: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the document changed on the way through:\n got %+v\nwant %+v", got, want)
	}
}

func TestAbsentAnnotationsStayAbsent(t *testing.T) {
	encoded := encodePublishedFixture(t, publishedManifestFixture(Meeting{Title: "Unmarked"}))
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded.Main.JSON, &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["annotations"]; ok {
		t.Fatalf("a manifest with no marks grew an annotations member: %s", wire["annotations"])
	}
	decoded, err := DecodePublishedManifest(encoded.Main.JSON)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Annotations) != 0 {
		t.Fatalf("decoded annotations = %s, want none", decoded.Annotations)
	}
	if got, err := ParseAnnotations(decoded.Annotations); got != nil || err != nil {
		t.Fatalf("no member must read as no marks, got (%v, %v)", got, err)
	}
}

// requireSameJSON compares by meaning rather than by bytes. The encoder
// compacts a raw member and escapes HTML-significant characters in it, neither
// of which changes what the member says.
func requireSameJSON(t *testing.T, when string, got json.RawMessage, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("%s: the member is no longer JSON: %v (%s)", when, err, got)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("%s: the annotations member changed:\n got %s\nwant %s", when, got, want)
	}
}
