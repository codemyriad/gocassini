package operator

import (
	"net/http"
	"testing"
)

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
