package transcribe

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// scanWindow is the boundary rule as it used to be written: a per-sample walk
// that stops at the first output sample needing audio past the end of the
// segment. overlayWindow now states the same thing in closed form, and this is
// the oracle that says it still means the same thing.
func scanWindow(placement Placement, sampleRate, srcSamples, dstSamples int) (int, int) {
	if dstSamples == 0 || srcSamples < 2 || sampleRate <= 0 || placement.Rate <= 0 {
		return 0, 0
	}
	msPerSample := 1000.0 / float64(sampleRate)
	start := int(math.Ceil(placement.OffsetMS / msPerSample))
	if start < 0 {
		start = 0
	}
	if start >= dstSamples {
		return 0, 0
	}
	stop := start
	for j := start; j < dstSamples; j++ {
		pos := ((float64(j)*msPerSample - placement.OffsetMS) / placement.Rate) / msPerSample
		if pos < 0 {
			continue
		}
		if pos >= float64(srcSamples) {
			break
		}
		stop = j + 1
	}
	if stop <= start {
		return 0, 0
	}
	return start, stop
}

// The window has to be the same window at both rates and at every offset, or
// the published mix and the transcript disagree about where a word starts. The
// closed form is algebra over floating point; the scan is what the renderer
// actually evaluates. They must not part company.
func TestOverlayWindowMatchesTheScanItReplaced(t *testing.T) {
	rng := rand.New(rand.NewSource(20260904))
	for _, rate := range []int{16000, 48000} {
		for i := 0; i < 4000; i++ {
			placement := Placement{
				// Negative, fractional, mid-timeline and past the end.
				OffsetMS: (rng.Float64()*1.4 - 0.2) * 60_000,
				// Plausible clock drift, and the extremes the fit still admits.
				Rate: 1 + (rng.Float64()*2-1)*0.005,
			}
			srcSamples := rng.Intn(rate*30) + 2
			dstSamples := rate * 60
			gotStart, gotStop := overlayWindow(placement, rate, srcSamples, dstSamples)
			wantStart, wantStop := scanWindow(placement, rate, srcSamples, dstSamples)
			if gotStart != wantStart || gotStop != wantStop {
				t.Fatalf("at %d Hz, offset %.6f ms rate %.9f over %d source samples: window [%d, %d), want [%d, %d)",
					rate, placement.OffsetMS, placement.Rate, srcSamples, gotStart, gotStop, wantStart, wantStop)
			}
		}
	}
	// The degenerate shapes the old scan refused outright.
	for _, tc := range []struct{ rate, src, dst int }{{16000, 1, 100}, {16000, 0, 100}, {0, 100, 100}, {16000, 100, 0}} {
		if from, to := overlayWindow(Placement{Rate: 1}, tc.rate, tc.src, tc.dst); from != 0 || to != 0 {
			t.Fatalf("rate %d, %d source samples, %d destination samples gave [%d, %d)", tc.rate, tc.src, tc.dst, from, to)
		}
	}
	if from, to := overlayWindow(Placement{OffsetMS: 100, Rate: 0}, 16000, 100, 100); from != 0 || to != 0 {
		t.Fatal("a zero rate produced a window")
	}
}

