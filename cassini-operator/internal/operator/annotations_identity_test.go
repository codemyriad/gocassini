package operator

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// D-773: imported documents may carry different namespaces and case variants
// of a label. Selecting an existing ID must still add that specific tag.
func TestAnnotationD773KeepsMultipleTagsAcrossNamespaces(t *testing.T) {
	for _, byID := range []bool{true, false} {
		t.Run(map[bool]string{true: "id", false: "label"}[byID], func(t *testing.T) {
			nc, service, handler, store := asyncFixture(t)
			first := annotatedFile(t, "", testTagNamespaceA, []testTag{{"tag_Test", "Test"}}, meetingMark("original", "tag_Test"))
			raw, _ := json.Marshal(first)
			nc.seed(annTestRecording, string(raw), recordingACLRules(nil, false))
			recordMarks(t, store, "MEETING1.opus", first)
			recordMarks(t, store, "SECRET.opus", annotatedFile(t, "", testTagNamespaceB, []testTag{{"tag_test", "test"}}, meetingMark("other", "tag_test")))
			nc.frontMu.Lock()
			nc.visible["SECRET.opus"] = true
			nc.frontMu.Unlock()
			tag := `{"label":"test"}`
			if byID {
				tag = `{"id":"tag_test","label":"test"}`
			}
			result := postAsync(t, handler, `{"ops":[{"op":"mark","tag":`+tag+`,"target":{"kind":"meeting"}}]}`)
			var doc projectedDocument
			if err := json.Unmarshal(result.Annotations, &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.Tags) != 2 || len(doc.Items) != 2 {
				t.Fatalf("second tag lost: %s", result.Annotations)
			}
			if err := service.syncAnnotation(context.Background(), "MEETING1.opus"); err != nil {
				t.Fatal(err)
			}
			var archived annotateResult
			json.Unmarshal([]byte(nc.recording(annTestRecording)), &archived)
			if !sameAnnotationDocument(archived.Annotations, result.Annotations) {
				t.Fatal("archive lost a tag")
			}
			response := tagCall(handler, http.MethodGet, "", "alice", "")
			var vocabulary tagVocabularyResponse
			if response.Code != 200 {
				t.Fatalf("vocabulary: %d %s", response.Code, response.Body.String())
			}
			json.Unmarshal(response.Body.Bytes(), &vocabulary)
			if len(vocabulary.Tags) != 2 {
				t.Fatalf("duplicate ID across namespaces: %+v", vocabulary.Tags)
			}
			for _, row := range vocabulary.Meetings {
				if row.MeetingID == "MEETING1" && len(row.Tags) != 2 {
					t.Fatalf("readback lost tags: %+v", row)
				}
			}
			for _, tag := range vocabulary.Tags {
				if tag.TagID == "tag_test" && (tag.Meetings != 2 || tag.Marks != 2) {
					t.Fatalf("wrong counts: %+v", tag)
				}
			}
			hidden, err := store.Vocabulary(context.Background(), []string{"MEETING1.opus"})
			if err != nil {
				t.Fatal(err)
			}
			for _, tag := range hidden {
				if tag.Meetings != 1 {
					t.Fatalf("hidden meeting counted: %+v", tag)
				}
			}
		})
	}
}
