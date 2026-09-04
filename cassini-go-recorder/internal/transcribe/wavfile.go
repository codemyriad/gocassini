package transcribe

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
)

// A meeting-length mono WAV that can be read and written at any sample offset.
//
// The mix already materialises every recorded track as a full-timeline 48 kHz
// WAV on disk before it encodes anything (PrepareMix). Once the splice has to
// happen at that rate as well, "load the timeline into a []float32" stops being
// an option: one buffer is 1.4 GB for a two-hour meeting, and the splice needs
// several. Random access to the file is what makes the overlay bounded — it
// touches only the samples inside the window it is placing, a chunk at a time,
// and never holds more than one chunk of any timeline.
type wavFile struct {
	f *os.File
	// dataOffset is where sample zero starts. Not a constant 44: ffmpeg's WAV
	// muxer writes a LIST/INFO chunk between "fmt " and "data", so the header
	// has to be parsed rather than assumed.
	dataOffset int64
	samples    int
	sampleRate int
	// scratch is the byte buffer reads and writes convert through, kept rather
	// than allocated per call. A one-hour window is a thousand chunks, and
	// three fresh buffers each would be tens of megabytes of pure garbage on a
	// path whose whole claim is that it does not scale with the meeting. A
	// wavFile is used by one goroutine at a time.
	scratch []byte
}

// bytes returns the scratch buffer at exactly n bytes, growing it if needed.
func (w *wavFile) bytes(n int) []byte {
	if cap(w.scratch) < n {
		w.scratch = make([]byte, n)
	}
	return w.scratch[:n]
}

// wavChunkScanLimit bounds the RIFF walk. A well-formed WAV reaches "data"
// within a handful of chunks; anything else is refused rather than followed.
const wavChunkScanLimit = 64

// openWAV opens a mono 16-bit PCM WAV for random access.
//
// Deliberately strict. This is the floor a participant's upload gets laid over,
// and every offset arithmetic below assumes two bytes per sample, one channel.
// Anything it does not fully understand — RF64, a compressed payload, stereo —
// is an error, which the caller turns into "keeping the recorded audio" rather
// than into a silently mispositioned splice.
func openWAV(path string) (*wavFile, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open wav: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
		}
	}()

	var riff [12]byte
	if _, err := io.ReadFull(f, riff[:]); err != nil {
		return nil, fmt.Errorf("read wav header: %w", err)
	}
	if string(riff[0:4]) == "RF64" {
		// RF64 carries the real sizes in a ds64 chunk because they do not fit
		// in 32 bits. Refusing is right: a file that large is a recording far
		// longer than anything this pipeline is built for, and guessing at the
		// layout would place audio at the wrong offset.
		return nil, fmt.Errorf("wav is RF64, which this reader does not parse")
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return nil, fmt.Errorf("not a RIFF/WAVE file")
	}

	pos := int64(12)
	var channels, bits, sampleRate int
	for scanned := 0; scanned < wavChunkScanLimit; scanned++ {
		var head [8]byte
		if _, err := f.ReadAt(head[:], pos); err != nil {
			return nil, fmt.Errorf("wav has no data chunk: %w", err)
		}
		id := string(head[0:4])
		size := int64(binary.LittleEndian.Uint32(head[4:8]))
		body := pos + 8
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, fmt.Errorf("wav fmt chunk is %d bytes", size)
			}
			fmtBody := make([]byte, 16)
			if _, err := f.ReadAt(fmtBody, body); err != nil {
				return nil, fmt.Errorf("read wav fmt: %w", err)
			}
			format := binary.LittleEndian.Uint16(fmtBody[0:2])
			channels = int(binary.LittleEndian.Uint16(fmtBody[2:4]))
			sampleRate = int(binary.LittleEndian.Uint32(fmtBody[4:8]))
			bits = int(binary.LittleEndian.Uint16(fmtBody[14:16]))
			if format != 1 {
				return nil, fmt.Errorf("wav is format %d, not uncompressed PCM", format)
			}
		case "data":
			if channels != 1 || bits != 16 {
				return nil, fmt.Errorf("wav is %d-channel %d-bit, need mono 16-bit", channels, bits)
			}
			info, err := f.Stat()
			if err != nil {
				return nil, fmt.Errorf("stat wav: %w", err)
			}
			// The declared size, but never past the end of the file: a writer
			// killed mid-flush leaves a header promising more than it wrote,
			// and reading past it would return zeros as if they were audio.
			if available := info.Size() - body; size > available {
				size = available
			}
			if size < 0 {
				size = 0
			}
			ok = true
			return &wavFile{f: f, dataOffset: body, samples: int(size / 2), sampleRate: sampleRate}, nil
		}
		pos = body + size
		if size%2 == 1 {
			pos++ // RIFF chunks are word-aligned.
		}
	}
	return nil, fmt.Errorf("wav has no data chunk in its first %d chunks", wavChunkScanLimit)
}

