package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Querying the index (D-623).
//
// THE VISIBLE SET IS AN ARGUMENT, AND IT IS BOUND INTO THE STATEMENT
//
// Every query takes the caller's readable `.opus` names and joins them INSIDE
// the SQL, so `LIMIT` applies to the already-filtered stream. Filtering after
// the query instead has no correct limit: with the caller's meetings scattered
// through the corpus, collecting twenty visible hits can mean scanning
// thousands of rows, and any fixed limit in the SQL silently truncates first —
// so a caller who may read two meetings of a thousand is told nothing matched
// while holding meetings they can play right now. That is the forbidden
// failure arriving through the normal path, not an error path.
//
// Binding it also makes the empty case safe by construction:
// `json_each('[]')` yields no rows, and an inner join against no rows yields no
// rows. Delete the WHERE clause entirely and a caller with an empty visible set
// still gets nothing. A subtractive filter would fail OPEN in the same
// situation.
//
// RANKING
//
// Ordered by relevance, and the score is never returned.
//
// The residual, stated rather than buried: FTS5 computes bm25 over the WHOLE
// index, including meetings this caller cannot see, so the ORDER of their own
// hits carries a weak statistical signal about hidden content. Binding the
// visible set fixes membership leakage, not scoring leakage. Suppressing the
// score shrinks the channel to an ordering; only a per-caller index would close
// it, which is not worth it. Time-ordering would also close it and was
// considered — relevance was chosen deliberately, because a search that ranks
// badly is a search nobody uses.
//
// `ORDER BY rank`, not `ORDER BY bm25(segment_fts)`: the magic column uses
// FTS5's own sorter, while the function call builds a temp B-tree over every
// global hit before sorting.

// searchRequest is one query. Visible is the caller's readable `.opus`
// basenames, resolved per request and never cached here.
type searchRequest struct {
	Text      string
	Visible   []string
	SpeakerID string
	Limit     int
	// UseAliases expands each query word into the mistranscriptions the speech
	// recogniser produces for it. On by default at the caller.
	UseAliases bool
	// AliasIndex is the expansion table, resolved per request from the shipped
	// groups merged with the operator's own. Passed in rather than read from a
	// package variable so an operator's edit takes effect on the next search
	// rather than the next restart.
	AliasIndex map[string][]string
}

// searchHit is a reference, never content. There is no text field, so a bug in
// a handler cannot turn this into a disclosure of what was said.
type searchHit struct {
	// rowID is the index row this hit came from. Internal: it is how the
	// exact-vs-alias label is resolved on precisely these rows rather than by a
	// second ranked query that might not contain them.
	rowID     int64  `json:"-"`
	OpusName  string `json:"-"`
	SegmentID string `json:"segmentId"`
	StartMS   int64  `json:"startMs"`
	EndMS     int64  `json:"endMs"`
	SpeakerID string `json:"speakerId,omitempty"`
	// Matched says WHY this is a hit: the caller's own words, or only a known
	// mistranscription of them. A reader deserves to know the difference
	// between finding "cassini" and finding "casino".
	Matched string `json:"matched"`
}

const (
	searchMatchedExact = "exact"
	searchMatchedAlias = "alias"
)

// searchResults is what a query produced, plus what it actually did.
type searchResults struct {
	Hits []searchHit
	// Widened is true when requiring every query word found nothing and the
	// query was retried as "any of them". Reported rather than hidden: a
	// widened result answers a different question from the one that was asked.
	Widened bool
	// Groups is what was searched for, after alias expansion — so a caller can
	// see that "cassini" also looked for "casino" rather than wondering why.
	Groups [][]string
}

// errSearchNoWords is the ONE failure here that is the caller's to fix. It is a
// sentinel so the handler can answer 400 for it and 502 for everything else: a
// closed handle, a locked database or a half-rebuilt index are outages, and
// telling an agent to rewrite its query means it never retries.
var errSearchNoWords = errors.New("the query has no searchable words")

const searchDefaultLimit = 20
const searchMaxLimit = 100

