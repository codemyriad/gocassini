package transcribe

// These tests pin the non-VAD chunked-decode strategy (D-50): the merged-mix
// fallback used to hand ~75s of dense audio to a single int8 decode, which
// landed one giant low-confidence span whose longest verbatim word run was
// unstable run-to-run — the e2e gate only ever passed via the
// MIN_CAPTURED_RUN_WORDS floor, so pass/fail rode on whether that one decode
// happened to emit a >=5-word verbatim run. Splitting into short overlapping
// windows keeps each decode short and high-confidence. The window boundaries
// and the overlap dedup are pure Go (no sherpa model), so they are pinned here.

import (
	"reflect"
	"testing"
)

// TestNonVADWindowBoundsDocumentedShape pins the documented window size /
// overlap / stride against a 75s-at-16kHz input. With a 15s window and 0.5s
// overlap the stride is 14.5s, so a 75s buffer is covered by 6 windows. If you
// revert the chunking (single full-length window), this test fails because it
// would see exactly one window spanning the whole buffer.
func TestNonVADWindowBoundsDocumentedShape(t *testing.T) {
	const sr = 16000
	total := 75 * sr // 75 seconds
	window := nonVADWindowSamples
	overlap := nonVADWindowOverlapSamples

	if window != 15*sr {
		t.Fatalf("nonVADWindowSamples = %d; want %d (15s @ 16kHz)", window, 15*sr)
	}
	if overlap != sr/2 {
		t.Fatalf("nonVADWindowOverlapSamples = %d; want %d (0.5s @ 16kHz)", overlap, sr/2)
	}

	bounds := nonVADWindowBounds(total, window, overlap)

	stride := window - overlap // 14.5s
	want := []windowBound{
		{start: 0 * stride, end: 0*stride + window},
		{start: 1 * stride, end: 1*stride + window},
		{start: 2 * stride, end: 2*stride + window},
		{start: 3 * stride, end: 3*stride + window},
		{start: 4 * stride, end: 4*stride + window},
		{start: 5 * stride, end: total}, // last window is short, clamped to total
	}
	if !reflect.DeepEqual(bounds, want) {
		t.Fatalf("nonVADWindowBounds(75s) =\n  %#v\nwant\n  %#v", bounds, want)
	}

	// Every interior window must be exactly one window long; only the final one
	// may be short. And consecutive windows must overlap by exactly `overlap`.
	for i, b := range bounds {
		if i < len(bounds)-1 {
			if b.end-b.start != window {
				t.Fatalf("window %d length = %d; want %d", i, b.end-b.start, window)
			}
			gotOverlap := bounds[i].end - bounds[i+1].start
			if gotOverlap != overlap {
				t.Fatalf("overlap between window %d and %d = %d; want %d", i, i+1, gotOverlap, overlap)
			}
		}
	}

	// The chunking must NOT collapse to a single full-length span — that is the
	// pre-fix behavior this whole change exists to kill.
	if len(bounds) < 2 {
		t.Fatalf("75s input produced %d window(s); want several short windows, not one giant decode", len(bounds))
	}
}

// TestNonVADWindowBoundsShortInputSingleWindow pins the degenerate cases: an
// input shorter than one window, and a zero-length input.
func TestNonVADWindowBoundsShortInputSingleWindow(t *testing.T) {
	const sr = 16000
	short := 8 * sr // 8s < 15s window
	bounds := nonVADWindowBounds(short, nonVADWindowSamples, nonVADWindowOverlapSamples)
	want := []windowBound{{start: 0, end: short}}
	if !reflect.DeepEqual(bounds, want) {
		t.Fatalf("short-input bounds = %#v; want %#v", bounds, want)
	}

	if got := nonVADWindowBounds(0, nonVADWindowSamples, nonVADWindowOverlapSamples); got != nil {
		t.Fatalf("zero-length input bounds = %#v; want nil", got)
	}
}

