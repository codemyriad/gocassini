package operator

import (
	"context"
	"encoding/json"
	"testing"
)

// The set-title op (D-862): committed through the same durable path as marks,
// projected so catalog resolution can overlay it, and synced into the archive
// copy by the same write-behind.

func titledFile(t *testing.T, container, namespace, title string) annotateResult {
	t.Helper()
	result := annotatedFile(t, container, namespace, []testTag{{id: "tag_a", label: "hiring"}}, meetingMark("mk_1", "tag_a"))
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(result.Annotations, &doc); err != nil {
		t.Fatal(err)
	}
	meeting, err := json.Marshal(map[string]any{
		"title": title, "setAtUtc": "2026-10-20T09:12:04Z",
		"actor": map[string]string{"kind": "person", "id": "alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc["meeting"] = meeting
	if result.Annotations, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestProjectAnnotationsReadsMeetingTitle(t *testing.T) {
	titled := titledFile(t, "cc", testTagNamespaceA, "Q4 hiring sync")
	projected := projectAnnotations(titled.Annotations, titled.Resolved, testAudioDigest)
	if projected.title != "Q4 hiring sync" {
		t.Fatalf("title = %q, want the document's meeting title", projected.title)
	}
	plain := annotatedFile(t, "dd", testTagNamespaceA, []testTag{{id: "tag_a", label: "hiring"}}, meetingMark("mk_1", "tag_a"))
	if projected := projectAnnotations(plain.Annotations, plain.Resolved, testAudioDigest); projected.title != "" {
		t.Fatalf("title = %q, want empty for an untitled document", projected.title)
	}
}

func TestAnnotationStoreTitles(t *testing.T) {
	store := newTestAnnotationStore(t)
	recordMarks(t, store, "TITLED.opus", titledFile(t, "cc", testTagNamespaceA, "Q4 hiring sync"))
	recordMarks(t, store, "PLAIN.opus", annotatedFile(t, "dd", testTagNamespaceA, []testTag{{id: "tag_a", label: "hiring"}}, meetingMark("mk_1", "tag_a")))

	titles, err := store.Titles(context.Background(), []string{"TITLED.opus", "PLAIN.opus", "ABSENT.opus"})
	if err != nil {
		t.Fatalf("Titles() error = %v", err)
	}
	if len(titles) != 1 || titles["TITLED.opus"] != "Q4 hiring sync" {
		t.Fatalf("Titles() = %v, want only the renamed meeting", titles)
	}
	if titles, err := store.Titles(context.Background(), nil); err != nil || len(titles) != 0 {
		t.Fatalf("Titles(nil) = %v, %v; want empty", titles, err)
	}
}

func TestOverlayEntryTitle(t *testing.T) {
	entry := json.RawMessage(`{"id":"MEETING1","title":"Chris","roomName":"Chris","dateLabel":"2026-10-20"}`)
	overlaid, err := overlayEntryTitle(entry, "Q4 hiring sync")
	if err != nil {
		t.Fatalf("overlayEntryTitle() error = %v", err)
	}
	var fields struct {
		ID, Title, RoomName, DateLabel string
	}
	if err := json.Unmarshal(overlaid, &fields); err != nil {
		t.Fatal(err)
	}
	if fields.Title != "Q4 hiring sync" || fields.ID != "MEETING1" || fields.RoomName != "Chris" || fields.DateLabel != "2026-10-20" {
		t.Fatalf("overlaid = %+v; only the title may change", fields)
	}
}

func TestAnnotationSetTitleCommitsProjectsAndSyncs(t *testing.T) {
	nc, s, h, store := asyncFixture(t)

	first := postAsync(t, h, `{"ops":[{"op":"set-title","title":"Q4 hiring sync"}]}`)
	var doc struct {
		Meeting *struct {
			Title string `json:"title"`
			Actor struct{ Kind, ID string }
		} `json:"meeting"`
	}
	if err := json.Unmarshal(first.Annotations, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Meeting == nil || doc.Meeting.Title != "Q4 hiring sync" {
		t.Fatalf("committed document = %s, want a meeting title", first.Annotations)
	}
	if doc.Meeting.Actor.ID != "alice" || doc.Meeting.Actor.Kind != "person" {
		t.Fatalf("actor = %+v, want the authenticated caller", doc.Meeting.Actor)
	}

	// The projection is current the moment the write commits, so catalog
	// resolution can overlay the rename before the write-behind lands.
	titles, err := store.Titles(context.Background(), []string{"MEETING1.opus"})
	if err != nil || titles["MEETING1.opus"] != "Q4 hiring sync" {
		t.Fatalf("Titles() = %v, %v; want the committed rename", titles, err)
	}

	// Setting the same title again neither bumps the revision nor re-stamps.
	repeat := postAsync(t, h, `{"ops":[{"op":"set-title","title":"Q4 hiring sync"}]}`)
	if repeat.Revision != first.Revision {
		t.Fatalf("revision %d -> %d; an identical title must be a no-op", first.Revision, repeat.Revision)
	}

	// The write-behind lands the rename in the archive copy.
	if err := s.syncAnnotation(context.Background(), "MEETING1.opus"); err != nil {
		t.Fatal(err)
	}
	var archived annotateResult
	if err := json.Unmarshal([]byte(nc.recording(annTestRecording)), &archived); err != nil {
		t.Fatal(err)
	}
	if !sameAnnotationDocument(archived.Annotations, first.Annotations) {
		t.Fatalf("archived document = %s, want the renamed one", archived.Annotations)
	}

	// Clearing restores the default everywhere.
	cleared := postAsync(t, h, `{"ops":[{"op":"set-title","title":""}]}`)
	if cleared.Revision <= first.Revision {
		t.Fatalf("clear kept revision %d", cleared.Revision)
	}
	titles, err = store.Titles(context.Background(), []string{"MEETING1.opus"})
	if err != nil || len(titles) != 0 {
		t.Fatalf("Titles() after clear = %v, %v; want empty", titles, err)
	}
}
