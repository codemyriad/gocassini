//go:build boundarybench

package transcribe

// Opt-in recorded-audio ablation. References and recordings stay outside git.
// All conditions use the production VAD, window merge, and energy gate.
import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
	"time"
)

type boundaryFixture struct {
	UseVAD                 *bool    `json:"useVAD,omitempty"`
	ConfiguredVocabulary   []string `json:"configuredVocabulary,omitempty"`
	DeriveParticipantHints bool     `json:"deriveParticipantHints,omitempty"`
	ID                     string   `json:"id"`
	AudioPath              string   `json:"audioPath"`
	MKVPath                string   `json:"mkvPath"`
	StreamIndex            int      `json:"streamIndex"`
	SampleLimitMS          int      `json:"sampleLimitMs"`
	HotwordsFile           string   `json:"hotwordsFile"`
	HotwordScore           *float32 `json:"hotwordScore,omitempty"`
	ScoreStartMS           int64    `json:"scoreStartMs"`
	ScoreEndMS             int64    `json:"scoreEndMs"`
}
type boundaryCondition struct {
	ID                      string `json:"id"`
	WindowMS                int    `json:"windowMs"`
	OverlapMS               int    `json:"overlapMs"`
	HeadMS                  int    `json:"headMs"`
	TailMS                  int    `json:"tailMs"`
	ContextMS               int    `json:"contextMs"`
	PreserveVADSpan         bool   `json:"preserveVADSpan"`
	DisableSyntheticPadding bool   `json:"disableSyntheticPadding"`
}