// TestNonVADWindowBoundsCoverNoGaps guarantees the windows leave no uncovered
// samples: window[i+1].start <= window[i].end for every consecutive pair, and
// the last window reaches `total`. A gap would silently drop transcribable
// audio.
func TestNonVADWindowBoundsCoverNoGaps(t *testing.T) {
	const sr = 16000
	for _, total := range []int{1, sr, 14*sr + 7, 15 * sr, 30 * sr, 75 * sr, 200 * sr} {
		bounds := nonVADWindowBounds(total, nonVADWindowSamples, nonVADWindowOverlapSamples)
		if len(bounds) == 0 {
			t.Fatalf("total=%d produced no windows", total)
		}
		if bounds[0].start != 0 {
			t.Fatalf("total=%d first window start=%d; want 0", total, bounds[0].start)
		}
		if bounds[len(bounds)-1].end != total {
			t.Fatalf("total=%d last window end=%d; want %d", total, bounds[len(bounds)-1].end, total)
		}
		for i := 1; i < len(bounds); i++ {
			if bounds[i].start > bounds[i-1].end {
				t.Fatalf("total=%d gap between window %d (end=%d) and %d (start=%d)",
					total, i-1, bounds[i-1].end, i, bounds[i].start)
			}
		}
	}
}

// TestDedupOverlappingWordsKeepsOverlapWordOnce is the core regression: a word
// that both windows decode in the overlap region must appear exactly once in
// the merged output, and words outside the overlap must all survive.
func TestDedupOverlappingWordsKeepsOverlapWordOnce(t *testing.T) {
	// Window 0 covers [0, 15000]ms; window 1 starts at 14500ms (stride 14.5s),
	// so the overlap region is [14500, 15000]ms with midpoint cut at 14750ms.
	const (
		windowStartMS = int64(14500)
		overlapMS     = int64(500)
	)

	// Previous window's decode, already in acc. "shared" has more left-window
	// context, while "late" has more right-window context.
	acc := []Word{
		{Text: "hello", StartMS: 1000, EndMS: 1400},
		{Text: "world", StartMS: 5000, EndMS: 5400},
		{Text: "shared", StartMS: 14600, EndMS: 14760},
		{Text: "late", StartMS: 14800, EndMS: 14980},
	}

	// Next window's decode. It re-emits "shared" and "late" (both windows saw
	// them), then continues past the overlap.
	next := []Word{
		{Text: "shared", StartMS: 14600, EndMS: 14760}, // duplicate; older copy wins
		{Text: "late", StartMS: 14800, EndMS: 14980},   // duplicate; newer copy wins
		{Text: "again", StartMS: 16000, EndMS: 16400},
		{Text: "more", StartMS: 20000, EndMS: 20400},
	}

	got := dedupOverlappingWords(acc, next, false /*firstWindow*/, windowStartMS, overlapMS)

	want := []Word{
		{Text: "hello", StartMS: 1000, EndMS: 1400},
		{Text: "world", StartMS: 5000, EndMS: 5400},
		{Text: "shared", StartMS: 14600, EndMS: 14760},
		{Text: "late", StartMS: 14800, EndMS: 14980},
		{Text: "again", StartMS: 16000, EndMS: 16400},
		{Text: "more", StartMS: 20000, EndMS: 20400},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dedupOverlappingWords =\n  %#v\nwant\n  %#v", got, want)
	}

	// Explicit count: "shared" and "late" must each appear exactly once.
	counts := map[string]int{}
	for _, w := range got {
		counts[w.Text]++
	}
	for _, dup := range []string{"shared", "late"} {
		if counts[dup] != 1 {
			t.Fatalf("overlap word %q appears %d times; want exactly 1", dup, counts[dup])
		}
	}
}

func TestDedupOverlappingWordsHandlesTimestampJitterAcrossOldMidpoint(t *testing.T) {
	const (
		windowStartMS = int64(9500)
		overlapMS     = int64(500)
	)
	for _, tc := range []struct {
		name      string
		accStart  int64
		nextStart int64
	}{
		{name: "old-before-new-after", accStart: 9740, nextStart: 9760},
		{name: "old-after-new-before", accStart: 9760, nextStart: 9740},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc := []Word{
				{Text: "before", StartMS: 9000, EndMS: 9200},
				{Text: "Shared,", StartMS: tc.accStart, EndMS: tc.accStart + 100},
			}
			next := []Word{
				{Text: "shared", StartMS: tc.nextStart, EndMS: tc.nextStart + 100},
				{Text: "after", StartMS: 10200, EndMS: 10400},
			}
			got := dedupOverlappingWords(acc, next, false, windowStartMS, overlapMS)
			if len(got) != 3 {
				t.Fatalf("merged words = %#v; want before, one shared copy, after", got)
			}
			counts := map[string]int{}
			for _, word := range got {
				counts[normalizeOverlapWord(word.Text)]++
			}
			if counts["shared"] != 1 {
				t.Fatalf("shared copies = %d in %#v; want exactly one", counts["shared"], got)
			}
		})
	}
}

