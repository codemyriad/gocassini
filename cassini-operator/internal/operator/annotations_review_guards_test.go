package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// allRecorded is every meeting the store holds — the visible set of a caller who
// may read the whole archive, for tests written before lookups took one.
func allRecorded(t *testing.T, store *annotationStore) []string {
	t.Helper()
	rows, err := store.db.Query(`SELECT opus_name FROM meeting_annotations`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

const guardTestAudio = "8e1f7499c6d5fba88c3bd9b69ecd3de1b07ae0cff65152c942c5e99062d01cbc"

func guardTestDoc(revision int, tagID, label string) annotateResult {
	doc := fmt.Sprintf(`{"format":"cassini.annotations.v1","revision":%d,"audioOpusSha256":%q,`+
		`"tagNamespace":"urn:uuid:07e4eab6-4f5f-4cc4-8913-46432c5cd726","tags":[{"id":%q,"label":%q}],`+
		`"items":[{"id":"mk_%s","tagId":%q,"target":{"kind":"meeting"},"createdAtUtc":"2026-09-10T00:00:00Z",`+
		`"actor":{"kind":"person","id":"alice"},"operationId":"op_1"}]}`,
		revision, guardTestAudio, tagID, label, tagID, tagID)
	resolved := true
	return annotateResult{Format: annotateResultFormat, Annotations: json.RawMessage(doc), Revision: revision,
		Resolved: &resolved, AudioOpusSHA256: guardTestAudio, ContainerSHA256: fmt.Sprintf("c%d", revision)}
}

func TestRecordKeepsTheNewerRevision(t *testing.T) {
	ctx := context.Background()
	store := openTestAnnotationStore(t)
	mine := []string{"M.opus"}
	if err := store.Record(ctx, "M.opus", guardTestDoc(6, "tag_new", "newer")); err != nil {
		t.Fatal(err)
	}
	// A slower write of revision 5 lands after 6.
	if err := store.Record(ctx, "M.opus", guardTestDoc(5, "tag_old", "older")); err != nil {
		t.Fatal(err)
	}
	if id, ok, _ := store.ResolveLabel(ctx, "newer", mine); !ok || id != "tag_new" {
		t.Fatal("a slower write of an older revision replaced the newer rows")
	}
	if _, ok, _ := store.ResolveLabel(ctx, "older", mine); ok {
		t.Fatal("the older revision's tag must not have been recorded")
	}
	// The rebuild read the file itself, so it replaces whatever is recorded.
	if _, err := store.record(ctx, "M.opus", guardTestDoc(5, "tag_old", "older"), false); err != nil {
		t.Fatal(err)
	}
	if id, ok, _ := store.ResolveLabel(ctx, "older", mine); !ok || id != "tag_old" {
		t.Fatal("an authoritative rebuild must replace the recorded rows")
	}
}

func TestResolveLabelConsultsOnlyTheCallersMeetings(t *testing.T) {
	ctx := context.Background()
	store := openTestAnnotationStore(t)
	if err := store.Record(ctx, "HIDDEN.opus", guardTestDoc(1, "tag_secret", "layoffs")); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(ctx, "MINE.opus", guardTestDoc(1, "tag_mine", "budget")); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.ResolveLabel(ctx, "layoffs", []string{"MINE.opus"}); ok {
		t.Fatal("a label carried only by a meeting the caller cannot open must not resolve")
	}
	if _, ok, _ := store.ResolveLabel(ctx, "layoffs", nil); ok {
		t.Fatal("an empty visible set must resolve nothing")
	}
	if id, ok, _ := store.ResolveLabel(ctx, "layoffs", []string{"HIDDEN.opus"}); !ok || id != "tag_secret" {
		t.Fatal("a label on a meeting the caller can open must resolve")
	}
}
