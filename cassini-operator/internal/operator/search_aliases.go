package operator

import "strings"

// searchAliasGroups maps names to common ASR spellings. Expansion happens at
// query time so aliases can change without rebuilding the transcript index.
// Decoder hints improve new recordings; these aliases also cover older ones.
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

// mergeSearchAliasGroups combines built-in and configured aliases. A configured
// group replaces every built-in group it overlaps, including noncanonical spellings.
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

// groupQueryWords matches the longest alias span first. Unmatched words form
// single-element groups.
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