func TestDedupOverlappingWordsRetainsOneSidedOverlapWords(t *testing.T) {
	acc := []Word{
		{Text: "before", StartMS: 9000, EndMS: 9200},
		{Text: "old-only", StartMS: 9800, EndMS: 9900},
	}
	next := []Word{
		{Text: "new-only", StartMS: 9700, EndMS: 9800},
		{Text: "after", StartMS: 10200, EndMS: 10400},
	}
	got := dedupOverlappingWords(acc, next, false, 9500, 500)
	want := []string{"before", "new-only", "old-only", "after"}
	if len(got) != len(want) {
		t.Fatalf("merged words = %#v; want %v", got, want)
	}
	for i := range want {
		if got[i].Text != want[i] {
			t.Fatalf("merged word %d = %q; want %q (all=%#v)", i, got[i].Text, want[i], got)
		}
	}
}

func TestNonVADChunkedDisagreeingSeamUsesMidpointOwnership(t *testing.T) {
	// The merged fallback's int8 decoder can hear the same overlap as one word
	// in the preceding window and several different words in the next. With no
	// text/time match, alignment alone would retain both readings. Keep the old
	// midpoint ownership for this path so only one hypothesis owns each instant.
	acc := []Word{
		{Text: "before", StartMS: 14000, EndMS: 14200},
		{Text: "recognize", StartMS: 14600, EndMS: 14900},
		{Text: "old-late", StartMS: 14820, EndMS: 14920},
	}
	next := []Word{
		{Text: "wreck", StartMS: 14610, EndMS: 14650},
		{Text: "a", StartMS: 14660, EndMS: 14700},
		{Text: "nice", StartMS: 14810, EndMS: 14850},
		{Text: "after", StartMS: 15100, EndMS: 15300},
	}

	got := dedupMergedFallbackWords(acc, next, false, 14500, 500)
	want := []Word{acc[0], acc[1], next[2], next[3]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("disagreeing non-VAD seam = %#v; want midpoint-owned %#v", got, want)
	}

	// The VAD-window merge intentionally keeps one-sided lexical evidence; its
	// private-corpus evaluation covers that behavior and must not inherit the
	// merged fallback's disagreement policy.
	gotVAD := dedupOverlappingWords(acc, next, false, 14500, 500)
	if len(gotVAD) != len(acc)+len(next) {
		t.Fatalf("disagreeing VAD seam = %#v; want both one-sided hypotheses", gotVAD)
	}
}

func TestNonVADChunkedBoundaryContactDoesNotTriggerDisagreementCut(t *testing.T) {
	// The preceding word only touches the overlap start; it does not populate
	// the overlap. The one-sided next-window word must therefore survive rather
	// than activating the merged fallback's disagreement policy.
	acc := []Word{{Text: "before", StartMS: 9300, EndMS: 9500}}
	next := []Word{
		{Text: "new-only", StartMS: 9600, EndMS: 9700},
		{Text: "after", StartMS: 10100, EndMS: 10300},
	}
	want := []Word{acc[0], next[0], next[1]}
	got := dedupMergedFallbackWords(acc, next, false, 9500, 500)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("boundary-contact seam = %#v; want one-sided words %#v", got, want)
	}
}

func TestNonVADChunkedConfidentMatchKeepsOneSidedOverlapWord(t *testing.T) {
	// Midpoint ownership is only the zero-match fallback. Once one shared word
	// anchors the hypotheses, retain additional one-sided lexical evidence from
	// the aligned merge even when it lies before the old midpoint cut.
	acc := []Word{{Text: "shared", StartMS: 9600, EndMS: 9700}}
	next := []Word{
		{Text: "shared", StartMS: 9610, EndMS: 9710},
		{Text: "new-only", StartMS: 9700, EndMS: 9740},
		{Text: "after", StartMS: 10100, EndMS: 10300},
	}
	got := dedupMergedFallbackWords(acc, next, false, 9500, 500)
	wantText := []string{"shared", "new-only", "after"}
	if len(got) != len(wantText) {
		t.Fatalf("anchored non-VAD seam = %#v; want %v", got, wantText)
	}
	for i, text := range wantText {
		if got[i].Text != text {
			t.Fatalf("anchored non-VAD seam word %d = %q; want %q (all=%#v)", i, got[i].Text, text, got)
		}
	}
}

