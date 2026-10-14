package topics

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

// TestMeasureBatchingCost times the same texts embedded alone and in padded
// batches, and reports how far apart the two vectors of each text are. It is
// a measurement, not a check of the shipped default: point
// CASSINI_TOPICS_BATCH_TEXTS at a JSON-lines file of {"text": "..."} (natural
// passages of real meetings, kept outside the repository) together with
// CASSINI_TOPICS_MODEL_DIR. CASSINI_TOPICS_THREADS sets the threads.
func TestMeasureBatchingCost(t *testing.T) {
	path := os.Getenv("CASSINI_TOPICS_BATCH_TEXTS")
	if path == "" {
		t.Skip("CASSINI_TOPICS_BATCH_TEXTS is not set; skipping the batching measurement")
	}
	texts := readTexts(t, path)
	threads, _ := Threads(4)
	measure := func(batch bool) ([]Embedding, time.Duration) {
		files := modelFiles(t)
		e, err := Open(files, Options{Threads: threads, Batch: batch})
		if err != nil {
			t.Fatal(err)
		}
		defer e.Close()
		start := time.Now()
		got, err := e.EmbedTexts(context.Background(), texts)
		if err != nil {
			t.Fatal(err)
		}
		return got, time.Since(start)
	}
	single, singleTime := measure(false)
	batched, batchTime := measure(true)
	minCos, tokens := math.Inf(1), 0
	for i := range texts {
		minCos = min(minCos, Cosine(single[i].Vector, batched[i].Vector))
		tokens += single[i].TokenCount
	}
	t.Logf("%d texts, %d tokens, %d threads: alone %s (%.0f tok/s), batched %s (%.0f tok/s), %.2fx; min cosine batched vs alone %.7f",
		len(texts), tokens, threads, singleTime.Round(time.Millisecond), float64(tokens)/singleTime.Seconds(),
		batchTime.Round(time.Millisecond), float64(tokens)/batchTime.Seconds(), singleTime.Seconds()/batchTime.Seconds(), minCos)
	if minCos < 0.99999 {
		t.Errorf("a batched vector differs from the text alone: min cosine %.7f < 0.99999", minCos)
	}
}

func readTexts(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var texts []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var line struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		texts = append(texts, line.Text)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return texts
}