// The chunked file renderer and the whole-timeline one are the same arithmetic
// or they are not the same splice. This pins them together on signals that
// exercise every branch: fractional offsets so the interpolation is never a
// plain copy, drifted rates so the source and destination indexes walk apart,
// and window edges that fall inside chunks rather than on their boundaries.
func TestOverlayFileWindowMatchesTheInMemoryOverlay(t *testing.T) {
	const sampleRate = 48000
	rng := rand.New(rand.NewSource(4152))
	cases := []struct {
		name      string
		offsetMS  float64
		rate      float64
		srcLen    int
		dstLen    int
		fromStart bool
	}{
		{name: "mid-timeline, drifting fast", offsetMS: 1234.567, rate: 1 + 300e-6, srcLen: sampleRate * 2, dstLen: sampleRate * 5},
		{name: "mid-timeline, drifting slow", offsetMS: 987.321, rate: 1 - 300e-6, srcLen: sampleRate * 2, dstLen: sampleRate * 5},
		{name: "at sample zero", offsetMS: 0, rate: 1, srcLen: sampleRate, dstLen: sampleRate * 3},
		{name: "starting before the timeline", offsetMS: -250.5, rate: 1, srcLen: sampleRate, dstLen: sampleRate * 3},
		{name: "running past the end of the timeline", offsetMS: 2500, rate: 1, srcLen: sampleRate * 4, dstLen: sampleRate * 3},
		{name: "one chunk long", offsetMS: 500.25, rate: 1, srcLen: 3000, dstLen: sampleRate},
		{name: "shorter than a chunk", offsetMS: 500.25, rate: 1, srcLen: 700, dstLen: sampleRate},
		{name: "edges strictly inside chunks", offsetMS: 21.3333, rate: 1.0000173, srcLen: 4096*3 + 137, dstLen: sampleRate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			placement := Placement{OffsetMS: tc.offsetMS, Rate: tc.rate}
			floor := make([]float32, tc.dstLen)
			for i := range floor {
				floor[i] = float32(int16(rng.Intn(65536)-32768)) / 32768
			}
			src := make([]float32, tc.srcLen)
			for i := range src {
				src[i] = float32(int16(rng.Intn(65536)-32768)) / 32768
			}

			want := append([]float32(nil), floor...)
			wantFrom, wantTo := overlayOntoTimeline(want, src, sampleRate, placement)

			dir := t.TempDir()
			floorPath := filepath.Join(dir, "floor.wav")
			srcPath := filepath.Join(dir, "src.wav")
			writeFloorWAV(t, floorPath, floor, sampleRate)
			writeFloorWAV(t, srcPath, src, sampleRate)
			dstFile, err := openWAV(floorPath)
			if err != nil {
				t.Fatalf("openWAV: %v", err)
			}
			srcFile, err := openWAV(srcPath)
			if err != nil {
				t.Fatalf("openWAV: %v", err)
			}
			gotFrom, gotTo, err := overlayFileWindow(dstFile, srcFile, tc.srcLen, placement, sampleRate, 0)
			if err != nil {
				t.Fatalf("overlayFileWindow: %v", err)
			}
			if err := dstFile.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			_ = srcFile.Close()
			if gotFrom != wantFrom || gotTo != wantTo {
				t.Fatalf("file window [%d, %d), memory window [%d, %d)", gotFrom, gotTo, wantFrom, wantTo)
			}
			got := readWAVFloats(t, floorPath)
			if len(got) != len(want) {
				t.Fatalf("file holds %d samples, want %d", len(got), len(want))
			}
			for i := range want {
				// The in-memory overlay is compared through the same
				// quantisation the file went through; anything else would be
				// comparing a float against its own rounding.
				if s16FromFloat32(got[i]) != s16FromFloat32(want[i]) {
					t.Fatalf("sample %d is %v on disk and %v in memory (window [%d, %d))",
						i, got[i], want[i], wantFrom, wantTo)
				}
			}
		})
	}
}