func TestDedupOverlappingWordsReplacesClampedBoundaryCopy(t *testing.T) {
	acc := []Word{
		{Text: "before", StartMS: 9300, EndMS: 9500},
		{Text: "final", StartMS: 10000, EndMS: 10000},
	}
	next := []Word{
		{Text: "final", StartMS: 9800, EndMS: 9950},
		{Text: "after", StartMS: 10100, EndMS: 10300},
	}
	got := dedupOverlappingWords(acc, next, false, 9500, 500)
	want := []Word{
		{Text: "before", StartMS: 9300, EndMS: 9500},
		{Text: "final", StartMS: 9800, EndMS: 9950},
		{Text: "after", StartMS: 10100, EndMS: 10300},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged boundary words = %#v; want %#v", got, want)
	}
}

func TestDedupOverlappingWordsAlignsShiftedPhrase(t *testing.T) {
	// This shape was observed in the private evaluation: the old midpoint
	// splice produced "pass it pass it" because the second hypothesis shifted
	// the repeated phrase about 300ms later.
	acc := []Word{
		{Text: "I", StartMS: 9400, EndMS: 9500},
		{Text: "can", StartMS: 9500, EndMS: 9660},
		{Text: "pass", StartMS: 9660, EndMS: 9820},
		{Text: "it", StartMS: 9820, EndMS: 9980},
	}
	next := []Word{
		{Text: "pass", StartMS: 9960, EndMS: 10120},
		{Text: "it", StartMS: 10120, EndMS: 10280},
		{Text: "to", StartMS: 10280, EndMS: 10440},
	}
	got := dedupOverlappingWords(acc, next, false, 9500, 500)
	wantText := []string{"I", "can", "pass", "it", "to"}
	if len(got) != len(wantText) {
		t.Fatalf("shifted phrase = %#v; want %v", got, wantText)
	}
	for i, text := range wantText {
		if got[i].Text != text {
			t.Fatalf("shifted phrase word %d = %q; want %q (all=%#v)", i, got[i].Text, text, got)
		}
	}
}

func TestDedupOverlappingWordsKeepsRapidRepeatedSingleton(t *testing.T) {
	acc := []Word{{Text: "yes", StartMS: 9550, EndMS: 9650}}
	next := []Word{{Text: "yes", StartMS: 9900, EndMS: 10000}}
	got := dedupOverlappingWords(acc, next, false, 9500, 500)
	if !reflect.DeepEqual(got, []Word{acc[0], next[0]}) {
		t.Fatalf("rapid repeated singleton = %#v; want both occurrences", got)
	}
}

func TestDedupOverlappingWordsKeepsRapidZeroLengthNextSingleton(t *testing.T) {
	acc := []Word{{Text: "yes", StartMS: 9600, EndMS: 9700}}
	next := []Word{{Text: "yes", StartMS: 10000, EndMS: 10000}}
	got := dedupOverlappingWords(acc, next, false, 9500, 500)
	if !reflect.DeepEqual(got, []Word{acc[0], next[0]}) {
		t.Fatalf("zero-length next singleton = %#v; want both occurrences", got)
	}
}

func TestDedupOverlappingWordsPreservesSemanticPunctuation(t *testing.T) {
	acc := []Word{{Text: "C++", StartMS: 9700, EndMS: 9800}}
	next := []Word{
		{Text: "C#", StartMS: 9710, EndMS: 9810},
		{Text: "C", StartMS: 9720, EndMS: 9820},
	}
	got := dedupOverlappingWords(acc, next, false, 9500, 500)
	if len(got) != 3 {
		t.Fatalf("technical tokens = %#v; want C++, C#, and C", got)
	}
	for _, tc := range []struct {
		word string
		want string
	}{
		{word: "C++", want: "c++"},
		{word: "C#", want: "c#"},
		{word: "a-b", want: "a-b"},
		{word: "ab", want: "ab"},
		{word: "“DON’T!”", want: "don't"},
	} {
		if got := normalizeOverlapWord(tc.word); got != tc.want {
			t.Errorf("normalizeOverlapWord(%q) = %q; want %q", tc.word, got, tc.want)
		}
	}
}

