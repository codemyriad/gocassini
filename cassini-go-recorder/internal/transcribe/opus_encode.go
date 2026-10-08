package transcribe

import (
	"bufio"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// The meeting mix (meeting.webm) is the only Opus encode a meeting gets:
// `cassini pack` copies its packets into the published .opus untouched, so the
// settings chosen here decide what every published meeting costs to store.
//
// They are also part of the meeting's identity. Annotations are bound to the
// Opus packets (integrity.opusAudioSha256), and a rerun rebuilds the mix from
// the same capture. Rebuilding with different settings produces different
// packets, and the publish path then discards the marks people made on the
// previous version. So encode policies are versioned by name, a released
// policy is never changed in place (a new behaviour gets a new name), and a
// rebuild of an existing meeting asks for the policy that meeting was built
// with (`cassini build --audio-encode`, which the operator passes on reruns).
const (
	// AudioEncodeFixed64k is the encode every meeting got before D-850:
	// 64 kb/s VBR whatever the sources carried, with libopus picking the
	// audio bandwidth. A manifest without an audioEncode record was built
	// with it.
	AudioEncodeFixed64k = "fixed-64k"
	// AudioEncodeSourceV1 sizes bitrate and audio bandwidth from the sources;
	// see chooseSourceV1Encode.
	AudioEncodeSourceV1 = "source-v1"
	// DefaultAudioEncodePolicy is what a first build of a recording uses.
	DefaultAudioEncodePolicy = AudioEncodeSourceV1
)

// AudioEncode is the Opus encode chosen for one meeting mix. It is recorded in
// the bundle's manifest.json so a rerun can tell which policy built it.
type AudioEncode struct {
	Policy     string `json:"policy"`
	BitrateBps int    `json:"bitrateBps"`
	// CutoffHz caps the audio bandwidth libopus may code (4000, 6000, 8000,
	// 12000 or 20000). Zero leaves the choice to libopus.
	CutoffHz int `json:"cutoffHz,omitempty"`
	// Application is libopus's -application: "voip" (high-pass filter and
	// formant emphasis, the pre-D-850 choice) or "audio" (codes what is there).
	Application string `json:"application"`
	// SourceBandwidthHz and SourceSpeechBitrateBps are the measurements the
	// choice was made from (zero when the policy does not look at sources, or
	// a measurement was unavailable). Provenance only.
	SourceBandwidthHz      int `json:"sourceBandwidthHz,omitempty"`
	SourceSpeechBitrateBps int `json:"sourceSpeechBitrateBps,omitempty"`
}

// ValidAudioEncodePolicy reports whether name is a policy this build knows.
// The empty name means DefaultAudioEncodePolicy.
func ValidAudioEncodePolicy(name string) bool {
	switch strings.TrimSpace(name) {
	case "", AudioEncodeFixed64k, AudioEncodeSourceV1:
		return true
	}
	return false
}

// libopusArgs is the encoder half of the mix's ffmpeg command. The
// fixed-64k list must stay byte-for-byte what MixDownToWebM ran before D-850:
// any change to it re-encodes, and so un-binds the marks of, every meeting
// published before then.
func (e AudioEncode) libopusArgs() []string {
	args := []string{
		"-c:a", "libopus",
		"-b:a", strconv.Itoa(e.BitrateBps/1000) + "k",
		"-vbr", "on",
		"-compression_level", "10",
		"-application", e.Application,
	}
	if e.CutoffHz > 0 {
		args = append(args, "-cutoff", strconv.Itoa(e.CutoffHz))
	}
	return args
}

// SourceAudio is what the encode policy needs to know about one source track.
type SourceAudio struct {
	Codec      string
	SampleRate int
	// SpeechBitrateBps is the rate the source spent while there was something
	// to code (see speechBitrateFromHistogram); 0 when unknown.
	SpeechBitrateBps int
}

// opusBand is one libopus bandwidth with the bitrate range this policy allows
// for it. Floors keep speech comfortably intelligible after a second lossy
// generation; ceilings are where Opus speech stops improving audibly for that
// bandwidth (the full-band ceiling is the pre-D-850 fixed rate).
type opusBand struct {
	cutoffHz   int
	floorBps   int
	ceilingBps int
}

var opusBands = []opusBand{
	{cutoffHz: 4000, floorBps: 12000, ceilingBps: 16000},  // narrowband: 8 kHz sources
	{cutoffHz: 6000, floorBps: 12000, ceilingBps: 20000},  // mediumband: 12 kHz sources
	{cutoffHz: 8000, floorBps: 16000, ceilingBps: 32000},  // wideband: 16 kHz sources
	{cutoffHz: 12000, floorBps: 20000, ceilingBps: 48000}, // super-wideband: 24 kHz sources
	{cutoffHz: 20000, floorBps: 24000, ceilingBps: 64000}, // fullband
}

// fullbandHz is assumed for a source whose bandwidth cannot be read from its
// sample rate. Opus always decodes at 48 kHz, and a WebRTC sender does not
// tell the container how much of that it used, so a Talk track counts as
// fullband and only its bitrate narrows the encode.
const fullbandHz = 20000

// speechBitrateHeadroom covers the second lossy generation. Measured on a
// 16 kHz / 24 kb/s AAC recording against its decoded source (PESQ-WB): the
// old fixed 64k scored 4.29, a 32k wideband encode 4.17, a 24k one 3.87.
const speechBitrateHeadroom = 4.0 / 3.0

// ChooseAudioEncode picks the Opus settings for a meeting mix under policy.
func ChooseAudioEncode(policy string, sources []SourceAudio) (AudioEncode, error) {
	switch strings.TrimSpace(policy) {
	case AudioEncodeFixed64k:
		return AudioEncode{Policy: AudioEncodeFixed64k, BitrateBps: 64000, Application: "voip"}, nil
	case "", AudioEncodeSourceV1:
		return chooseSourceV1Encode(sources), nil
	default:
		return AudioEncode{}, fmt.Errorf("unknown audio encode policy %q (known: %s, %s)", policy, AudioEncodeSourceV1, AudioEncodeFixed64k)
	}
}

// chooseSourceV1Encode never spends bits on what no source carries:
//
//   - Bandwidth: the mix cannot contain frequencies above the widest source's
//     Nyquist limit, so libopus is told to stop there (a 16 kHz phone
//     recording is coded as wideband, not fullband). Opus sources count as
//     fullband (see fullbandHz).
//   - Bitrate: the mix carries roughly one speaker at a time, so it needs about
//     what the richest single source spent on speech, plus headroom for the
//     re-encode, clamped to the band's floor and ceiling. A source with no
//     usable measurement (lossless, PCM, too short) gets the ceiling, which
//     for fullband is the old fixed 64k, so nothing gets worse than before.
//   - Application "audio": "voip" mode's high-pass and formant emphasis
//     reshape the signal rather than code it. Against the decoded sources,
//     "audio" scored at least as well at every rate measured, and far better
//     on clean high-rate speech (PESQ-WB 4.25 vs 2.88 at 64k).
func chooseSourceV1Encode(sources []SourceAudio) AudioEncode {
	bandwidthHz := 0
	speechBps := 0
	measured := len(sources) > 0
	for _, s := range sources {
		bw := fullbandHz
		if !strings.EqualFold(strings.TrimSpace(s.Codec), "opus") && s.SampleRate > 0 {
			bw = s.SampleRate / 2
		}
		bandwidthHz = max(bandwidthHz, bw)
		if s.SpeechBitrateBps <= 0 {
			measured = false
		}
		speechBps = max(speechBps, s.SpeechBitrateBps)
	}
	if bandwidthHz == 0 {
		bandwidthHz = fullbandHz
	}
	band := opusBands[len(opusBands)-1]
	for _, b := range opusBands {
		if bandwidthHz <= b.cutoffHz {
			band = b
			break
		}
	}
	bitrate := band.ceilingBps
	if measured {
		// Whole kb/s: the encoder's -b:a is given in k, and a recorded value
		// should read the same as the command that used it.
		want := int(math.Ceil(float64(speechBps)*speechBitrateHeadroom/1000)) * 1000
		bitrate = min(max(want, band.floorBps), band.ceilingBps)
	}
	return AudioEncode{
		Policy:                 AudioEncodeSourceV1,
		BitrateBps:             bitrate,
		CutoffHz:               band.cutoffHz,
		Application:            "audio",
		SourceBandwidthHz:      bandwidthHz,
		SourceSpeechBitrateBps: speechBps,
	}
}

// ChooseMeetingAudioEncode measures the recording's audio streams and picks
// the mix encode under policy. The fixed policy measures nothing.
func ChooseMeetingAudioEncode(mkv string, streams []AudioStream, policy string) (AudioEncode, error) {
	switch strings.TrimSpace(policy) {
	case "", AudioEncodeSourceV1:
	default:
		return ChooseAudioEncode(policy, nil)
	}
	rates, err := probeSpeechBitrates(mkv)
	if err != nil {
		return AudioEncode{}, err
	}
	sources := make([]SourceAudio, 0, len(streams))
	for _, s := range streams {
		sources = append(sources, SourceAudio{Codec: s.Codec, SampleRate: s.SampleRate, SpeechBitrateBps: rates[s.Index]})
	}
	return ChooseAudioEncode(policy, sources)
}

// minSpeechPackets is the least a track must carry before its bitrate counts:
// about a second of 20 ms frames. Shorter tracks say nothing reliable.
const minSpeechPackets = 50

// probeSpeechBitrates returns, per audio stream index, the bitrate the source
// spent while coding sound, from packet sizes alone (one demux pass, no
// decode). A whole-recording average would be meaningless for Talk tracks,
// which are mostly silence between a participant's turns; see
// speechBitrateFromHistogram.
func probeSpeechBitrates(mkv string) (map[int]int, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "a",
		"-show_entries", "packet=stream_index,size,duration_time",
		"-of", "csv=p=0",
		mkv,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open packet probe: %w", err)
	}
	stderr := boundedBuffer{limit: 8192}
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start packet probe: %w", err)
	}
	hists := map[int]map[int]int{}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		// csv with p=0 keeps -show_entries order regardless of how it was
		// requested: stream_index, duration_time, size.
		fields := strings.Split(strings.TrimSpace(scanner.Text()), ",")
		if len(fields) < 3 {
			continue
		}
		index, err1 := strconv.Atoi(fields[0])
		duration, err2 := strconv.ParseFloat(fields[1], 64)
		size, err3 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || err3 != nil || duration <= 0 || size < 0 {
			continue // N/A durations (a codec's priming packet) carry no rate
		}
		if hists[index] == nil {
			hists[index] = map[int]int{}
		}
		hists[index][int(math.Round(float64(size)*8/duration))]++
	}
	scanErr := scanner.Err()
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("ffprobe packets: %w\n%s", err, truncate(stderr.String(), 800))
	}
	if scanErr != nil {
		return nil, fmt.Errorf("read packet probe: %w", scanErr)
	}
	rates := make(map[int]int, len(hists))
	for index, hist := range hists {
		rates[index] = speechBitrateFromHistogram(hist)
	}
	return rates, nil
}

