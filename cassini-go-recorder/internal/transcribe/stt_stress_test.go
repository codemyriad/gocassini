//go:build stressmodel

package transcribe

// Local-only stress harness for D-50. NOT compiled in normal CI (guarded by the
// `stressmodel` build tag). It loads the real int8 model + Silero VAD, decodes a
// dense ~75s rotated-mix fixture N times BOTH ways — the pre-fix single
// full-length span and the post-fix chunked windows — and prints the
// longest-verbatim-run distribution against a reference text so the de-flaking
// claim can be checked empirically.
//
// Run:
//
//	CASSINI_STRESS_WAV=/tmp/rotated_mix_75s.wav \
//	CASSINI_STRESS_REF=/path/reference.txt \
//	CASSINI_STRESS_MODEL_DIR=$HOME/.cache/cassini/models/parakeet-tdt-0.6b-v3-int8 \
//	CASSINI_STRESS_VAD=$HOME/.cache/cassini/vad/silero_vad.onnx \
//	CASSINI_STRESS_N=20 \
//	go test -tags stressmodel -run TestStressNonVADFallback -v ./internal/transcribe/

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestStressNonVADFallback(t *testing.T) {
	wav := os.Getenv("CASSINI_STRESS_WAV")
	refPath := os.Getenv("CASSINI_STRESS_REF")
	modelDir := os.Getenv("CASSINI_STRESS_MODEL_DIR")
	vadPath := os.Getenv("CASSINI_STRESS_VAD")
	if wav == "" || modelDir == "" || vadPath == "" {
		t.Skip("set CASSINI_STRESS_WAV / CASSINI_STRESS_MODEL_DIR / CASSINI_STRESS_VAD")
	}
	n := 20
	if v := os.Getenv("CASSINI_STRESS_N"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}

	paths := ModelPaths{
		EncoderFile: filepath.Join(modelDir, "encoder.int8.onnx"),
		DecoderFile: filepath.Join(modelDir, "decoder.int8.onnx"),
		JoinerFile:  filepath.Join(modelDir, "joiner.int8.onnx"),
		TokensFile:  filepath.Join(modelDir, "tokens.txt"),
		ModelType:   "nemo_transducer",
		SampleRate:  16000,
		FeatureDim:  128,
	}

	samples, err := ExtractMixedFloats(wav)
	if err != nil {
		t.Fatalf("extract mixed floats: %v", err)
	}
	t.Logf("loaded %d samples (%.1fs)", len(samples), float64(len(samples))/16000)

	var refWords []string
	if refPath != "" {
		raw, err := os.ReadFile(refPath)
		if err != nil {
			t.Fatalf("read ref: %v", err)
		}
		refWords = normalizeWords(string(raw))
	}

	rec, err := NewRecognizer(paths, vadPath, "cpu", 4)
	if err != nil {
		t.Fatalf("new recognizer: %v", err)
	}
	defer rec.Close()

	runOnce := func(chunked bool) int {
		var words []Word
		if chunked {
			words, err = rec.transcribeNonVADChunked(samples, paths.SampleRate)
		} else {
			// Pre-fix behavior: single full-length span (only the 55s safety
			// split applies, exactly what ensureMergedFallback used to do).
			words, err = rec.transcribeSegment(samples, paths.SampleRate, 0, false)
		}
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		got := make([]string, 0, len(words))
		for _, w := range words {
			got = append(got, normalizeWords(w.Text)...)
		}
		if len(refWords) == 0 {
			return len(got) // no ref: report raw word count
		}
		return longestRun(got, refWords)
	}

	for _, mode := range []struct {
		name    string
		chunked bool
	}{{"PRE-FIX single-span", false}, {"POST-FIX chunked", true}} {
		runs := make([]int, 0, n)
		for i := 0; i < n; i++ {
			runs = append(runs, runOnce(mode.chunked))
		}
		sort.Ints(runs)
		sum := 0
		below5 := 0
		for _, r := range runs {
			sum += r
			if r < 5 {
				below5++
			}
		}
		t.Logf("%-22s longest-verbatim-run over %d runs: min=%d p50=%d max=%d mean=%.1f  (<5 floor: %d/%d)",
			mode.name, n, runs[0], runs[len(runs)/2], runs[len(runs)-1], float64(sum)/float64(n), below5, n)
		t.Logf("    distribution: %v", runs)
	}
}

func normalizeWords(s string) []string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

// longestRun returns the longest run of consecutive got words that appears
// verbatim as a contiguous subsequence of want — the same metric the e2e gate
// uses for MIN_CAPTURED_RUN_WORDS.
func longestRun(got, want []string) int {
	best := 0
	for i := 0; i < len(got); i++ {
		for j := 0; j < len(want); j++ {
			k := 0
			for i+k < len(got) && j+k < len(want) && got[i+k] == want[j+k] {
				k++
			}
			if k > best {
				best = k
			}
		}
	}
	return best
}