func TestDedupOverlappingWordsKeepsStableSourceOrderForEqualStarts(t *testing.T) {
	acc := []Word{{Text: "old", StartMS: 9800, EndMS: 10000}}
	next := []Word{{Text: "new", StartMS: 9800, EndMS: 9900}}
	got := dedupOverlappingWords(acc, next, false, 9500, 500)
	want := []Word{acc[0], next[0]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("equal-start source order = %#v; want %#v", got, want)
	}
}

// TestDedupOverlappingWordsFirstWindowVerbatim pins that the first window is
// kept verbatim (no preceding overlap to dedup against).
func TestDedupOverlappingWordsFirstWindowVerbatim(t *testing.T) {
	next := []Word{
		{Text: "a", StartMS: 0, EndMS: 100},
		{Text: "b", StartMS: 200, EndMS: 300},
	}
	got := dedupOverlappingWords(nil, next, true /*firstWindow*/, 0, 500)
	if !reflect.DeepEqual(got, next) {
		t.Fatalf("first window not kept verbatim: got %#v want %#v", got, next)
	}
}

func TestClampWordsToTimelineEndPreservesBoundaryTokensWithinDecoderPadding(t *testing.T) {
	words := []Word{
		{Text: "within", StartMS: 13000, EndMS: 14000},
		{Text: "straddles", StartMS: 14000, EndMS: 14950},
		{Text: "boundary", StartMS: 14455, EndMS: 14800},
		{Text: "inside-padding", StartMS: 14800, EndMS: 14950},
		{Text: "padding-limit", StartMS: 14955, EndMS: 15000},
		{Text: "beyond-padding", StartMS: 14956, EndMS: 15000},
		{Text: "reversed-padding", StartMS: 14800, EndMS: 14799},
	}

	got := clampWordsToTimelineEnd(words, 14455, 500)
	want := []Word{
		{Text: "within", StartMS: 13000, EndMS: 14000},
		{Text: "straddles", StartMS: 14000, EndMS: 14455},
		{Text: "boundary", StartMS: 14455, EndMS: 14455},
		{Text: "inside-padding", StartMS: 14455, EndMS: 14455},
		{Text: "padding-limit", StartMS: 14455, EndMS: 14455},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clampWordsToTimelineEnd =\n  %#v\nwant\n  %#v", got, want)
	}
}

func TestClampWordsToTimelineEndWithoutPaddingKeepsOnlyExactBoundary(t *testing.T) {
	words := []Word{
		{Text: "boundary", StartMS: 1000, EndMS: 1100},
		{Text: "past-boundary", StartMS: 1001, EndMS: 1100},
	}
	want := []Word{{Text: "boundary", StartMS: 1000, EndMS: 1000}}
	if got := clampWordsToTimelineEnd(words, 1000, 0); !reflect.DeepEqual(got, want) {
		t.Fatalf("clampWordsToTimelineEnd without padding = %#v; want %#v", got, want)
	}
}

func TestFilterWordsByEnergyRejectsSilenceAndClicksButKeepsQuietInterjections(t *testing.T) {
	const sampleRate = 16000
	samples := make([]float32, 3*sampleRate)
	// A quiet 30ms utterance begins 50ms before the model timestamp. The energy
	// margin must preserve it at the -60 dBFS peak boundary.
	for i := 950 * sampleRate / 1000; i < 980*sampleRate/1000; i++ {
		samples[i] = minimumWordPeakAmplitude
	}
	// Negative PCM contributes to peak and RMS by absolute/magnitude values.
	for i := 1500 * sampleRate / 1000; i < 1530*sampleRate/1000; i++ {
		samples[i] = -0.01
	}
	// A lone full-scale click passes the peak and RMS floors but must fail the
	// minimum active-duration requirement.
	samples[1950*sampleRate/1000] = 1
	words := []Word{
		{Text: "quiet", StartMS: 1000, EndMS: 1100},
		{Text: "negative", StartMS: 1500, EndMS: 1600},
		{Text: "click", StartMS: 2000, EndMS: 2100},
		{Text: "silence", StartMS: 2400, EndMS: 2500},
		{Text: "outside", StartMS: 4000, EndMS: 4100},
	}

	got := filterWordsByEnergy(samples, sampleRate, words)
	want := []Word{
		{Text: "quiet", StartMS: 1000, EndMS: 1100},
		{Text: "negative", StartMS: 1500, EndMS: 1600},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterWordsByEnergy =\n  %#v\nwant\n  %#v", got, want)
	}
}

