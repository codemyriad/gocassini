package transcribe

import (
	"reflect"
	"testing"
)

func TestDecoderHeadPaddingDoesNotShiftSourceTimeline(t *testing.T) {
	words := []Word{
		{Text: "onset", StartMS: 80, EndMS: 240, extentCap: 350},
		{Text: "later", StartMS: 500, EndMS: 820},
	}
	offsetDecoderWords(words, 70000, 100)
	want := []Word{
		{Text: "onset", StartMS: 70000, EndMS: 70140, extentCap: 70250},
		{Text: "later", StartMS: 70400, EndMS: 70720, extentCap: 70000},
	}
	if !reflect.DeepEqual(words, want) {
		t.Fatalf("shifted words = %#v; want %#v", words, want)
	}
	if words[1].extentCapMS() != words[1].EndMS {
		t.Fatal("unset ceiling became an audio extension")
	}
	// With no head padding, retain the established offset behavior exactly.
	plain := []Word{{Text: "word", StartMS: 0, EndMS: 160, extentCap: 320}}
	offsetDecoderWords(plain, 250, 0)
	if !reflect.DeepEqual(plain, []Word{{Text: "word", StartMS: 250, EndMS: 410, extentCap: 570}}) {
		t.Fatalf("unpadded offset = %#v", plain)
	}
}

func TestVADContextClipsToRealSamplesAndMergesAdjacentTurns(t *testing.T) {
	// Expansion can overlap across a pause. Both crops refer to the same
	// source coordinates; seam reconciliation must emit shared words once.
	first := vadContextBounds(0, 16000, 40000, 16000, 500)
	second := vadContextBounds(24000, 40000, 40000, 16000, 500)
	if first != (windowBound{0, 24000}) || second != (windowBound{16000, 40000}) {
		t.Fatalf("bounds = %#v, %#v", first, second)
	}
	older := []Word{{Text: "yes.", StartMS: 1200, EndMS: 1350}}
	newer := []Word{{Text: "yes,", StartMS: 1210, EndMS: 1360}, {Text: "next", StartMS: 1600, EndMS: 1800}}
	got := dedupOverlappingWords(older, newer, false, int64(second.start)/16, int64(first.end-second.start)/16)
	if len(got) != 2 || normalizeOverlapWord(got[0].Text) != "yes" || got[1].Text != "next" {
		t.Fatalf("expanded turn seam = %#v", got)
	}
}

func TestVADBenchmarkPoliciesCoverSourceWithoutGaps(t *testing.T) {
	for _, rate := range []int{8000, 16000, 48000} {
		for _, seconds := range []int{5, 10, 15, 25} {
			for _, overlapMS := range []int{0, 250, 500, 1000} {
				policy := vadDecodePolicy{windowSamples: seconds * 16000, overlapSamples: overlapMS * 16, graceSamples: 8000, minTerminalSamples: min(5, seconds/2) * 16000}
				for _, total := range []int{1, seconds*rate + rate/2 + 1, 30*rate + 1, 55 * rate} {
					bounds := vadSegmentWindowBoundsWithPolicy(total, rate, policy)
					if len(bounds) == 0 || bounds[0].start != 0 || bounds[len(bounds)-1].end != total {
						t.Fatalf("incomplete source coverage: %#v", bounds)
					}
					for i, b := range bounds {
						if b.start < 0 || b.end > total || b.end <= b.start || b.end-b.start > seconds*rate+rate/2 {
							t.Fatalf("invalid crop: %#v", bounds)
						}
						if i > 0 && bounds[i-1].end-b.start != overlapMS*rate/1000 {
							t.Fatalf("gap/overlap changed: %#v", bounds)
						}
					}
				}
			}
		}
	}
}

func TestPreserveVADSpanRetainsWholeSourceWithoutChangingDefault(t *testing.T) {
	baseline := defaultVADDecodePolicy()
	if baseline.preserveVADSpan {
		t.Fatal("production default unexpectedly preserves whole VAD spans")
	}
	// The experimental mode does not require a replacement window length.
	whole := vadDecodePolicy{preserveVADSpan: true}
	for _, rate := range []int{8000, 16000, 48000} {
		// Include real context beyond a 25s detector span and exact edge samples.
		for _, total := range []int{1, 14*rate + 1, 25*rate + 60*rate/1000} {
			got := vadSegmentWindowBoundsWithPolicy(total, rate, whole)
			if !reflect.DeepEqual(got, []windowBound{{start: 0, end: total}}) {
				t.Fatalf("whole span lost/split source at %dHz: %#v", rate, got)
			}
		}
		if len(vadSegmentWindowBoundsWithPolicy(14*rate, rate, baseline)) < 2 {
			t.Fatalf("default window subdivision changed at %dHz", rate)
		}
		if got := vadSegmentWindowBoundsWithPolicy(0, rate, whole); len(got) != 0 {
			t.Fatalf("empty source created a crop: %#v", got)
		}
	}
}

func TestWordsOverlappingSpeechExcludesContextOnlyWords(t *testing.T) {
	words := []Word{
		{Text: "before", StartMS: 0, EndMS: 100},
		{Text: "onset", StartMS: 90, EndMS: 150},
		{Text: "inside", StartMS: 150, EndMS: 200},
		{Text: "offset", StartMS: 200, EndMS: 310},
		{Text: "after", StartMS: 300, EndMS: 400},
	}
	got := wordsOverlappingSpeech(words, 100, 300)
	if len(got) != 3 || got[0].Text != "onset" || got[1].Text != "inside" || got[2].Text != "offset" {
		t.Fatalf("context words leaked or boundary words lost: %+v", got)
	}
	if got[0].StartMS != 90 || got[2].EndMS != 310 {
		t.Fatalf("recording timestamps changed: %+v", got)
	}
}
