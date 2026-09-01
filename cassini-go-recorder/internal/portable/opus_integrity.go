package portable

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"math"
)

const (
	opusIntegrityDomain = "org.cassini.opus-packets/1\x00"
	maxOggPacketBytes   = 64 << 20
)

// OpusAudioIntegrity identifies the compressed recording independently of its
// Ogg page layout and OpusTags metadata. The digest covers OpusHead, every
// compressed audio packet with an explicit length prefix, the packet count,
// and the normalized playable sample count. OpusTags and raw Ogg framing
// (including granule positions) are deliberately excluded because Cassini
// embeds the manifest in OpusTags and FFmpeg can normalize Ogg framing during a
// metadata-only stream copy.
type OpusAudioIntegrity struct {
	SHA256      string
	SampleRate  int
	Channels    int
	SampleCount int64
	DurationMS  int64
	PacketCount uint64
}

// ComputeOpusAudioIntegrity computes Cassini's canonical compressed-audio
// digest directly from a single-stream Ogg Opus file. It does not decode audio
// and therefore does not depend on FFmpeg's decoder implementation or sample
// rounding behavior.
func ComputeOpusAudioIntegrity(reader io.Reader) (OpusAudioIntegrity, error) {
	digest := sha256.New()
	_, _ = digest.Write([]byte(opusIntegrityDomain))

	var (
		partial          []byte
		packetIndex      int
		audioPackets     uint64
		decodedSamples   uint64
		preSkip          uint64
		finalGranule     uint64
		haveFinalGranule bool
		channels         int
		streamSerial     uint32
		expectedSequence uint32
		haveStream       bool
		sawEOS           bool
	)

	for pageIndex := 0; ; pageIndex++ {
		header := make([]byte, 27)
		n, err := io.ReadFull(reader, header)
		if err == io.EOF && n == 0 {
			break
		}
		if err != nil {
			return OpusAudioIntegrity{}, fmt.Errorf("read Ogg page %d header: %w", pageIndex, err)
		}
		if sawEOS {
			return OpusAudioIntegrity{}, fmt.Errorf("Ogg data continues after end-of-stream page")
		}
		if string(header[:4]) != "OggS" {
			return OpusAudioIntegrity{}, fmt.Errorf("invalid Ogg capture pattern on page %d", pageIndex)
		}
		if header[4] != 0 {
			return OpusAudioIntegrity{}, fmt.Errorf("unsupported Ogg bitstream version %d", header[4])
		}

		headerType := header[5]
		serial := binary.LittleEndian.Uint32(header[14:18])
		sequence := binary.LittleEndian.Uint32(header[18:22])
		if !haveStream {
			if headerType&0x02 == 0 {
				return OpusAudioIntegrity{}, fmt.Errorf("first Ogg page is not beginning-of-stream")
			}
			streamSerial = serial
			expectedSequence = sequence
			haveStream = true
		} else {
			if serial != streamSerial {
				return OpusAudioIntegrity{}, fmt.Errorf("chained or multiplexed Ogg streams are not supported")
			}
			expectedSequence++
			if sequence != expectedSequence {
				return OpusAudioIntegrity{}, fmt.Errorf("Ogg page sequence jumped from %d to %d", expectedSequence-1, sequence)
			}
			if headerType&0x02 != 0 {
				return OpusAudioIntegrity{}, fmt.Errorf("unexpected beginning-of-stream flag on page %d", pageIndex)
			}
		}

		continued := headerType&0x01 != 0
		if continued != (len(partial) > 0) {
			return OpusAudioIntegrity{}, fmt.Errorf("invalid continued-packet flag on Ogg page %d", pageIndex)
		}

		segmentTable := make([]byte, int(header[26]))
		if _, err := io.ReadFull(reader, segmentTable); err != nil {
			return OpusAudioIntegrity{}, fmt.Errorf("read Ogg page %d segment table: %w", pageIndex, err)
		}
		payloadBytes := 0
		for _, size := range segmentTable {
			payloadBytes += int(size)
		}
		payload := make([]byte, payloadBytes)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return OpusAudioIntegrity{}, fmt.Errorf("read Ogg page %d payload: %w", pageIndex, err)
		}
		if err := validateOggPageChecksum(header, segmentTable, payload); err != nil {
			return OpusAudioIntegrity{}, fmt.Errorf("Ogg page %d: %w", pageIndex, err)
		}

		cursor := 0
		completedAudioPacket := false
		for _, segmentSize := range segmentTable {
			size := int(segmentSize)
			if len(partial)+size > maxOggPacketBytes {
				return OpusAudioIntegrity{}, fmt.Errorf("Ogg packet exceeds %d bytes", maxOggPacketBytes)
			}
			partial = append(partial, payload[cursor:cursor+size]...)
			cursor += size
			if segmentSize == 255 {
				continue
			}

			switch packetIndex {
			case 0:
				canonicalHead, parsedChannels, parsedPreSkip, err := canonicalOpusHead(partial)
				if err != nil {
					return OpusAudioIntegrity{}, err
				}
				channels = parsedChannels
				preSkip = parsedPreSkip
				hashOpusPacket(digest, 'H', canonicalHead)
			case 1:
				if len(partial) < 8 || string(partial[:8]) != "OpusTags" {
					return OpusAudioIntegrity{}, fmt.Errorf("second Ogg packet is not OpusTags")
				}
				// Excluded: this packet contains the manifest that carries the
				// digest, so hashing it would make the format self-referential.
			default:
				packetSamples, err := opusPacketSampleCount(partial)
				if err != nil {
					return OpusAudioIntegrity{}, fmt.Errorf("invalid Opus audio packet %d: %w", audioPackets, err)
				}
				if decodedSamples > math.MaxUint64-packetSamples {
					return OpusAudioIntegrity{}, fmt.Errorf("Opus decoded sample count overflow")
				}
				decodedSamples += packetSamples
				hashOpusPacket(digest, 'A', partial)
				audioPackets++
				completedAudioPacket = true
			}
			packetIndex++
			partial = nil
		}

		if headerType&0x04 != 0 {
			granule := binary.LittleEndian.Uint64(header[6:14])
			if !completedAudioPacket {
				return OpusAudioIntegrity{}, fmt.Errorf("end-of-stream Ogg page completes no Opus audio packet")
			}
			if granule == ^uint64(0) || granule > math.MaxInt64 {
				return OpusAudioIntegrity{}, fmt.Errorf("invalid final Opus granule %d", granule)
			}
			finalGranule = granule
			haveFinalGranule = true
			sawEOS = true
		}
	}

	if !haveStream {
		return OpusAudioIntegrity{}, fmt.Errorf("empty Ogg stream")
	}
	if len(partial) != 0 {
		return OpusAudioIntegrity{}, fmt.Errorf("truncated final Ogg packet")
	}
	if packetIndex < 2 {
		return OpusAudioIntegrity{}, fmt.Errorf("Ogg Opus stream is missing header packets")
	}
	if audioPackets == 0 {
		return OpusAudioIntegrity{}, fmt.Errorf("Ogg Opus stream contains no audio packets")
	}
	if !sawEOS {
		return OpusAudioIntegrity{}, fmt.Errorf("Ogg Opus stream has no end-of-stream page")
	}
	if !haveFinalGranule || finalGranule < preSkip {
		return OpusAudioIntegrity{}, fmt.Errorf("invalid final Opus granule %d with pre-skip %d", finalGranule, preSkip)
	}

	if decodedSamples < preSkip {
		return OpusAudioIntegrity{}, fmt.Errorf("Opus packets contain %d samples, less than pre-skip %d", decodedSamples, preSkip)
	}
	packetSampleCount := decodedSamples - preSkip
	granuleSampleCount := finalGranule - preSkip
	sampleCount := packetSampleCount
	if granuleSampleCount < sampleCount {
		sampleCount = granuleSampleCount
	}
	if sampleCount > math.MaxInt64 {
		return OpusAudioIntegrity{}, fmt.Errorf("Opus playable sample count overflows int64: %d", sampleCount)
	}

	var trailer [17]byte
	trailer[0] = 'E'
	binary.LittleEndian.PutUint64(trailer[1:9], audioPackets)
	binary.LittleEndian.PutUint64(trailer[9:17], sampleCount)
	_, _ = digest.Write(trailer[:])

	playableSamples := int64(sampleCount)
	return OpusAudioIntegrity{
		SHA256:      hex.EncodeToString(digest.Sum(nil)),
		SampleRate:  48000,
		Channels:    channels,
		SampleCount: playableSamples,
		DurationMS:  playableSamples * 1000 / 48000,
		PacketCount: audioPackets,
	}, nil
}

