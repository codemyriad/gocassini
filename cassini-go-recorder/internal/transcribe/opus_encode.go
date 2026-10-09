package transcribe

import (
	"bufio"
	"fmt"
	"io"
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
// They are also part of the meeting's identity. Annotations (the tags and
// marks people put on a meeting) are bound to the Opus packets
// (integrity.opusAudioSha256), and a rerun rebuilds the mix from the same
// capture. Rebuilding with different settings produces different
// packets, and the publish path then discards the marks people made on the
// previous version. So encode policies are versioned by name, a released
// policy is never changed in place (a new behaviour gets a new name), and a
// rebuild of an existing meeting asks for the policy that meeting was built
// with (`cassini build --audio-encode`, which the operator passes on reruns).
const (
	// AudioEncodeFixed64k is the encode every meeting got before D-850:
	// 64 kb/s VBR (variable bitrate: 64 kb/s is the average target, quiet
	// stretches cost less) whatever the sources carried, with libopus picking
	// the audio bandwidth. A manifest without an audioEncode record was built
	// with it.
	AudioEncodeFixed64k = "fixed-64k"
	// AudioEncodeMatchSource sizes bitrate and audio bandwidth (the highest
	// frequency kept) from what the sources carry; see
	// chooseMatchSourceEncode.
	AudioEncodeMatchSource = "match-source"
	// DefaultAudioEncodePolicy is what a first build of a recording uses.
	DefaultAudioEncodePolicy = AudioEncodeMatchSource
)

// AudioEncode is the Opus encode chosen for one meeting mix. It is recorded in
// the bundle's manifest.json so a rerun can tell which policy built it.
type AudioEncode struct {
	Policy     string `json:"policy"`
	BitrateBps int    `json:"bitrateBps"`
	// CutoffHz caps the audio bandwidth libopus may code (4000, 6000, 8000,
	// 12000 or 20000). Zero leaves the choice to libopus.
	CutoffHz int `json:"cutoffHz,omitempty"`
	// Application is libopus's -application tuning: "voip" reshapes the
	// signal for intelligibility on a phone line (it filters out low
	// frequencies and boosts the bands that carry vowels; the pre-D-850
	// choice), "audio" tries to reproduce the input as it is.
	Application string `json:"application"`
	// SourceBandwidthHz and SourceSpeechBitrateBps are the measurements the
	// choice was made from (zero when the policy does not look at sources, or
	// a measurement was unavailable). SourceSpeechBitrateBps is per coded
	// channel (see SourceAudio). Provenance only.
	SourceBandwidthHz      int `json:"sourceBandwidthHz,omitempty"`
	SourceSpeechBitrateBps int `json:"sourceSpeechBitrateBps,omitempty"`
}

// ValidAudioEncodePolicy reports whether name is a policy this build knows.
// The empty name means DefaultAudioEncodePolicy.
func ValidAudioEncodePolicy(name string) bool {
	switch strings.TrimSpace(name) {
	case "", AudioEncodeFixed64k, AudioEncodeMatchSource:
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
	// SpeechBitrateBps is the rate the source spent per coded channel while
	// there was something to code (see probeSpeechBitrates and
	// speechBitrateFromHistogram); 0 when unknown. Per channel because the mix
	// is mono: a participant whose client sends stereo at 64 kb/s spends part
	// of it on a second channel the downmix folds away. Halving is a sizing
	// rule, not an exact split (Opus codes the two channels jointly); on a
	// voice sent as 64 kb/s stereo and folded to mono, the 44 kb/s encode it
	// leads to scored within 0.03 of the old 64 kb/s one (PESQ-WB, ViSQOL).
	SpeechBitrateBps int
	// Empty marks a track with fewer than minSpeechPackets packets (about a
	// second of 20 ms Talk frames; often none at all: a participant who never
	// unmuted). It still counts for bandwidth but not for bitrate: there is
	// no voice in it for the mix to make room for.
	Empty bool
}

// opusBand is one libopus bandwidth with the bitrate range this policy allows
// for it. Opus names its bandwidths by the highest frequency they keep:
// narrowband 4 kHz (telephone), mediumband 6 kHz, wideband 8 kHz,
// super-wideband 12 kHz, fullband 20 kHz (everything people hear). Floors keep
// speech comfortably intelligible after a second lossy generation (the mix is
// coded again from sources that were already lossy-coded once); ceilings are
// where Opus speech stops improving audibly for that bandwidth (the fullband
// ceiling is the pre-D-850 fixed rate).
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
// 16 kHz / 24 kb/s AAC recording against its decoded source with PESQ-WB, the
// ITU speech quality score that predicts a listener rating from 1 (bad) to
// about 4.6 (no audible difference from the reference): the old fixed 64k
// scored 4.29, a 32k wideband encode 4.17, a 24k one 3.87.
const speechBitrateHeadroom = 4.0 / 3.0

// ChooseAudioEncode picks the Opus settings for a meeting mix under policy.
func ChooseAudioEncode(policy string, sources []SourceAudio) (AudioEncode, error) {
	switch strings.TrimSpace(policy) {
	case AudioEncodeFixed64k:
		return AudioEncode{Policy: AudioEncodeFixed64k, BitrateBps: 64000, Application: "voip"}, nil
	case "", AudioEncodeMatchSource:
		return chooseMatchSourceEncode(sources), nil
	default:
		return AudioEncode{}, fmt.Errorf("unknown audio encode policy %q (known: %s, %s)", policy, AudioEncodeMatchSource, AudioEncodeFixed64k)
	}
}

// chooseMatchSourceEncode never spends bits on what no source carries:
//
//   - Bandwidth: a source sampled at R Hz holds no frequency above R/2, so
//     the mix has nothing above the widest source's R/2 and libopus is told
//     to stop there (a 16 kHz phone recording is coded as wideband, not
//     fullband). Opus sources count as fullband (see fullbandHz).
//   - Bitrate: the mix carries roughly one speaker at a time, so it needs
//     about what the highest-rate single source spent on speech per channel
//     (the mix is mono; see SourceAudio.SpeechBitrateBps), plus headroom for
//     the re-encode, clamped to the band's floor and ceiling. Empty tracks
//     (see SourceAudio.Empty) are left out. A source with no usable
//     measurement (lossless, PCM, packets without durations), or a
//     recording with no track that is not empty, gets the ceiling, which for
//     fullband is the old fixed 64k, so nothing gets worse than before.
//   - Application "audio": "voip" reshapes the signal (see
//     AudioEncode.Application) rather than reproduce it. Against the decoded
//     sources, "audio" scored at least as well at every rate measured, and
//     far better on clean high-rate speech (PESQ-WB 4.25 vs 2.88 at 64k).
func chooseMatchSourceEncode(sources []SourceAudio) AudioEncode {
	bandwidthHz := 0
	speechBps := 0
	measured, counted := true, 0
	for _, s := range sources {
		bw := fullbandHz
		if !isOpusCodec(s.Codec) && s.SampleRate > 0 {
			bw = s.SampleRate / 2
		}
		bandwidthHz = max(bandwidthHz, bw)
		if s.Empty {
			continue
		}
		counted++
		if s.SpeechBitrateBps <= 0 {
			measured = false
		}
		speechBps = max(speechBps, s.SpeechBitrateBps)
	}
	if counted == 0 {
		measured = false
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
		Policy:                 AudioEncodeMatchSource,
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
	case "", AudioEncodeMatchSource:
	default:
		return ChooseAudioEncode(policy, nil)
	}
	rates, packets, err := probeSpeechBitrates(mkv, streams)
	if err != nil {
		return AudioEncode{}, err
	}
	sources := make([]SourceAudio, 0, len(streams))
	for _, s := range streams {
		sources = append(sources, SourceAudio{
			Codec:            s.Codec,
			SampleRate:       s.SampleRate,
			SpeechBitrateBps: rates[s.Index],
			Empty:            packets[s.Index] < minSpeechPackets,
		})
	}
	return ChooseAudioEncode(policy, sources)
}

// minSpeechPackets is the least a track must carry before its bitrate counts:
// about a second of 20 ms frames. Shorter tracks say nothing reliable, and
// hold too little sound to matter to the mix (see SourceAudio.Empty).
const minSpeechPackets = 50

// probeSpeechBitrates returns, per audio stream index, the bitrate the source
// spent per coded channel while coding sound, and how many packets it holds, from packet sizes alone (one
// demux pass, no decode). A whole-recording average would be meaningless for
// Talk tracks, which are mostly silence between a participant's turns; see
// speechBitrateFromHistogram.
//
// The channel count comes from each packet where it can. A Talk track's
// container always says two channels: WebRTC names Opus "opus/48000/2"
// whether or not the sender codes stereo, and the recorder writes that header
// (depacket's Ogg writer). What the sender actually coded is in the first
// byte of every Opus packet (its TOC byte, bit 2 set for stereo; RFC 6716
// 3.1), so when every stream is Opus the probe also asks ffprobe for packet
// data and reads that bit. Otherwise the stream's channel count is used,
// except for Opus, whose header count cannot be trusted and counts as one.
func probeSpeechBitrates(mkv string, streams []AudioStream) (rates, packets map[int]int, err error) {
	readTOC := len(streams) > 0
	fallbackChannels := make(map[int]int, len(streams))
	for _, s := range streams {
		if isOpusCodec(s.Codec) {
			fallbackChannels[s.Index] = 1
		} else {
			readTOC = false
			fallbackChannels[s.Index] = max(s.Channels, 1)
		}
	}
	args := []string{"-v", "error", "-select_streams", "a"}
	if readTOC {
		args = append(args, "-show_entries", "packet=stream_index,size,duration_time,data", "-show_data")
	} else {
		args = append(args, "-show_entries", "packet=stream_index,size,duration_time")
	}
	cmd := exec.Command("ffprobe", append(args, "-of", "csv=p=0", mkv)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("open packet probe: %w", err)
	}
	stderr := boundedBuffer{limit: 8192}
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start packet probe: %w", err)
	}
	hists, packets, scanErr := packetRateHistograms(stdout, fallbackChannels)
	if scanErr != nil {
		// Drain so ffprobe is not blocked on a full pipe before Wait.
		_, _ = io.Copy(io.Discard, stdout)
	}
	if err := cmd.Wait(); err != nil {
		return nil, nil, fmt.Errorf("ffprobe packets: %w\n%s", err, truncate(stderr.String(), 800))
	}
	if scanErr != nil {
		return nil, nil, fmt.Errorf("read packet probe: %w", scanErr)
	}
	rates = make(map[int]int, len(hists))
	for index, hist := range hists {
		rates[index] = speechBitrateFromHistogram(hist)
	}
	return rates, packets, nil
}

func isOpusCodec(codec string) bool {
	return strings.EqualFold(strings.TrimSpace(codec), "opus")
}

// packetRateHistograms reads ffprobe's csv packet listing (one
// "stream_index,duration_time,size[,data]" line per packet: ffprobe writes a
// section's fields in its own fixed order, not the order -show_entries names
// them in) into per-stream histograms of per-channel packet bitrates (bps ->
// packet count), and per-stream packet counts (every packet, with or without a
// duration). With -show_data each packet line is followed by a hex dump
// of the packet whose first line starts "00000000: "; its first byte is the
// Opus TOC byte. Every other dump line is ignored. A packet without a dump
// counts at its stream's fallbackChannels (one when missing).
func packetRateHistograms(r io.Reader, fallbackChannels map[int]int) (hists map[int]map[int]int, packets map[int]int, err error) {
	hists, packets = map[int]map[int]int{}, map[int]int{}
	type packet struct {
		index    int
		bps      float64
		channels int
	}
	var pending *packet
	flush := func() {
		if pending == nil {
			return
		}
		if hists[pending.index] == nil {
			hists[pending.index] = map[int]int{}
		}
		hists[pending.index][int(math.Round(pending.bps/float64(pending.channels)))]++
		pending = nil
	}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if toc, ok := strings.CutPrefix(line, "00000000: "); ok {
			if pending != nil && len(toc) >= 2 {
				if b, err := strconv.ParseUint(toc[:2], 16, 8); err == nil {
					pending.channels = 1
					if b&0x04 != 0 {
						pending.channels = 2
					}
				}
			}
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 3 {
			continue
		}
		index, err1 := strconv.Atoi(fields[0])
		if err1 != nil {
			continue // a hex dump line past the first
		}
		flush()
		packets[index]++
		duration, err2 := strconv.ParseFloat(fields[1], 64)
		size, err3 := strconv.Atoi(fields[2])
		if err2 != nil || err3 != nil || duration <= 0 || size < 0 {
			continue // N/A durations (a codec's priming packet) carry no rate
		}
		pending = &packet{index: index, bps: float64(size) * 8 / duration, channels: max(fallbackChannels[index], 1)}
	}
	flush()
	return hists, packets, scanner.Err()
}

// speechBitrateFromHistogram estimates the rate a track's encoder spent on
// sound, from a histogram of per-packet bitrates (bps -> packet count).
//
// The 90th-percentile packet is taken as "speaking"; the estimate is the mean
// of every packet at least half that rate. That drops the silence frames a VBR
// Opus track is mostly made of (including DTX, discontinuous transmission,
// where a WebRTC sender sends a tiny packet only now and then while its user
// is quiet; both cost a fraction of a speech frame) and keeps the speech, so a
// Talk track sent at 32 kb/s reads ~32 kb/s however long its owner was quiet,
// while a constant-rate source (AAC, MP3) reads its average. A participant
// whose speech fills under a tenth of their track's packets reads at about
// their silence rate instead; the mix is sized by the highest-rate track, so
// that only matters when this participant also sent at the highest rate.
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
