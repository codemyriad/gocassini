//go:build diarizationbench

package transcribe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// TestDiarizationFixtureAccuracy runs Nemotron over private ground-truth clips
// (several people in one mixed stream, labelled from per-participant tracks)
// and reports how many words the split gives to the right person. The clips
// are real meetings: they never enter the repository, and only aggregate
// numbers are logged — no names and no text.
//
//	CASSINI_DIARIZATION_FIXTURES=/path/to/fixtures/meetings \
//	CASSINI_DIARIZATION_MODEL=/path/to/model.int8.onnx \
//	go test -tags diarizationbench ./internal/transcribe -run DiarizationFixture -v
func TestDiarizationFixtureAccuracy(t *testing.T) {
	dir := os.Getenv("CASSINI_DIARIZATION_FIXTURES")
	if dir == "" {
		t.Fatal("set CASSINI_DIARIZATION_FIXTURES to the private clip directory")
	}
	model, err := ResolveDiarizationModel("")
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Clips []struct {
			Clip, Wav, GroundTruth string
		} `json:"clips"`
	}
	readJSONFile(t, filepath.Join(dir, "index.json"), &index)

	var right, total int
	for i, clip := range index.Clips {
		var gt struct {
			Speakers []string `json:"speakers"`
			Words    []struct {
				StartMS int64  `json:"startMs"`
				EndMS   int64  `json:"endMs"`
				Speaker string `json:"speaker"`
			} `json:"words"`
		}
		readJSONFile(t, filepath.Join(dir, clip.GroundTruth), &gt)
		wave := sherpa.ReadWave(filepath.Join(dir, clip.Wav))
		if wave == nil {
			t.Fatalf("clip %d: unreadable wav", i+1)
		}
		turns, err := diarizeFn(model, wave.Samples, wave.SampleRate)
		if err != nil {
			t.Fatalf("clip %d: %v", i+1, err)
		}
		words := make([]Word, len(gt.Words))
		for j, w := range gt.Words {
			words[j] = Word{StartMS: w.StartMS, EndMS: w.EndMS}
		}
		assigned := assignWordsToTurns(words, turns)
		// Score under the best one-to-one mapping of voices to people.
		counts := map[[2]string]int{}
		for j, w := range gt.Words {
			if assigned == nil {
				break
			}
			counts[[2]string{string(rune('A' + assigned[j])), w.Speaker}]++
		}
		hits := bestMapping(counts)
		right += hits
		total += len(gt.Words)
		t.Logf("clip %d: people %d, voices found %d, words right %d/%d (%.1f%%)",
			i+1, len(gt.Speakers), countTurnSpeakers(turns), hits, len(gt.Words), 100*float64(hits)/float64(max(1, len(gt.Words))))
	}
	t.Logf("all clips: words right %d/%d (%.1f%%)", right, total, 100*float64(right)/float64(max(1, total)))
}

// bestMapping returns the largest number of words explained by assigning each
// voice to at most one person (exhaustive; fixtures have at most 8 voices).
func bestMapping(counts map[[2]string]int) int {
	voiceSet, personSet := map[string]bool{}, map[string]bool{}
	for k := range counts {
		voiceSet[k[0]] = true
		personSet[k[1]] = true
	}
	var voicesList, people []string
	for v := range voiceSet {
		voicesList = append(voicesList, v)
	}
	for p := range personSet {
		people = append(people, p)
	}
	used := map[string]bool{}
	var walk func(i int) int
	walk = func(i int) int {
		if i == len(voicesList) {
			return 0
		}
		best := walk(i + 1) // this voice maps to nobody
		for _, p := range people {
			if used[p] {
				continue
			}
			used[p] = true
			best = max(best, counts[[2]string{voicesList[i], p}]+walk(i+1))
			used[p] = false
		}
		return best
	}
	return walk(0)
}

func readJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", filepath.Base(path), err)
	}
}