// The fade is inside the window, so [start, stop) stays the exact span the
// splice modified — and the handover is monotonic at both ends, which is what
// removes the click.
func TestOverlayFileWindowCrossfadesInsideItsWindow(t *testing.T) {
	const sampleRate = 48000
	const fade = 720 // 15 ms
	const floorValue = float32(0.25)
	const sourceValue = float32(-0.75)

	dir := t.TempDir()
	floorPath := filepath.Join(dir, "floor.wav")
	srcPath := filepath.Join(dir, "src.wav")
	floor := recordedMarker(sampleRate*4, floorValue)
	writeFloorWAV(t, floorPath, floor, sampleRate)
	writeFloorWAV(t, srcPath, recordedMarker(sampleRate, sourceValue), sampleRate)

	dstFile, err := openWAV(floorPath)
	if err != nil {
		t.Fatalf("openWAV: %v", err)
	}
	srcFile, err := openWAV(srcPath)
	if err != nil {
		t.Fatalf("openWAV: %v", err)
	}
	from, to, err := overlayFileWindow(dstFile, srcFile, sampleRate, Placement{OffsetMS: 1000, Rate: 1}, sampleRate, fade)
	if err != nil {
		t.Fatalf("overlayFileWindow: %v", err)
	}
	_ = dstFile.Close()
	_ = srcFile.Close()
	got := readWAVFloats(t, floorPath)

	// Outside the window, not one sample moved.
	for i := 0; i < len(got); i++ {
		if i >= from && i < to {
			continue
		}
		if math.Float32bits(got[i]) != math.Float32bits(floorValue) {
			t.Fatalf("sample %d outside the window is %v, want the recorded %v", i, got[i], floorValue)
		}
	}
	// The middle is the upload, plainly.
	for i := from + fade; i < to-fade; i++ {
		if math.Abs(float64(got[i]-sourceValue)) > 1.0/32768 {
			t.Fatalf("sample %d in the body of the window is %v, want the uploaded %v", i, got[i], sourceValue)
		}
	}
	// The head ramps one way and the tail the other, without a step anywhere.
	for i := from; i < from+fade-1; i++ {
		if got[i+1] > got[i]+1.0/32768 {
			t.Fatalf("the head of the fade is not monotonic at sample %d: %v then %v", i, got[i], got[i+1])
		}
	}
	for i := to - fade; i < to-1; i++ {
		if got[i+1] < got[i]-1.0/32768 {
			t.Fatalf("the tail of the fade is not monotonic at sample %d: %v then %v", i, got[i], got[i+1])
		}
	}
	// No seam anywhere is larger than one fade step. This is the click.
	step := float64(floorValue-sourceValue)/float64(fade) + 2.0/32768
	for i := 1; i < len(got); i++ {
		if jump := math.Abs(float64(got[i] - got[i-1])); jump > step {
			t.Fatalf("sample %d jumps by %v, more than the %v a %d-sample fade allows", i, jump, step, fade)
		}
	}
}