// Search runs one query against the index.
//
// Two statements, not one: the caller's literal words first, then the
// alias-expanded form. Membership of the first is what distinguishes an exact
// hit from an alias hit, and a contentless index cannot be asked which term
// matched.
func (s *searchStore) Search(ctx context.Context, req searchRequest) (searchResults, error) {
	words := splitSearchWords(req.Text)
	if len(words) == 0 {
		return searchResults{}, errSearchNoWords
	}
	limit := req.Limit
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	if limit > searchMaxLimit {
		limit = searchMaxLimit
	}
	// Never nil: json_each(null) errors, while json_each('[]') is the safe empty
	// set this design depends on.
	visible, err := json.Marshal(nonNilStrings(req.Visible))
	if err != nil {
		return searchResults{}, fmt.Errorf("encode visible set: %w", err)
	}

	groups := groupQueryWords(words, req.UseAliases, req.AliasIndex)
	results := searchResults{Groups: groups}

	// Over-fetch, because word-derived rows OVERLAP: a word is written into the
	// bucket its timestamp falls in and the previous one, so a single spoken
	// word matches two rows. Collapsing afterwards would otherwise eat into the
	// caller's limit and return half as many distinct moments as asked for.
	fetch := limit*2 + 10

	// Strict first: every group must appear. A turn containing all of what was
	// asked is a better answer than one containing any of it.
	hits, err := s.match(ctx, buildMatchExpression(groups, true), visible, req.SpeakerID, fetch)
	if err != nil {
		return searchResults{}, err
	}
	if len(hits) == 0 && len(groups) > 1 {
		// Widen rather than return nothing, and say so. A question rarely has
		// one segment containing all of it.
		hits, err = s.match(ctx, buildMatchExpression(groups, false), visible, req.SpeakerID, fetch)
		if err != nil {
			return searchResults{}, err
		}
		results.Widened = len(hits) > 0
	}
	hits = collapseOverlappingHits(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	if len(hits) == 0 {
		results.Hits = []searchHit{}
		return results, nil
	}

	// Which of these would have matched without alias expansion?
	//
	// Asked of PRECISELY the rows being returned, not by running the literal
	// query again with its own ranking and limit. That earlier shape could put
	// a genuine word match outside its own top N on a busy corpus and label it
	// `alias` — and a wrong label is worse than none, because the label exists
	// to say that finding "casino" is not the same as finding "cassini".
	exact := map[int64]bool{}
	if req.UseAliases && len(hits) > 0 {
		literal := make([][]string, 0, len(words))
		for _, word := range words {
			literal = append(literal, []string{word})
		}
		rowIDs := make([]int64, 0, len(hits))
		for _, hit := range hits {
			rowIDs = append(rowIDs, hit.rowID)
		}
		matched, err := s.matchWithinRows(ctx, buildMatchExpression(literal, false), rowIDs)
		if err != nil {
			return searchResults{}, err
		}
		exact = matched
	}
	for i := range hits {
		hits[i].Matched = searchMatchedAlias
		if !req.UseAliases || exact[hits[i].rowID] {
			hits[i].Matched = searchMatchedExact
		}
	}
	results.Hits = hits
	return results, nil
}

// match runs the statement. The visible set is bound as JSON and inner-joined,
// so it bounds the result set before LIMIT rather than after.
func (s *searchStore) match(ctx context.Context, expression string, visible []byte, speakerID string, limit int) ([]searchHit, error) {
	if strings.TrimSpace(expression) == "" {
		return nil, nil
	}
	query := `
SELECT r.rowid_, r.opus_name, r.segment_id, r.start_ms, r.end_ms, r.speaker_id
  FROM segment_fts f
  JOIN segment_ref r    ON r.rowid_ = f.rowid
  JOIN json_each(?1) v  ON v.value = r.opus_name
 WHERE segment_fts MATCH ?2`
	args := []any{string(visible), expression}
	if speaker := strings.TrimSpace(speakerID); speaker != "" {
		query += ` AND r.speaker_id = ?3
 ORDER BY rank
 LIMIT ?4`
		args = append(args, speaker, limit)
	} else {
		query += `
 ORDER BY rank
 LIMIT ?3`
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	var hits []searchHit
	for rows.Next() {
		var hit searchHit
		if err := rows.Scan(&hit.rowID, &hit.OpusName, &hit.SegmentID, &hit.StartMS, &hit.EndMS, &hit.SpeakerID); err != nil {
			return nil, fmt.Errorf("scan search hit: %w", err)
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	return hits, nil
}

// buildMatchExpression renders the groups as an FTS5 expression.
//
// Server-built from tokenised input and never interpolated from raw text: every
// term is stripped to letters, digits and inner spaces, then quoted, so a
// caller cannot reach the query language itself. Quoting also stops an ordinary
// word like "AND" or "NEAR" being read as an operator.
//
// A term of five characters or more is matched as a PREFIX, which is what makes
// "record" find "recording" and "recorder" without a stemmer. Shorter terms are
// not, because a three-letter prefix matches far too much.
func buildMatchExpression(groups [][]string, requireAll bool) string {
	rendered := make([]string, 0, len(groups))
	for _, group := range groups {
		variants := make([]string, 0, len(group))
		for _, variant := range group {
			term := sanitizeSearchTerm(variant)
			if term == "" {
				continue
			}
			quoted := `"` + term + `"`
			if len([]rune(term)) >= 5 && !strings.Contains(term, " ") {
				quoted += "*"
			}
			variants = append(variants, quoted)
		}
		if len(variants) == 0 {
			continue
		}
		if len(variants) == 1 {
			rendered = append(rendered, variants[0])
			continue
		}
		rendered = append(rendered, "("+strings.Join(variants, " OR ")+")")
	}
	if len(rendered) == 0 {
		return ""
	}
	if requireAll {
		return strings.Join(rendered, " AND ")
	}
	return strings.Join(rendered, " OR ")
}

// sanitizeSearchTerm reduces a term to what may safely sit inside a quoted FTS5
// string: letters, digits, and single inner spaces for multi-word aliases.
func sanitizeSearchTerm(raw string) string {
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			lastSpace = false
		case (r == ' ' || r == '-' || r == '_') && !lastSpace:
			b.WriteRune(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// splitSearchWords tokenises the caller's query the same way terms are
// sanitised, so what is searched for is what the caller can see was searched.
func splitSearchWords(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	words := make([]string, 0, len(fields))
	for _, field := range fields {
		if term := sanitizeSearchTerm(field); term != "" {
			words = append(words, term)
		}
	}
	return words
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// collapseOverlappingHits merges hits that describe the same speech.
//
// Word-derived rows overlap by construction — a word lands in the bucket its
// timestamp falls in AND the previous one — so one spoken word matches two
// rows. Returned as-is that is two "moments" for something said once, and the
// wider row cites the start of ALL the speech it holds, which can be a stride
// earlier than the word actually matched. A contentless index cannot say which
// word matched, so the narrowest overlapping row is the tightest honest
// reference available.
//
// Grouped by meeting AND speaker, so two people talking at once stay two hits.
// Rank order is preserved: a collapsed hit keeps the position of the best-ranked
// row it came from.
//
// What survives is a RANGE, not a point. The words matched somewhere inside it
// and the index cannot say where, so a consumer must present it as a span; the
// CLI does. Reporting its start as "the time" would be false precision, and on
// a word-derived row it can be most of a window early.
func collapseOverlappingHits(hits []searchHit) []searchHit {
	kept := make([]searchHit, 0, len(hits))
	for _, hit := range hits {
		merged := false
		for i := range kept {
			if kept[i].OpusName != hit.OpusName || kept[i].SpeakerID != hit.SpeakerID {
				continue
			}
			if hit.StartMS > kept[i].EndMS || kept[i].StartMS > hit.EndMS {
				continue
			}
			// Same speech. Keep whichever localises it more tightly.
			if hit.EndMS-hit.StartMS < kept[i].EndMS-kept[i].StartMS {
				kept[i] = hit
			}
			merged = true
			break
		}
		if !merged {
			kept = append(kept, hit)
		}
	}
	return kept
}

// matchWithinRows reports which of the given rows match an expression.
//
// The rowids are bound as JSON for the same reason the visible set is: there is
// no parameter ceiling, and the join does the filtering inside the statement.
func (s *searchStore) matchWithinRows(ctx context.Context, expression string, rowIDs []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if strings.TrimSpace(expression) == "" || len(rowIDs) == 0 {
		return out, nil
	}
	encoded, err := json.Marshal(rowIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT f.rowid
  FROM segment_fts f
  JOIN json_each(?1) v ON v.value = f.rowid
 WHERE segment_fts MATCH ?2`, string(encoded), expression)
	if err != nil {
		return nil, fmt.Errorf("resolve exact matches: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan exact match: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}