// opusPacketSampleCount returns the packet's decoded duration at Opus's fixed
// 48 kHz clock. It follows opus_packet_get_nb_samples: the TOC byte selects a
// frame duration and coding mode; the frame-count code selects 1, 2, or the
// explicit count in byte 1. A packet may not exceed Opus's 120 ms limit.
func opusPacketSampleCount(packet []byte) (uint64, error) {
	if len(packet) == 0 {
		return 0, fmt.Errorf("empty packet")
	}
	toc := packet[0]
	var samplesPerFrame uint64
	switch {
	case toc&0x80 != 0:
		samplesPerFrame = uint64(48000<<((toc>>3)&0x03)) / 400
	case toc&0x60 == 0x60:
		if toc&0x08 != 0 {
			samplesPerFrame = 48000 / 50
		} else {
			samplesPerFrame = 48000 / 100
		}
	default:
		durationCode := (toc >> 3) & 0x03
		if durationCode == 3 {
			samplesPerFrame = 48000 * 60 / 1000
		} else {
			samplesPerFrame = uint64(48000<<durationCode) / 100
		}
	}

	var frameCount uint64
	switch toc & 0x03 {
	case 0:
		frameCount = 1
	case 1, 2:
		frameCount = 2
	case 3:
		if len(packet) < 2 {
			return 0, fmt.Errorf("frame-count code 3 has no count byte")
		}
		frameCount = uint64(packet[1] & 0x3f)
		if frameCount == 0 {
			return 0, fmt.Errorf("frame-count code 3 declares zero frames")
		}
	}
	total := samplesPerFrame * frameCount
	if total == 0 || total > 48000*120/1000 {
		return 0, fmt.Errorf("packet duration is %d samples, outside 0..120 ms", total)
	}
	return total, nil
}