// The awkward geometries: a window that opens at sample zero, one that closes
// exactly at the end of the timeline, one shorter than two fades, one placed
// past where the participant's own recording ended, and two that overlap so the
// later one has to win.
func TestOverlayFileWindowHandlesTheAwkwardGeometries(t *testing.T) {
	const sampleRate = 16000

	newFloor := func(t *testing.T, dir string, samples int) (*wavFile, string) {
		t.Helper()
		path := filepath.Join(dir, "floor.wav")
		writeFloorWAV(t, path, recordedMarker(samples, 0.25), sampleRate)
		floor, err := openWAV(path)
		if err != nil {
			t.Fatalf("openWAV: %v", err)
		}
		return floor, path
	}
	newSource := func(t *testing.T, dir, name string, samples int, value float32) *wavFile {
		t.Helper()
		path := filepath.Join(dir, name)
		writeFloorWAV(t, path, recordedMarker(samples, value), sampleRate)
		src, err := openWAV(path)
		if err != nil {
			t.Fatalf("openWAV: %v", err)
		}
		return src
	}

	t.Run("a window that opens at sample zero", func(t *testing.T) {
		dir := t.TempDir()
		floor, path := newFloor(t, dir, sampleRate)
		src := newSource(t, dir, "src.wav", sampleRate/2, -0.5)
		from, to, err := overlayFileWindow(floor, src, sampleRate/2, Placement{OffsetMS: 0, Rate: 1}, sampleRate, 160)
		if err != nil {
			t.Fatalf("overlayFileWindow: %v", err)
		}
		_ = floor.Close()
		_ = src.Close()
		if from != 0 {
			t.Fatalf("the window opens at %d, want 0", from)
		}
		got := readWAVFloats(t, path)
		if got[to/2] > 0 {
			t.Fatal("the middle of a window at sample zero kept the recorded value")
		}
	})

	t.Run("a window that runs off the end of the timeline", func(t *testing.T) {
		dir := t.TempDir()
		floor, path := newFloor(t, dir, sampleRate)
		src := newSource(t, dir, "src.wav", sampleRate*2, -0.5)
		_, to, err := overlayFileWindow(floor, src, sampleRate*2, Placement{OffsetMS: 500, Rate: 1}, sampleRate, 160)
		if err != nil {
			t.Fatalf("overlayFileWindow: %v", err)
		}
		_ = floor.Close()
		_ = src.Close()
		if to != sampleRate {
			t.Fatalf("the window closes at %d, want the end of the timeline at %d", to, sampleRate)
		}
		got := readWAVFloats(t, path)
		if len(got) != sampleRate {
			t.Fatalf("the timeline grew to %d samples", len(got))
		}
	})

	t.Run("a window shorter than two fades", func(t *testing.T) {
		dir := t.TempDir()
		floor, path := newFloor(t, dir, sampleRate)
		src := newSource(t, dir, "src.wav", 100, -0.5)
		from, to, err := overlayFileWindow(floor, src, 100, Placement{OffsetMS: 500, Rate: 1}, sampleRate, 720)
		if err != nil {
			t.Fatalf("overlayFileWindow: %v", err)
		}
		_ = floor.Close()
		_ = src.Close()
		if to-from != 100 {
			t.Fatalf("a 100-sample segment covered %d samples", to-from)
		}
		got := readWAVFloats(t, path)
		// Everything still moved somewhat, and nothing overshot the source.
		for i := from; i < to; i++ {
			if got[i] > 0.25 || got[i] < -0.5 {
				t.Fatalf("sample %d faded to %v, outside the two values it is between", i, got[i])
			}
		}
	})

	t.Run("a window past the end of the participant's own recording", func(t *testing.T) {
		dir := t.TempDir()
		// A floor that is all timeline but whose recorded part ended early is
		// what writeParticipantFloor produces for a participant who left: the
		// tail is zero-padded, and an upload covering it must still land.
		path := filepath.Join(dir, "floor.wav")
		floorSamples := make([]float32, sampleRate*4)
		for i := 0; i < sampleRate; i++ {
			floorSamples[i] = 0.25
		}
		writeFloorWAV(t, path, floorSamples, sampleRate)
		floor, err := openWAV(path)
		if err != nil {
			t.Fatalf("openWAV: %v", err)
		}
		src := newSource(t, dir, "src.wav", sampleRate, -0.5)
		from, to, err := overlayFileWindow(floor, src, sampleRate, Placement{OffsetMS: 2000, Rate: 1}, sampleRate, 160)
		if err != nil {
			t.Fatalf("overlayFileWindow: %v", err)
		}
		_ = floor.Close()
		_ = src.Close()
		got := readWAVFloats(t, path)
		if mid := (from + to) / 2; got[mid] > -0.4 {
			t.Fatalf("sample %d past the recorded audio is %v; the upload did not land on the padded tail", mid, got[mid])
		}
	})

	t.Run("two windows that abut", func(t *testing.T) {
		// A microphone change mid-call ends one segment and starts the next.
		// Every window fades at both edges, so where two of them meet the
		// published audio passes through the recorded track for the length of
		// two fades — thirty milliseconds of the same speaker, recorded rather
		// than uploaded.
		//
		// That is the deliberate trade, and this is what has to hold for it to
		// be the right one: the handover is gradual. Suppressing the fades at an
		// abutting seam would put a step there instead, which is the click the
		// crossfade exists to remove, so the dip stays and the discontinuity
		// does not.
		dir := t.TempDir()
		floor, path := newFloor(t, dir, sampleRate*4)
		first := newSource(t, dir, "first.wav", sampleRate, -0.5)
		_, firstTo, err := overlayFileWindow(floor, first, sampleRate, Placement{OffsetMS: 500, Rate: 1}, sampleRate, 160)
		if err != nil {
			t.Fatalf("overlayFileWindow: %v", err)
		}
		second := newSource(t, dir, "second.wav", sampleRate, -0.5)
		secondFrom, _, err := overlayFileWindow(floor, second, sampleRate, Placement{OffsetMS: 1500, Rate: 1}, sampleRate, 160)
		if err != nil {
			t.Fatalf("overlayFileWindow: %v", err)
		}
		_ = floor.Close()
		_ = first.Close()
		_ = second.Close()
		if secondFrom != firstTo {
			t.Fatalf("the two windows do not abut: %d then %d", firstTo, secondFrom)
		}
		got := readWAVFloats(t, path)
		// No step anywhere across the seam: every sample is within one fade
		// step of the one before it.
		step := 0.75/160 + 2.0/32768
		for i := firstTo - 200; i < secondFrom+200 && i < len(got); i++ {
			if jump := math.Abs(float64(got[i] - got[i-1])); jump > step {
				t.Fatalf("sample %d at the seam jumps by %v, more than the %v a fade allows", i, jump, step)
			}
		}
		// The uploaded audio stands on both sides, and the excursion between
		// them is bounded by the two fades.
		if got[firstTo-200] > -0.4 || got[secondFrom+200] > -0.4 {
			t.Fatal("the uploaded audio does not stand on both sides of the seam")
		}
		for i := 0; i < len(got); i++ {
			if got[i] > 0.25+1.0/32768 {
				t.Fatalf("sample %d is %v, past the recorded value the fade returns to", i, got[i])
			}
		}
	})

	t.Run("a later window nested inside an earlier one", func(t *testing.T) {
		dir := t.TempDir()
		floor, path := newFloor(t, dir, sampleRate*4)
		wide := newSource(t, dir, "wide.wav", sampleRate*2, -0.5)
		if _, _, err := overlayFileWindow(floor, wide, sampleRate*2, Placement{OffsetMS: 500, Rate: 1}, sampleRate, 0); err != nil {
			t.Fatalf("overlayFileWindow: %v", err)
		}
		narrow := newSource(t, dir, "narrow.wav", sampleRate/2, 0.9)
		from, to, err := overlayFileWindow(floor, narrow, sampleRate/2, Placement{OffsetMS: 1000, Rate: 1}, sampleRate, 160)
		if err != nil {
			t.Fatalf("overlayFileWindow: %v", err)
		}
		_ = floor.Close()
		_ = wide.Close()
		_ = narrow.Close()
		got := readWAVFloats(t, path)
		if mid := (from + to) / 2; got[mid] < 0.8 {
			t.Fatalf("the nested window is %v at its middle; the later segment did not win", got[mid])
		}
		// The earlier segment resumes on both sides of the nested one, so the
		// inner window is faded at both edges rather than left as a step.
		if got[from-1] > -0.4 {
			t.Fatalf("the earlier segment does not stand at sample %d: %v", from-1, got[from-1])
		}
		if got[to+1] > -0.4 {
			t.Fatalf("the earlier segment does not resume at sample %d: %v", to+1, got[to+1])
		}
	})
}

