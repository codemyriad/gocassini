package topics

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"unicode"
)

// SpeakerKeyEnv names the variable that carries the deployment's key for
// speaker-term hashes. cassini-operator keeps the key in its annotations
// database, beside the identities it already holds, and never in the
// disposable vectors file the hashes are stored in.
const SpeakerKeyEnv = "CASSINI_TOPICS_SPEAKER_KEY"

// SpeakerTermHashes lists a keyed hash (HMAC-SHA256) of every term in speaker
// labels, so a caller can keep people's names out of words it shows. Keyed:
// a plain hash of a first name is reversed with a list of names. A term is a
// run of at least three letters, digits or marks, lower-cased: the same rule
// cassini-operator names proposals with (change both together). With no key
// it returns nothing.
func SpeakerTermHashes(key []byte, labels []string) []string {
	if len(key) == 0 {
		return []string{}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, label := range labels {
		for _, term := range speakerTerms(label) {
			mac := hmac.New(sha256.New, key)
			mac.Write([]byte(term))
			h := hex.EncodeToString(mac.Sum(nil))
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	sort.Strings(out)
	return out
}

func speakerTerms(text string) []string {
	var out []string
	var b strings.Builder
	runes := 0
	flush := func() {
		if runes >= 3 {
			out = append(out, strings.ToLower(b.String()))
		}
		b.Reset()
		runes = 0
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
			b.WriteRune(r)
			runes++
			continue
		}
		flush()
	}
	flush()
	return out
}
