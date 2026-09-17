package operator

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func writeLocal(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "m.opus")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConditionalPutAndLeafETag(t *testing.T) {
	nc := newFakeNCFiles()
	cfg := testExAppConfig(nc.server(t).URL)
	client := &http.Client{}
	ctx := context.Background()
	rel := "Cassini/Recordings/meetings/m.opus"

	// A conditional write to a file that is not there is refused: If-Match
	// asserts a version exists.
	if _, _, err := cfg.davPutFileIfMatch(ctx, client, ncRecordingsOwner, rel, writeLocal(t, "v0"), ncRecordingsContentType, `"7"`); !errors.Is(err, errDAVPreconditionFailed) {
		t.Fatalf("If-Match on an absent file must be refused, got %v", err)
	}

	// An unconditional PUT is unchanged in behaviour and sends no If-Match.
	if _, err := cfg.davPutFileStatus(ctx, client, ncRecordingsOwner, rel, writeLocal(t, "v1"), ncRecordingsContentType); err != nil {
		t.Fatalf("unconditional put: %v", err)
	}
	if len(nc.putIfMatch) != 1 {
		t.Fatalf("davPutFileStatus must not send If-Match; saw %v", nc.putIfMatch)
	}

	state, err := cfg.davPropfindLeafState(ctx, client, ncRecordingsOwner, rel)
	if err != nil || state.ETag != `"1"` {
		t.Fatalf("the leaf must report its ETag verbatim, got %q (%v)", state.ETag, err)
	}

	// Someone else writes in between: our ETag is now stale.
	if _, err := cfg.davPutFileStatus(ctx, client, ncRecordingsOwner, rel, writeLocal(t, "theirs"), ncRecordingsContentType); err != nil {
		t.Fatal(err)
	}
	_, _, err = cfg.davPutFileIfMatch(ctx, client, ncRecordingsOwner, rel, writeLocal(t, "ours"), ncRecordingsContentType, state.ETag)
	if !errors.Is(err, errDAVPreconditionFailed) {
		t.Fatalf("a stale ETag must be refused with errDAVPreconditionFailed, got %v", err)
	}
	if got := string(nc.files[rel]); got != "theirs" {
		t.Fatalf("a refused write must not land; the file holds %q", got)
	}

	// Re-read, retry with the current ETag, and it lands.
	fresh, err := cfg.davPropfindLeafState(ctx, client, ncRecordingsOwner, rel)
	if err != nil {
		t.Fatal(err)
	}
	_, etag, err := cfg.davPutFileIfMatch(ctx, client, ncRecordingsOwner, rel, writeLocal(t, "ours"), ncRecordingsContentType, fresh.ETag)
	if err != nil || etag != `"3"` || string(nc.files[rel]) != "ours" {
		t.Fatalf("a current ETag must land and answer the new one: etag=%q err=%v file=%q", etag, err, nc.files[rel])
	}
}