func TestRecordedBoundaryBenchmark(t *testing.T) {
	corpusPath := os.Getenv("CASSINI_BOUNDARY_CORPUS")
	if corpusPath == "" {
		t.Skip("set CASSINI_BOUNDARY_CORPUS, CASSINI_BOUNDARY_POLICIES, CASSINI_BOUNDARY_OUTPUT and CASSINI_CACHE_ROOT")
	}
	pipeline := os.Getenv("CASSINI_BOUNDARY_PIPELINE")
	if pipeline == "" {
		pipeline = "ablation"
	}
	if pipeline != "ablation" && pipeline != "production" && pipeline != "legacy" && pipeline != "audio-audit" {
		t.Fatal("unsupported benchmark pipeline")
	}
	warmup := os.Getenv("CASSINI_BOUNDARY_SKIP_WARMUP") != "1"
	var fixtures []boundaryFixture
	var conditions []boundaryCondition
	inputs := map[string]any{corpusPath: &fixtures}
	if pipeline == "ablation" {
		inputs[os.Getenv("CASSINI_BOUNDARY_POLICIES")] = &conditions
	} else {
		conditions = []boundaryCondition{{ID: pipeline, PreserveVADSpan: true}}
	}
	for path, target := range inputs {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, target); err != nil {
			t.Fatal(err)
		}
	}
	if len(fixtures) == 0 || len(conditions) == 0 {
		t.Fatal("empty corpus or conditions")
	}
	for _, c := range conditions {
		invalidWindows := !c.PreserveVADSpan && (c.WindowMS < 2000 || c.OverlapMS < 0 || c.OverlapMS >= c.WindowMS/2)
		if invalidWindows || c.HeadMS < 0 || c.TailMS < 0 || c.ContextMS < 0 {
			t.Fatalf("invalid condition: %+v", c)
		}
	}
	cache := os.Getenv("CASSINI_CACHE_ROOT")
	model := ModelID(os.Getenv("CASSINI_BOUNDARY_MODEL"))
	if model == "" {
		model = ModelParakeet06BV3
	}
	device := os.Getenv("CASSINI_BOUNDARY_DEVICE")
	if device == "" {
		device = "cpu"
	}
	var paths ModelPaths
	var vad string
	var err error
	if pipeline != "audio-audit" {
		paths, err = EnsureModel(cache, model, os.Stderr)
		if err != nil {
			t.Fatal(err)
		}
		if paths.EncoderFile == "" || paths.SampleRate != 16000 {
			t.Fatal("benchmark requires a 16 kHz transducer model")
		}
		vad, err = EnsureVAD(cache, os.Stderr)
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := os.OpenFile(os.Getenv("CASSINI_BOUNDARY_OUTPUT"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	enc := json.NewEncoder(out)
	// Metadata can inspect every stream's decoded onset. Cache once per MKV so
	// a multi-track fixture list does not repeat those probes for every track.
	probeCache := make(map[string][]AudioStream)
	for _, fixture := range fixtures {
		extractionStart := time.Now()
		useVAD := true
		if fixture.UseVAD != nil {
			useVAD = *fixture.UseVAD
		}
		if pipeline != "production" && pipeline != "audio-audit" && (fixture.DeriveParticipantHints || len(fixture.ConfiguredVocabulary) > 0) {
			t.Fatal("derived/configured vocabulary requires production profile")
		}
		if fixture.HotwordsFile != "" && (fixture.DeriveParticipantHints || len(fixture.ConfiguredVocabulary) > 0) {
			t.Fatal("manual hotwordsFile cannot be combined with derived/configured vocabulary")
		}
		if fixture.DeriveParticipantHints && fixture.MKVPath == "" {
			t.Fatal("participant hints require MKV metadata")
		}
		if pipeline == "audio-audit" && fixture.SampleLimitMS > 0 {
			t.Fatal("audio-audit requires full tracks; remove sampleLimitMs")
		}
		var samples []float32
		if fixture.MKVPath != "" {
			streams, cached := probeCache[fixture.MKVPath]
			if !cached {
				var e error
				streams, _, e = ProbeMKV(fixture.MKVPath)
				if e != nil {
					t.Fatal(e)
				}
				probeCache[fixture.MKVPath] = streams
			}
			found := false
			for _, stream := range streams {
				if stream.Index == fixture.StreamIndex {
					samples, err = ExtractSpeakerFloats(fixture.MKVPath, stream)
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("stream %d absent", fixture.StreamIndex)
			}
		} else {
			samples, err = ExtractMixedFloats(fixture.AudioPath)
		}
		if err != nil {
			t.Fatal(err)
		}
		if fixture.SampleLimitMS > 0 && len(samples) > fixture.SampleLimitMS*16 {
			samples = samples[:fixture.SampleLimitMS*16]
		}
		hash := sha256.New()
		var raw [4]byte
		for _, s := range samples {
			binary.LittleEndian.PutUint32(raw[:], math.Float32bits(s))
			hash.Write(raw[:])
		}
		if pipeline == "audio-audit" {
			row := map[string]any{
				"pipeline": pipeline, "warmup": false, "fixture": fixture.ID,
				"condition": boundaryCondition{ID: pipeline}, "decoder": "", "model": "",
				"device": "none", "hotwordsSha256": "", "hotwordScore": 0, "beamWidth": 0,
				"pcmSha256": fmt.Sprintf("%x", hash.Sum(nil)), "samples": len(samples),
				"seconds": time.Since(extractionStart).Seconds(), "words": []Word{},
			}
			if err = enc.Encode(row); err != nil {
				t.Fatal(err)
			}
			if err = out.Sync(); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s / audio-audit: %d samples (no inference)", fixture.ID, len(samples))
			continue
		}
		method := os.Getenv("CASSINI_BOUNDARY_DECODER")
		if method == "" {
			method = decodingModifiedBeamSearch
		}
		if method != decodingGreedySearch && method != decodingModifiedBeamSearch {
			t.Fatal("unsupported benchmark decoder")
		}
		if pipeline == "legacy" {
			method = decodingModifiedBeamSearch
		}
		score := float32(defaultHotwordsScore)
		if fixture.HotwordScore != nil {
			score = *fixture.HotwordScore
		}
		decoder := &DecoderConfig{Method: method, MaxActivePaths: hotwordsMaxActivePaths, HotwordsFile: fixture.HotwordsFile, Score: score, BpeVocabFile: paths.BpeVocabFile}
		if method == decodingGreedySearch {
			decoder.HotwordsFile = ""
			decoder.MaxActivePaths = 0
		}
		var hintsProv *HintsProvenance
		if pipeline == "production" && fixture.HotwordsFile == "" {
			if fixture.HotwordScore != nil {
				t.Fatal("production vocabulary uses operator score configuration, not hotwordScore")
			}
			decoder, hintsProv, err = boundaryProductionDecoder(t.TempDir(), fixture, probeCache[fixture.MKVPath], paths)
			if err != nil {
				t.Fatal(err)
			}
			method = decoder.Method
		}
		if pipeline == "production" && !useVAD {
			decoder = mergedFallbackDecoder(paths, decoder)
			hintsProv = mergedFallbackHints(paths, hintsProv, true)
			method = decoder.Method
		}
		var hotwordsHash string
		if decoder.Biased() {
			data, err := os.ReadFile(decoder.HotwordsFile)
			if err != nil {
				t.Fatal(err)
			}
			hotwordsHash = fmt.Sprintf("%x", sha256.Sum256(data))
		} else {
			decoder.Score = 0 // No contextual score is applied without hotwords.
		}
		rec, e := newRecognizerWithProfile(paths, vad, device, 1, decoder, pipeline == "production")
		if e != nil {
			t.Fatal(e)
		}
		// Warm the loaded recognizer before timing conditions. Timings remain
		// diagnostic; accuracy comparisons do not depend on them.
		if warmup {
			if _, e = rec.Transcribe(samples, 16000, useVAD); e != nil {
				t.Fatal(e)
			}
		}
		for _, condition := range conditions {
			policy := vadDecodePolicy{windowSamples: condition.WindowMS * 16, overlapSamples: condition.OverlapMS * 16, graceSamples: 8000, minTerminalSamples: min(5000, condition.WindowMS/2) * 16, headPaddingMS: condition.HeadMS, tailPaddingMS: condition.TailMS, contextMS: condition.ContextMS, preserveVADSpan: condition.PreserveVADSpan, disableSyntheticPadding: condition.DisableSyntheticPadding}
			// Preserve the established 5s terminal policy for >=10s windows; smaller
			// windows use half a window so rebalancing cannot exceed the main window.
			if pipeline != "ablation" {
				policy = rec.decodePolicy()
				condition = boundaryCondition{ID: pipeline, WindowMS: policy.windowSamples / 16, OverlapMS: policy.overlapSamples / 16, HeadMS: policy.headPaddingMS, TailMS: policy.tailPaddingMS, ContextMS: policy.contextMS, PreserveVADSpan: policy.preserveVADSpan, DisableSyntheticPadding: policy.disableSyntheticPadding}
			}
			// Non-VAD chunk decoding reads the recognizer policy internally.
			if pipeline == "ablation" {
				rec.boundaryPolicy = &policy
			}
			before := time.Now()
			words, e := rec.transcribeWithVADPolicy(samples, 16000, useVAD, policy)
			elapsed := time.Since(before)
			if e != nil {
				t.Fatal(e)
			}
			if fixture.ScoreEndMS > 0 {
				kept := words[:0]
				for _, w := range words {
					if w.StartMS >= fixture.ScoreStartMS && w.StartMS < fixture.ScoreEndMS {
						kept = append(kept, w)
					}
				}
				words = kept
			}
			row := struct {
				Pipeline       string            `json:"pipeline"`
				Warmup         bool              `json:"warmup"`
				Fixture        string            `json:"fixture"`
				Condition      boundaryCondition `json:"condition"`
				Decoder        string            `json:"decoder"`
				HotwordsSHA256 string            `json:"hotwordsSha256"`
				HotwordScore   float32           `json:"hotwordScore"`
				BeamWidth      int               `json:"beamWidth"`
				Model          ModelID           `json:"model"`
				Device         string            `json:"device"`
				PCMHash        string            `json:"pcmSha256"`
				Samples        int               `json:"samples"`
				Seconds        float64           `json:"seconds"`
				Words          []Word            `json:"words"`
				Hints          *HintsProvenance  `json:"hints,omitempty"`
				UseVAD         bool              `json:"useVAD"`
			}{pipeline, warmup, fixture.ID, condition, method, hotwordsHash, decoder.Score, decoder.MaxActivePaths, model, device, fmt.Sprintf("%x", hash.Sum(nil)), len(samples), elapsed.Seconds(), words, hintsProv, useVAD}
			if e = enc.Encode(row); e != nil {
				t.Fatal(e)
			}
			if e = out.Sync(); e != nil {
				t.Fatal(e)
			}
			t.Logf("%s / %s: %d words, %.2fs", fixture.ID, condition.ID, len(words), elapsed.Seconds())
		}
		rec.Close()
	}
}

// Use the same vocabulary and per-speaker exclusion path as Transcribe.
func boundaryProductionDecoder(dir string, fixture boundaryFixture, streams []AudioStream, paths ModelPaths) (*DecoderConfig, *HintsProvenance, error) {
	if fixture.HotwordsFile != "" {
		return nil, nil, fmt.Errorf("manual hotwords must use the explicit ablation path")
	}
	hintStreams := streams
	if !fixture.DeriveParticipantHints {
		hintStreams = nil
	}
	vocabulary := vocabularyForBuild(fixture.ConfiguredVocabulary, hintStreams)
	decoder, provenance, err := resolveDecoderVocabulary(dir, vocabulary, paths)
	if err != nil {
		return nil, nil, err
	}
	speakers, err := speakerDecoders(dir, vocabulary, streams, decoder)
	if err != nil {
		return nil, nil, err
	}
	if provenance != nil && provenance.Applied && len(speakers) > 0 {
		provenance.OwnNameExcluded = true
	}
	if specific := speakers[fixture.StreamIndex]; specific != nil {
		decoder = specific
	}
	return decoder, provenance, nil
}

func TestBoundaryProductionVocabularyMatchesBuild(t *testing.T) {
	t.Setenv(envHintsDisabled, "")
	t.Setenv(envHintsScore, "2")
	streams := []AudioStream{{Index: 2, SpeakerLabel: "Silvio"}, {Index: 5, SpeakerLabel: "Chris"}}
	fixture := boundaryFixture{StreamIndex: 2, DeriveParticipantHints: true, ConfiguredVocabulary: []string{"Librocco"}}
	paths := transducerPaths(t)
	got, prov, err := boundaryProductionDecoder(t.TempDir(), fixture, streams, paths)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	vocabulary := vocabularyForBuild(fixture.ConfiguredVocabulary, streams)
	base, wantProv, err := resolveDecoderVocabulary(dir, vocabulary, paths)
	if err != nil {
		t.Fatal(err)
	}
	perSpeaker, err := speakerDecoders(dir, vocabulary, streams, base)
	if err != nil {
		t.Fatal(err)
	}
	if wantProv != nil && wantProv.Applied && len(perSpeaker) > 0 {
		wantProv.OwnNameExcluded = true
	}
	want := perSpeaker[2]
	a, err := os.ReadFile(got.HotwordsFile)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(want.HotwordsFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != "Librocco\nChris :0.5\n" || string(a) != string(b) || !reflect.DeepEqual(prov, wantProv) {
		t.Fatalf("vocabulary/provenance mismatch: %q %+v", a, prov)
	}
	got.HotwordsFile = ""
	want.HotwordsFile = ""
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoder mismatch: %+v %+v", got, want)
	}
	fixture.HotwordsFile = "manual.txt"
	if _, _, err = boundaryProductionDecoder(t.TempDir(), fixture, streams, paths); err == nil {
		t.Fatal("accepted contradictory manual and automatic hints")
	}
}
