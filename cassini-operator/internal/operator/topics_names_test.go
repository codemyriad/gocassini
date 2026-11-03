package operator

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

func TestDraftNamesFromTheGroupWithoutAStopList(t *testing.T) {
	for _, tc := range []struct {
		lang    string
		group   []string
		other   []string
		want    string
		exclude string
	}{
		{"en", []string{"the staged rollout of the android build", "the android rollout and the store", "android rollout tomorrow with the team"},
			[]string{"the coffee machine and the team", "the weather and the team", "the team lunch"}, "android / rollout", ""},
		{"it", []string{"il rilascio della versione android", "rilascio android per il negozio", "il rilascio android domani"},
			[]string{"il caffè della macchina", "il tempo per il pranzo", "il pranzo della squadra"}, "android / rilascio", ""},
		{"de", []string{"die Veröffentlichung der Android Version", "Android Veröffentlichung im Laden", "die Android Veröffentlichung morgen mit Anna"},
			[]string{"die Kaffeemaschine und Anna", "das Wetter und Anna", "die Mittagspause mit Anna"}, "android / veröffentlichung", "anna"},
	} {
		var texts []topicNameText
		for i, text := range tc.group {
			texts = append(texts, topicNameText{Meeting: string(rune('a' + i)), Text: text, InGroup: true})
		}
		for i, text := range tc.other {
			texts = append(texts, topicNameText{Meeting: string(rune('a' + i)), Text: text})
		}
		key := []byte("k")
		hashes := map[string]bool{}
		if tc.exclude != "" {
			mac := hmac.New(sha256.New, key)
			mac.Write([]byte(tc.exclude))
			hashes[hex.EncodeToString(mac.Sum(nil))] = true
		}
		name, hints := draftTopicName(texts, speakerTermExcluder(key, hashes))
		if name != tc.want {
			t.Errorf("%s: name %q (hints %v)", tc.lang, name, hints)
		}
		for _, h := range hints {
			if h == "the" || h == "il" || h == "die" || h == "anna" || h == "team" {
				t.Errorf("%s: a common word or a speaker made it into the hints: %v", tc.lang, hints)
			}
		}
	}
}

func TestDraftNamesNeedTwoMeetingsAndSkipSubstrings(t *testing.T) {
	texts := []topicNameText{
		{Meeting: "a", Text: "rollout rollouts budget", InGroup: true},
		{Meeting: "b", Text: "rollout rollouts", InGroup: true},
		{Meeting: "c", Text: "nothing here at all", InGroup: false},
		{Meeting: "c", Text: "other words entirely", InGroup: false},
	}
	name, hints := draftTopicName(texts, nil)
	if name != "rollout" || !reflect.DeepEqual(hints, []string{"rollout"}) {
		t.Fatalf("name %q hints %v", name, hints)
	}
	if got := sharedTopicTerms("Android Rollout", []string{"android / rollout", "store"}); strings.Join(got, ",") != "android,rollout" {
		t.Fatalf("shared terms %v", got)
	}
}

func TestTermsKeepIndicVowelSignsAndMatchTheRecordersHashes(t *testing.T) {
	if got := topicTerms("परियोजना बजट समीक्षा"); !reflect.DeepEqual(got, []string{"परियोजना", "बजट", "समीक्षा"}) {
		t.Fatalf("terms %v", got)
	}
	// HMAC-SHA256("deployment-key", "josé"), as topics.SpeakerTermHashes
	// reports it with that key.
	exclude := speakerTermExcluder([]byte("deployment-key"), map[string]bool{"13f3070a2d5123997c3153f2fa51e14ad25769a5030c080eef4172ec2be8babe": true})
	if terms := topicTerms("José"); len(terms) != 1 || !exclude(terms[0]) {
		t.Fatalf("josé is not excluded: %v", terms)
	}
}
