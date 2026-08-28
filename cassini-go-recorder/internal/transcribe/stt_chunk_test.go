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

	// Previous window's decode, already in acc. The word "shared" sits in the
	// overlap, before the cut (start 14600 < 14750), so it stays with window 0.
	// "late" starts at 14800 (>= cut): it must be trimmed from acc and re-owned
	// by window 1.
	acc := []Word{
		{Text: "hello", StartMS: 1000, EndMS: 1400},
		{Text: "world", StartMS: 5000, EndMS: 5400},
		{Text: "shared", StartMS: 14600, EndMS: 14760},
		{Text: "late", StartMS: 14800, EndMS: 14980},
	}

	// Next window's decode. It re-emits "shared" and "late" (both windows saw
	// them), then continues past the overlap.
	next := []Word{
		{Text: "shared", StartMS: 14600, EndMS: 14760}, // before cut -> dropped here
		{Text: "late", StartMS: 14800, EndMS: 14980},   // at/after cut -> kept here
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

func TestClampWordsToTimelineEndExcludesDecoderPadding(t *testing.T) {
	words := []Word{
		{Text: "within", StartMS: 13000, EndMS: 14000},
		{Text: "straddles", StartMS: 14000, EndMS: 14950},
		{Text: "padding", StartMS: 14455, EndMS: 14800},
		{Text: "later-padding", StartMS: 14800, EndMS: 14950},
	}

	got := clampWordsToTimelineEnd(words, 14455)
	want := []Word{
		{Text: "within", StartMS: 13000, EndMS: 14000},
		{Text: "straddles", StartMS: 14000, EndMS: 14455},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clampWordsToTimelineEnd =\n  %#v\nwant\n  %#v", got, want)
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
		{Text: "padding", StartMS: 1000, EndMS: 1200},
	}
	got := finalizeTranscriptWords(samples, sampleRate, words, 1000)
	want := []Word{{Text: "straddles", StartMS: 900, EndMS: 1000}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("finalizeTranscriptWords = %#v; want %#v", got, want)
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
		merged = dedupOverlappingWords(merged, decodeWindow(b), first, windowStartMS, overlapMS)
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