func TestFilterWordsByEnergyAllowsMeasuredDecoderLead(t *testing.T) {
	const sampleRate = 16000
	if wordEnergyPreMarginMS != 100 || wordEnergyPostMarginMS != 200 {
		t.Fatalf("word energy margins = %dms/%dms; want 100ms/200ms", wordEnergyPreMarginMS, wordEnergyPostMarginMS)
	}
	samples := make([]float32, 2*sampleRate)
	// Real Parakeet output has placed a word up to 180ms before its direct PCM.
	// Keep that measured decoder lead while still requiring sustained energy.
	for i := 780 * sampleRate / 1000; i < 830*sampleRate/1000; i++ {
		samples[i] = 0.01
	}
	words := []Word{{Text: "delayed-energy", StartMS: 500, EndMS: 600}}

	got := filterWordsByEnergy(samples, sampleRate, words)
	if !reflect.DeepEqual(got, words) {
		t.Fatalf("filterWordsByEnergy measured decoder lead = %#v; want %#v", got, words)
	}
}

func TestFilterWordsByEnergyRejectsMalformedTimestamps(t *testing.T) {
	samples := make([]float32, 16000)
	for i := range samples {
		samples[i] = 0.1
	}
	words := []Word{
		{Text: "max", StartMS: int64(1<<63 - 1), EndMS: int64(1<<63 - 1)},
		{Text: "min", StartMS: int64(-1 << 63), EndMS: int64(-1 << 63)},
		{Text: "reversed", StartMS: 800, EndMS: 700},
	}
	if got := filterWordsByEnergy(samples, 16000, words); len(got) != 0 {
		t.Fatalf("filterWordsByEnergy malformed timestamps = %#v; want none", got)
	}
}

