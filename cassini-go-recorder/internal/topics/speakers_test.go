package topics

import "testing"

func TestSpeakerTermHashesAreKeyedAndCoverEveryNameWord(t *testing.T) {
	key := []byte("deployment-key")
	got := SpeakerTermHashes(key, []string{"José Álvarez", "Mira", "Ab", "मीरा शर्मा"})
	// "ab" is too short to be a term; the rest are hashed once each.
	if len(got) != 5 {
		t.Fatalf("hashes %v", got)
	}
	// The golden value cassini-operator compares with: HMAC-SHA256(key, "josé").
	const jose = "13f3070a2d5123997c3153f2fa51e14ad25769a5030c080eef4172ec2be8babe"
	found := false
	for _, h := range got {
		found = found || h == jose
	}
	if !found {
		t.Fatalf("josé is not among %v", got)
	}
	if other := SpeakerTermHashes([]byte("another-key"), []string{"José"}); other[0] == got[0] {
		t.Fatal("hashes must depend on the key")
	}
	if len(SpeakerTermHashes(nil, []string{"José"})) != 0 {
		t.Fatal("no key, no hashes")
	}
	if speakerTerms("मीरा शर्मा")[0] != "मीरा" {
		t.Fatalf("Devanagari vowel signs are part of the word: %v", speakerTerms("मीरा शर्मा"))
	}
}