// A participant who rejoined has several tracks. They are disjoint in time — a
// rejoin is a new RTP identity for a span the previous one had ended — so
// summing them places rather than mixes, and the result must be the whole
// timeline whatever length the individual tracks are.
func TestWriteParticipantFloorSumsDisjointTracksAndPadsTheTimeline(t *testing.T) {
	const sampleRate = 16000
	const timeline = sampleRate * 6
	dir := t.TempDir()

	first := make([]float32, sampleRate*2)
	for i := range first {
		first[i] = 0.5
	}
	second := make([]float32, sampleRate*4)
	for i := sampleRate * 3; i < len(second); i++ {
		second[i] = -0.25
	}
	firstPath := filepath.Join(dir, "track-01.wav")
	secondPath := filepath.Join(dir, "track-02.wav")
	writeFloorWAV(t, firstPath, first, sampleRate)
	writeFloorWAV(t, secondPath, second, sampleRate)

	out := filepath.Join(dir, "render.wav")
	if err := writeParticipantFloor([]string{firstPath, secondPath}, timeline, sampleRate, out); err != nil {
		t.Fatalf("writeParticipantFloor: %v", err)
	}
	got := readWAVFloats(t, out)
	if len(got) != timeline {
		t.Fatalf("the floor is %d samples, want the whole %d-sample timeline", len(got), timeline)
	}
	if got[sampleRate] != 0.5 {
		t.Fatalf("the first track is %v where it should stand alone", got[sampleRate])
	}
	if got[sampleRate*5/2] != 0 {
		t.Fatalf("the gap between the two stints is %v, want silence", got[sampleRate*5/2])
	}
	if got[sampleRate*7/2] != -0.25 {
		t.Fatalf("the second track is %v where it should stand alone", got[sampleRate*7/2])
	}
	for i := sampleRate * 4; i < timeline; i++ {
		if got[i] != 0 {
			t.Fatalf("sample %d past both tracks is %v, want the zero padding", i, got[i])
		}
	}
	// The source tracks are read, never written.
	if again := readWAVFloats(t, firstPath); again[0] != 0.5 || len(again) != len(first) {
		t.Fatal("writing the floor modified a recorded track")
	}
}

