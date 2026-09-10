package operator

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
)

func TestAnnotationsMeetingPOSTSkipsTheUploadWhenNothingChanged(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	sum := sha256.Sum256([]byte("OPUS-original"))
	// What the real CLI answers for a batch that changed nothing: the input's
	// own digest as the container's.
	unchanged := `{"format":"cassini.annotate.result.v1","annotations":null,"revision":1,"operationId":"op_1",` +
		`"added":[],"removed":[],"notFound":["mk_gone"],"resolved":true,"audioOpusSha256":"aa",` +
		`"containerSha256":"` + hex.EncodeToString(sum[:]) + `"}`
	bin := fakeCassini(t, annTestCLIPrints(unchanged))
	h, _ := annTestService(t, nc.url, bin, nil)

	rec := annTestCall(h, http.MethodPost, "MEETING1", "alice", `{"ops":[{"op":"unmark","itemId":"mk_gone"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := nc.ifMatches(); len(got) != 0 {
		t.Fatalf("a batch that changed nothing must not upload; saw PUTs with If-Match %v", got)
	}
	if got := nc.recording(annTestRecording); got != "OPUS-original" {
		t.Fatalf("the recording must be untouched, holds %q", got)
	}
}

func TestTagVocabularySharesSearchsRateLimit(t *testing.T) {
	s := tagService(untouchedUpstream(t).URL, nil)
	limiter := newSearchRateLimiter()
	for i := 0; i < 1000; i++ {
		if ok, _ := limiter.allow("alice"); !ok {
			break
		}
	}
	s.rt.searchLimiter = limiter

	rec := getTags(t, s, "alice")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a caller who spent the budget on searches must be refused here too; code = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("a refusal must say when to retry")
	}
}