// speechBitrateFromHistogram estimates the rate a track's encoder spent on
// sound, from a histogram of per-packet bitrates (bps -> packet count).
//
// The 90th-percentile packet is taken as "speaking" even for a participant
// who talks a tenth of the time; the estimate is the mean of every packet at
// least half that rate. That drops the silence/DTX frames a VBR Opus track is
// mostly made of (they cost a fraction of a speech frame) and keeps the
// speech, so a Talk track sent at 32 kb/s reads ~32 kb/s however long its
// owner was quiet, while a constant-rate source (AAC, MP3) reads its average.
func speechBitrateFromHistogram(hist map[int]int) int {
	total := 0
	keys := make([]int, 0, len(hist))
	for rate, n := range hist {
		keys = append(keys, rate)
		total += n
	}
	if total < minSpeechPackets {
		return 0
	}
	sort.Ints(keys)
	p90Rank := int(0.9 * float64(total))
	p90, seen := 0, 0
	for _, rate := range keys {
		seen += hist[rate]
		if seen > p90Rank {
			p90 = rate
			break
		}
	}
	var sum, count float64
	for _, rate := range keys {
		if 2*rate >= p90 {
			sum += float64(rate) * float64(hist[rate])
			count += float64(hist[rate])
		}
	}
	if count == 0 {
		return 0
	}
	return int(math.Round(sum / count))
}