func TestFinalizeTranscriptWordsClampsBeforeEnergyGate(t *testing.T) {
	const sampleRate = 16000
	samples := make([]float32, sampleRate)
	for i := range samples {
		samples[i] = 0.01
	}
	words := []Word{
		{Text: "straddles", StartMS: 900, EndMS: 1100},
		{Text: "boundary", StartMS: 1000, EndMS: 1200},
		{Text: "inside-vad-padding", StartMS: 1032, EndMS: 1100},
		{Text: "beyond-vad-padding", StartMS: 1033, EndMS: 1100},
	}
	got := finalizeTranscriptWords(samples, sampleRate, words, 1000, 32)
	want := []Word{
		{Text: "straddles", StartMS: 900, EndMS: 1000},
		{Text: "boundary", StartMS: 1000, EndMS: 1000},
		{Text: "inside-vad-padding", StartMS: 1000, EndMS: 1000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("finalizeTranscriptWords = %#v; want %#v", got, want)
	}
}

func TestSamplesToCeilMSMatchesActualVADTailPadding(t *testing.T) {
	if got := samplesToCeilMS(0, 16000); got != 0 {
		t.Fatalf("zero padding = %dms; want 0", got)
	}
	if got := samplesToCeilMS(511, 16000); got != 32 {
		t.Fatalf("511-sample VAD padding = %dms; want ceil(31.9375)=32", got)
	}
	if got := samplesToCeilMS(8000, 16000); got != 500 {
		t.Fatalf("decoder padding = %dms; want 500", got)
	}
}

func TestDecoderTailPadSamplesPadsEveryVADSegment(t *testing.T) {
	const sampleRate = 16000
	for _, seconds := range []int{1, 9, 10, 15, 25, 55} {
		if got := decoderTailPadSamples(seconds*sampleRate, sampleRate, true); got != sampleRate/2 {
			t.Errorf("%ds VAD segment padding = %d samples; want %d", seconds, got, sampleRate/2)
		}
	}
}

func TestLongVADSegmentUsesOverlappingDecoderWindows(t *testing.T) {
	const sampleRate = 16000
	got := vadSegmentWindowBounds(25*sampleRate+700, sampleRate)
	want := []windowBound{
		{start: 0, end: 10 * sampleRate},
		{start: 9*sampleRate + sampleRate/2, end: 19*sampleRate + sampleRate/2},
		{start: 19 * sampleRate, end: 25*sampleRate + 700},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("25s VAD bounds = %#v; want %#v", got, want)
	}
	if got := vadSegmentWindowBounds(10*sampleRate, sampleRate); !reflect.DeepEqual(got, []windowBound{{start: 0, end: 10 * sampleRate}}) {
		t.Fatalf("10s VAD bounds = %#v; want one unsplit window", got)
	}
	barelyLong := 10*sampleRate + 1
	if got := vadSegmentWindowBounds(barelyLong, sampleRate); !reflect.DeepEqual(got, []windowBound{{start: 0, end: barelyLong}}) {
		t.Fatalf("10s+1-sample VAD bounds = %#v; want one extended window", got)
	}
	tinyTerminal := 19*sampleRate + sampleRate/2 + 1
	wantMergedTerminal := []windowBound{
		{start: 0, end: 10 * sampleRate},
		{start: 9*sampleRate + sampleRate/2, end: 15*sampleRate + 1},
		{start: 14*sampleRate + sampleRate/2 + 1, end: tinyTerminal},
	}
	if got := vadSegmentWindowBounds(tinyTerminal, sampleRate); !reflect.DeepEqual(got, wantMergedTerminal) {
		t.Fatalf("tiny-terminal VAD bounds = %#v; want %#v", got, wantMergedTerminal)
	}
}

func TestVADSegmentWindowBoundsStayBoundedWithExactOverlap(t *testing.T) {
	const sampleRate = 16000
	for _, total := range []int{
		10*sampleRate + sampleRate/2 + 1,
		11 * sampleRate,
		19*sampleRate + sampleRate/2 + 1,
		20 * sampleRate,
		25*sampleRate + 123,
		55*sampleRate - 1,
	} {
		bounds := vadSegmentWindowBounds(total, sampleRate)
		if len(bounds) < 2 {
			t.Fatalf("total=%d produced %d bounds; want a split", total, len(bounds))
		}
		if bounds[0].start != 0 || bounds[len(bounds)-1].end != total {
			t.Fatalf("total=%d is not fully covered: %#v", total, bounds)
		}
		for i, bound := range bounds {
			length := bound.end - bound.start
			if length < vadDecodeMinTerminal || length > vadDecodeWindowSamples {
				t.Fatalf("total=%d window %d length=%d; want [%d,%d] (all=%#v)",
					total, i, length, vadDecodeMinTerminal, vadDecodeWindowSamples, bounds)
			}
			if i > 0 {
				if overlap := bounds[i-1].end - bound.start; overlap != vadDecodeWindowOverlap {
					t.Fatalf("total=%d overlap %d->%d = %d; want %d (all=%#v)",
						total, i-1, i, overlap, vadDecodeWindowOverlap, bounds)
				}
			}
		}
	}
}

func TestDecoderTailPadSamplesPreservesNonVADWindowPolicy(t *testing.T) {
	const sampleRate = 16000
	if got := decoderTailPadSamples(9*sampleRate, sampleRate, false); got != sampleRate/2 {
		t.Fatalf("9s non-VAD chunk padding = %d samples; want %d", got, sampleRate/2)
	}
	if got := decoderTailPadSamples(10*sampleRate, sampleRate, false); got != 0 {
		t.Fatalf("10s non-VAD chunk padding = %d samples; want 0", got)
	}
	if got := decoderTailPadSamples(15*sampleRate, sampleRate, false); got != 0 {
		t.Fatalf("15s non-VAD window padding = %d samples; want 0", got)
	}
	for _, tc := range []struct {
		name       string
		samples    int
		sampleRate int
	}{
		{name: "empty", samples: 0, sampleRate: sampleRate},
		{name: "invalid-rate", samples: sampleRate, sampleRate: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decoderTailPadSamples(tc.samples, tc.sampleRate, true); got != 0 {
				t.Fatalf("padding = %d samples; want 0", got)
			}
		})
	}
}

