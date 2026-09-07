package operator

import "strings"

// Aliases: what the speech recogniser wrote when someone said a name (D-623).
//
// This is not a nicety. The words people search for hardest are project and
// product names, and those are exactly the words ASR gets wrong, because they
// are not in its language model. Measured on a 117-meeting prototype index of
// this team's own archive, "cassini" appears as casini, casino, cassina,
// castini; "librocco" as broccoli, bronco, bookshop. A tokenizer matches none
// of those, so a search for the single most-searched word in the corpus
// silently misses most of its occurrences — the exact confident-empty-answer
// failure the rest of this design refuses to make anywhere else.
//
// Expansion happens at QUERY time, never at index time. Three reasons:
//
//  1. the index stays disposable — adding a name later needs no reindex;
//  2. the index keeps holding what was actually said, so a reference always
//     points at real words rather than at a guess made months ago;
//  3. a wrong alias is a bad query, recoverable by editing this table, rather
//     than a corrupted index.
//
// D-726 now biases the transcriber with the operator's vocabulary, which fixes
// this at the source for NEW recordings. It does not help the meetings already
// published, and it only helps terms somebody thought to type into the box, so
// both mechanisms are needed.
//
// The list below is a starting point drawn from this repository's own
// vocabulary. It is deliberately data rather than cleverness: a curated list
// beats any phonetic algorithm here, because the variants are what one specific
// model produced for one specific set of names.
var searchAliasGroups = [][]string{
	{"cassini", "casini", "casino", "cassina", "cassino", "cassine", "castini"},
	{"gocassini", "go cassini", "gokasini"},
	{"codemyriad", "code myriad", "codemeriad", "code mirror"},
	{"nextcloud", "next cloud", "nextcloud talk", "next cloud talk"},
	{"librocco", "libroco", "libracco", "broccoli", "bronco", "bookshop", "book shop"},
	{"eisbuk", "icebook", "ice book"},
	{"opus", "opis"},
	{"sqlite", "sql lite", "sequel light"},
	{"webdav", "web dav"},
	{"exapp", "ex app"},
}

// mergeSearchAliasGroups combines the shipped groups with an operator's own.
//
// Configured groups WIN on collision, by canonical name: an operator who lists
// spellings for a name we also ship has seen what their own transcriber
// produces, which beats a general list. Merging rather than replacing means
// adding one name does not silently drop every built-in.
func mergeSearchAliasGroups(builtin, configured [][]string) map[string][]string {
	index := buildSearchAliasIndex(builtin)
	for _, group := range configured {
		if len(group) < 2 {
			// Expands to nothing; see normalizeSearchAliases.
			continue
		}
		// Remove whatever the built-ins said about every spelling in this group,
		// so a configured group replaces rather than blends with one it overlaps.
		for _, variant := range group {
			key := strings.ToLower(strings.TrimSpace(variant))
			if existing, ok := index[key]; ok {
				for _, stale := range existing {
					delete(index, strings.ToLower(strings.TrimSpace(stale)))
				}
			}
		}
		for _, variant := range group {
			index[strings.ToLower(strings.TrimSpace(variant))] = group
		}
	}
	return index
}

func buildSearchAliasIndex(groups [][]string) map[string][]string {
	index := map[string][]string{}
	for _, group := range groups {
		for _, variant := range group {
			index[strings.ToLower(strings.TrimSpace(variant))] = group
		}
	}
	return index
}

// searchAliasMaxSpan is how many query words an alias key may cover. Alias keys
// like "nextcloud talk" and "book shop" are multi-word, so matching one word at
// a time would miss them.
const searchAliasMaxSpan = 3

// groupQueryWords turns the query's words into groups of equivalent terms,
// consuming the LONGEST alias span first so "nextcloud talk" is one group
// rather than two.
//
// A word with no alias becomes a group of one, so the caller downstream has a
// single shape to work with rather than two cases.
func groupQueryWords(words []string, useAliases bool, index map[string][]string) [][]string {
	groups := make([][]string, 0, len(words))
	for i := 0; i < len(words); {
		matched := false
		if useAliases && len(index) > 0 {
			for span := min(searchAliasMaxSpan, len(words)-i); span >= 1 && !matched; span-- {
				key := strings.ToLower(strings.Join(words[i:i+span], " "))
				if group, ok := index[key]; ok {
					groups = append(groups, group)
					i += span
					matched = true
				}
			}
		}
		if !matched {
			groups = append(groups, []string{words[i]})
			i++
		}
	}
	return groups
}