// The whole reason the renderer works on files: a long meeting must not put a
// timeline in the heap. Two hours at 48 kHz would be 1.4 GB per buffer, and the
// splice used to need three of them.
func TestRenderingALongTimelineStaysBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a multi-hundred-megabyte file")
	}
	const sampleRate = 48000
	const minutes = 20
	const timeline = sampleRate * 60 * minutes

	dir := t.TempDir()
	floorPath := filepath.Join(dir, "floor.wav")
	f, err := os.Create(floorPath)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := writeWAVHeader(f, timeline, sampleRate); err != nil {
		t.Fatalf("header: %v", err)
	}
	if err := f.Truncate(44 + int64(timeline)*2); err != nil {
		t.Skipf("cannot make a %d MB fixture here: %v", timeline*2/1_000_000, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	srcPath := filepath.Join(dir, "src.wav")
	writeFloorWAV(t, srcPath, recordedMarker(sampleRate*60, 0.5), sampleRate)

	floor, err := openWAV(floorPath)
	if err != nil {
		t.Fatalf("openWAV: %v", err)
	}
	src, err := openWAV(srcPath)
	if err != nil {
		t.Fatalf("openWAV: %v", err)
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, _, err := overlayFileWindow(floor, src, sampleRate*60, Placement{OffsetMS: 300_000, Rate: 1.0001}, sampleRate, 720); err != nil {
		t.Fatalf("overlayFileWindow: %v", err)
	}
	runtime.ReadMemStats(&after)
	_ = floor.Close()
	_ = src.Close()

	// A minute of source at 48 kHz is 11.5 MB as float32 and the timeline is
	// 230 MB; the chunked renderer touches neither. Four megabytes is generous
	// room for the chunk buffers and the test's own bookkeeping, and still two
	// orders of magnitude below a regression to whole-timeline slices.
	const budget = 4 << 20
	if grew := after.TotalAlloc - before.TotalAlloc; grew > budget {
		t.Fatalf("the render allocated %d bytes over a %d-minute timeline; the bound is %d",
			grew, minutes, budget)
	}
}

// A window too short to be faded is left to the recorded track.
//
// The head weight is (head+1)/fade, so at three output samples or fewer every
// weight the formula can produce is 1 and the "crossfade" is a full-amplitude
// jump from the recorded track to the upload — exactly the click it exists to
// remove. Four samples is where a ramp first exists, so the boundary is walked
// one sample at a time. What a refused window costs is at most three samples of
// somebody's upload, 62 microseconds at the mix's rate, and their recorded
// audio stands there instead.
func TestAWindowTooShortToCrossfadeKeepsTheRecordedTrack(t *testing.T) {
	const sampleRate = 16000
	const timeline = sampleRate // one second
	const fade = 240            // 15 ms, the length the published mix asks for
	const floorValue = float32(0.25)
	const sourceValue = float32(-0.75)
	const srcSamples = 1000

	// The segment is placed to end exactly where the timeline does, so the
	// window it can cover is however many output samples are left after its
	// start — one, two, three or four.
	place := func(length int) Placement {
		return Placement{OffsetMS: float64(timeline-length) * 1000 / float64(sampleRate), Rate: 1}
	}
	render := func(t *testing.T, length, fadeSamples int) (int, int, []float32) {
		t.Helper()
		dir := t.TempDir()
		floorPath := filepath.Join(dir, "floor.wav")
		srcPath := filepath.Join(dir, "src.wav")
		writeFloorWAV(t, floorPath, recordedMarker(timeline, floorValue), sampleRate)
		writeFloorWAV(t, srcPath, recordedMarker(srcSamples, sourceValue), sampleRate)
		floor, err := openWAV(floorPath)
		if err != nil {
			t.Fatalf("openWAV: %v", err)
		}
		src, err := openWAV(srcPath)
		if err != nil {
			t.Fatalf("openWAV: %v", err)
		}
		from, to, err := overlayFileWindow(floor, src, srcSamples, place(length), sampleRate, fadeSamples)
		if err != nil {
			t.Fatalf("overlayFileWindow: %v", err)
		}
		if err := floor.Close(); err != nil {
			t.Fatalf("close floor: %v", err)
		}
		_ = src.Close()
		return from, to, readWAVFloats(t, floorPath)
	}

	for _, length := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("%d output sample(s) cannot be faded", length), func(t *testing.T) {
			from, to, got := render(t, length, fade)
			if to > from {
				t.Fatalf("a %d-sample window reported [%d, %d); it cannot be faded, so it must report nothing",
					length, from, to)
			}
			for i, sample := range got {
				if sample != floorValue {
					t.Fatalf("sample %d is %v; a window too short to fade must leave the recorded %v alone",
						i, sample, floorValue)
				}
			}
		})
	}

	t.Run("four output samples fade rather than step", func(t *testing.T) {
		from, to, got := render(t, 4, fade)
		if to-from != 4 {
			t.Fatalf("the window is [%d, %d), want four samples", from, to)
		}
		for i := 0; i < from; i++ {
			if got[i] != floorValue {
				t.Fatalf("sample %d before the window is %v, want the recorded %v", i, got[i], floorValue)
			}
		}
		// A ramp: the edges are between the two values rather than at either of
		// them, which is what "no step" means with two constant sources.
		for _, edge := range []int{from, to - 1} {
			if got[edge] <= sourceValue || got[edge] >= floorValue {
				t.Fatalf("sample %d is %v; the edge of the window neither faded in nor out", edge, got[edge])
			}
		}
		for i := from + 1; i < to-1; i++ {
			if math.Abs(float64(got[i]-sourceValue)) > 1.0/32768 {
				t.Fatalf("sample %d in the body of the window is %v, want the uploaded %v", i, got[i], sourceValue)
			}
		}
	})

	// The refusal belongs to the fade, not to the window: a caller that asked
	// for hard edges still gets every sample it asked for.
	t.Run("hard edges still splice three samples", func(t *testing.T) {
		from, to, got := render(t, 3, 0)
		if to-from != 3 {
			t.Fatalf("the window is [%d, %d), want the three samples a hard-edged overlay covers", from, to)
		}
		for i := from; i < to; i++ {
			if math.Abs(float64(got[i]-sourceValue)) > 1.0/32768 {
				t.Fatalf("sample %d is %v, want the uploaded %v", i, got[i], sourceValue)
			}
		}
	})
}