func (w *wavFile) Close() error {
	if w == nil || w.f == nil {
		return nil
	}
	f := w.f
	w.f = nil
	return f.Close()
}

// readSamples fills dst with the samples starting at `at`, as floats in
// [-1, 1]. Positions past the end of the data chunk read as silence, so a
// participant who stopped talking before the meeting ended still has a floor
// under the rest of the timeline.
func (w *wavFile) readSamples(at int, dst []float32) error {
	if len(dst) == 0 {
		return nil
	}
	for i := range dst {
		dst[i] = 0
	}
	if at < 0 || at >= w.samples {
		return nil
	}
	n := len(dst)
	if at+n > w.samples {
		n = w.samples - at
	}
	raw := w.bytes(n * 2)
	if _, err := w.f.ReadAt(raw, w.dataOffset+int64(at)*2); err != nil && err != io.EOF {
		return fmt.Errorf("read wav samples: %w", err)
	}
	for i := 0; i < n; i++ {
		dst[i] = float32(int16(binary.LittleEndian.Uint16(raw[i*2:]))) / 32768.0
	}
	return nil
}

// writeSamples writes src at sample offset `at`.
func (w *wavFile) writeSamples(at int, src []float32) error {
	if len(src) == 0 {
		return nil
	}
	raw := w.bytes(len(src) * 2)
	for i, sample := range src {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(s16FromFloat32(sample)))
	}
	if _, err := w.f.WriteAt(raw, w.dataOffset+int64(at)*2); err != nil {
		return fmt.Errorf("write wav samples: %w", err)
	}
	return nil
}

// s16FromFloat32 converts one sample, rounded, with 32768 as the scale.
//
// 32768 matching the decoder, and rounded rather than truncated. The files this
// writes are SPLICES: most of their content is the participant's recorded
// track, decoded as s16/32768 and expected to survive untouched wherever no
// upload was laid over it. Scaling back by 32767 and truncating turned every
// one of those samples into a slightly different one — 16384 came back as
// 16383, and ±1 came back as zero — so "the recorded audio is unchanged
// outside the overlaid windows" was true in memory and false in the file the
// mix and the transcription pass actually read. The pair is exact in both
// directions now, and only the single value +1.0 has to be clamped because
// int16 has no positive counterpart to -32768.
func s16FromFloat32(sample float32) int16 {
	v := float64(sample)
	if v > 1 {
		v = 1
	} else if v < -1 {
		v = -1
	}
	scaled := math.Round(v * 32768)
	if scaled > 32767 {
		scaled = 32767
	}
	return int16(scaled)
}

// writeWAVHeader writes the 44-byte canonical mono 16-bit PCM header.
func writeWAVHeader(w io.Writer, samples, sampleRate int) error {
	dataBytes := samples * 2
	header := make([]byte, 0, 44)
	le32 := func(v uint32) []byte { return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)} }
	le16 := func(v uint16) []byte { return []byte{byte(v), byte(v >> 8)} }
	header = append(header, "RIFF"...)
	header = append(header, le32(uint32(36+dataBytes))...)
	header = append(header, "WAVEfmt "...)
	header = append(header, le32(16)...)
	header = append(header, le16(1)...) // PCM
	header = append(header, le16(1)...) // mono
	header = append(header, le32(uint32(sampleRate))...)
	header = append(header, le32(uint32(sampleRate*2))...) // byte rate
	header = append(header, le16(2)...)                    // block align
	header = append(header, le16(16)...)                   // bits per sample
	header = append(header, "data"...)
	header = append(header, le32(uint32(dataBytes))...)
	_, err := w.Write(header)
	return err
}
