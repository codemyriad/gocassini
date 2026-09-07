package transcribe

import (
	"encoding/binary"
	"math"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestProbeHotwordsOnRealAudio is a manual measurement harness, not a CI test.
// It decodes one f32le PCM file with and without a vocabulary and prints both
// transcripts, so the effect of biasing can be measured against a real
// recording. Skipped unless CASSINI_PROBE_PCM is set.
func TestProbeHotwordsOnRealAudio(t *testing.T) {
	pcm := os.Getenv("CASSINI_PROBE_PCM")
	if pcm == "" {
		t.Skip("set CASSINI_PROBE_PCM to run the manual hotword probe")
	}
	raw, err := os.ReadFile(pcm)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]float32, len(raw)/4)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}

	cache := os.Getenv("HOME") + "/.cache/cassini"
	id := ModelID(os.Getenv("CASSINI_PROBE_MODEL"))
	paths, err := EnsureModel(cache, id, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	vad, err := EnsureVAD(cache, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}

	run := func(terms []string, force string) string {
		dec, prov, err := resolveDecoder(t.TempDir(), terms, paths)
		if force != "" && dec != nil {
			dec.Method = force
			if force == decodingGreedySearch {
				dec.MaxActivePaths = 0
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("decoder=%+v prov=%+v", dec, prov)
		rec, err := NewRecognizer(paths, vad, "cpu", 12, dec)
		if err != nil {
			t.Fatal(err)
		}
		defer rec.Close()
		start := time.Now()
		words, err := rec.Transcribe(samples, paths.SampleRate, true)
		if err != nil {
			t.Fatal(err)
		}
		elapsed := time.Since(start)
		audio := float64(len(samples)) / float64(paths.SampleRate)
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		t.Logf("RESULT method=%s biased=%v wall=%v audio=%.1fs rtf=%.3f peakHeapMB=%.0f words=%d",
			dec.Method, dec.Biased(), elapsed.Round(time.Millisecond), audio,
			elapsed.Seconds()/audio, float64(ms.Sys)/1e6, len(words))
		parts := make([]string, len(words))
		for i, w := range words {
			parts[i] = w.Text
		}
		return strings.Join(parts, " ")
	}

	var terms []string
	if v := os.Getenv("CASSINI_PROBE_TERMS"); v != "" {
		terms = strings.Split(v, ",")
	}
	t.Logf("GREEDY-NOHINTS: %s", run(nil, decodingGreedySearch))
	t.Logf("BEAM-NOHINTS:   %s", run(nil, ""))
	t.Logf("BEAM-HINTS:     %s", run(terms, ""))
}