func TestShortInterjectionVADConfig(t *testing.T) {
	t.Setenv("CASSINI_VAD_DEVICE", "cpu")
	cfg := newVADModelConfig("vad.onnx", 16000)
	if cfg.SileroVad.Model != "vad.onnx" || cfg.SampleRate != 16000 {
		t.Fatalf("VAD identity config = model %q sample rate %d", cfg.SileroVad.Model, cfg.SampleRate)
	}
	if cfg.SileroVad.Threshold != 0.18 || cfg.SileroVad.MinSpeechDuration != 0.10 {
		t.Fatalf("VAD short-turn config = threshold %g min speech %g; want 0.18 / 0.10", cfg.SileroVad.Threshold, cfg.SileroVad.MinSpeechDuration)
	}
	if cfg.SileroVad.WindowSize != vadWindowSamples || cfg.NumThreads != 1 || cfg.Provider != "cpu" {
		t.Fatalf("VAD runtime config = window %d threads %d provider %q", cfg.SileroVad.WindowSize, cfg.NumThreads, cfg.Provider)
	}
}

// TestNonVADChunkedDedupEndToEndShape simulates the full window iteration over a
// 75s timeline with a synthetic per-window decoder, asserting that a word in
// every overlap region survives exactly once after the real
// nonVADWindowBounds + dedupOverlappingWords pipeline. This is the model-free
// analogue of transcribeNonVADChunked. If you revert the chunking to a single
// window, the loop produces one window and the per-overlap assertions vanish —
// so this test also documents the multi-window contract.
func TestNonVADChunkedDedupEndToEndShape(t *testing.T) {
	const sr = 16000
	total := 75 * sr
	window := nonVADWindowSamples
	overlap := nonVADWindowOverlapSamples
	overlapMS := int64(overlap) * 1000 / int64(sr)

	bounds := nonVADWindowBounds(total, window, overlap)
	if len(bounds) < 2 {
		t.Fatalf("expected several windows over 75s, got %d", len(bounds))
	}

	// Synthetic decoder: each window "decodes" one word per second of its span,
	// with full-recording timestamps (so the same wall-clock second decoded by
	// two overlapping windows yields the same StartMS in both — exactly the
	// duplication the dedup must collapse).
	decodeWindow := func(b windowBound) []Word {
		var words []Word
		startMS := int64(b.start) * 1000 / int64(sr)
		endMS := int64(b.end) * 1000 / int64(sr)
		for ms := (startMS / 1000) * 1000; ms < endMS; ms += 1000 {
			if ms < startMS {
				continue
			}
			words = append(words, Word{Text: secWord(ms), StartMS: ms, EndMS: ms + 200})
		}
		return words
	}

	var merged []Word
	first := true
	for _, b := range bounds {
		windowStartMS := int64(b.start) * 1000 / int64(sr)
		merged = dedupMergedFallbackWords(merged, decodeWindow(b), first, windowStartMS, overlapMS)
		first = false
	}

	// Every whole-second word from 0..74 must appear exactly once.
	counts := map[string]int{}
	for _, w := range merged {
		counts[w.Text]++
	}
	for s := 0; s < 75; s++ {
		w := secWord(int64(s) * 1000)
		if counts[w] != 1 {
			t.Fatalf("second %ds word %q appears %d times; want exactly 1 (overlap dedup broken)", s, w, counts[w])
		}
	}
	// And no spurious extra words leaked in.
	if len(merged) != 75 {
		t.Fatalf("merged word count = %d; want 75 (one per second, deduped)", len(merged))
	}

	// Output must stay in non-decreasing timestamp order across window seams.
	for i := 1; i < len(merged); i++ {
		if merged[i].StartMS < merged[i-1].StartMS {
			t.Fatalf("merged words out of order at %d: %d < %d", i, merged[i].StartMS, merged[i-1].StartMS)
		}
	}
}

func secWord(ms int64) string {
	s := ms / 1000
	return "w" + itoa(s)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