// canonicalOpusHead validates the stream-identification packet and clears its
// informational input-rate field. RFC 7845 says decoders must not use that
// field for playback, so two otherwise identical Opus streams remain the same
// Cassini audio when only the source-rate hint differs. Playback-relevant
// fields such as pre-skip, output gain, mapping family, and channel map remain
// covered by the digest.
func canonicalOpusHead(packet []byte) ([]byte, int, uint64, error) {
	if len(packet) < 19 || string(packet[:8]) != "OpusHead" {
		return nil, 0, 0, fmt.Errorf("first Ogg packet is not a valid OpusHead")
	}
	version := packet[8]
	if version == 0 || version&0xf0 != 0 {
		return nil, 0, 0, fmt.Errorf("unsupported OpusHead version %d", version)
	}
	channels := int(packet[9])
	if channels != 1 && channels != 2 {
		return nil, 0, 0, fmt.Errorf("portable Opus must have one or two channels, got %d", channels)
	}

	mappingFamily := packet[18]
	if mappingFamily == 0 {
		if len(packet) != 19 {
			return nil, 0, 0, fmt.Errorf("OpusHead mapping family 0 has %d bytes, want 19", len(packet))
		}
	} else {
		want := 21 + channels
		if len(packet) != want {
			return nil, 0, 0, fmt.Errorf("mapped OpusHead has %d bytes, want %d", len(packet), want)
		}
		streamCount := int(packet[19])
		coupledCount := int(packet[20])
		if streamCount == 0 || coupledCount > streamCount || streamCount+coupledCount > 255 {
			return nil, 0, 0, fmt.Errorf("invalid OpusHead stream mapping: streams=%d coupled=%d", streamCount, coupledCount)
		}
		codedChannels := streamCount + coupledCount
		for _, mapping := range packet[21:] {
			if int(mapping) >= codedChannels && mapping != 255 {
				return nil, 0, 0, fmt.Errorf("invalid OpusHead channel mapping %d for %d coded channels", mapping, codedChannels)
			}
		}
	}

	canonical := append([]byte(nil), packet...)
	clear(canonical[12:16])
	return canonical, channels, uint64(binary.LittleEndian.Uint16(packet[10:12])), nil
}

func validateOggPageChecksum(header, segmentTable, payload []byte) error {
	want := binary.LittleEndian.Uint32(header[22:26])
	crc := uint32(0)
	for index, value := range header {
		if index >= 22 && index < 26 {
			value = 0
		}
		crc = oggCRCStep(crc, value)
	}
	for _, value := range segmentTable {
		crc = oggCRCStep(crc, value)
	}
	for _, value := range payload {
		crc = oggCRCStep(crc, value)
	}
	if crc != want {
		return fmt.Errorf("checksum mismatch: header=%08x actual=%08x", want, crc)
	}
	return nil
}

func oggCRCStep(crc uint32, value byte) uint32 {
	crc ^= uint32(value) << 24
	for bit := 0; bit < 8; bit++ {
		if crc&0x80000000 != 0 {
			crc = crc<<1 ^ 0x04c11db7
		} else {
			crc <<= 1
		}
	}
	return crc
}

func hashOpusPacket(digest hash.Hash, kind byte, packet []byte) {
	var header [9]byte
	header[0] = kind
	binary.LittleEndian.PutUint64(header[1:], uint64(len(packet)))
	_, _ = digest.Write(header[:])
	_, _ = digest.Write(packet)
}
