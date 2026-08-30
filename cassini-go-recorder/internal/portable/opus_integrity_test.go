package portable

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestOpusIntegrityIgnoresTagsPageLayoutAndStreamSerial(t *testing.T) {
	head := testOpusHead(1, 312)
	audioA := []byte{0xf8, 0xff, 0xfe}
	audioB := []byte{0x78, 0x01, 0x02, 0x03}

	first := joinOggPages(
		testOggPage(0x02, 11, 0, 0, head),
		testOggPage(0, 11, 1, 0, testOpusTags("first metadata")),
		testOggPage(0, 11, 2, 960, audioA),
		testOggPage(0x04, 11, 3, 2232, audioB),
	)
	second := joinOggPages(
		testOggPage(0x02, 99, 40, 0, head, testOpusTags("different and much longer metadata")),
		testOggPage(0x04, 99, 41, 2232, audioA, audioB),
	)

	gotFirst, err := ComputeOpusAudioIntegrity(bytes.NewReader(first))
	if err != nil {
		t.Fatalf("first integrity: %v", err)
	}
	gotSecond, err := ComputeOpusAudioIntegrity(bytes.NewReader(second))
	if err != nil {
		t.Fatalf("second integrity: %v", err)
	}
	if gotFirst.SHA256 != gotSecond.SHA256 {
		t.Fatalf("metadata/page layout changed digest: %s != %s", gotFirst.SHA256, gotSecond.SHA256)
	}
	const goldenDigest = "afbd39fed4a87ca2fd82a10ed361ec5225c4e05efc843695ee153328317aaa2f"
	if gotFirst.SHA256 != goldenDigest {
		t.Fatalf("canonical digest = %s, want golden %s", gotFirst.SHA256, goldenDigest)
	}
	if gotFirst.SampleCount != 1608 || gotFirst.DurationMS != 33 || gotFirst.PacketCount != 2 {
		t.Fatalf("unexpected integrity summary: %+v", gotFirst)
	}
}

func TestOpusIntegrityPreservesPacketBoundaries(t *testing.T) {
	head := testOpusHead(1, 0)
	one := joinOggPages(
		testOggPage(0x02, 1, 0, 0, head, testOpusTags("tags")),
		testOggPage(0x04, 1, 1, 1920, []byte{0, 0}, []byte{2}),
	)
	two := joinOggPages(
		testOggPage(0x02, 2, 0, 0, head, testOpusTags("tags")),
		testOggPage(0x04, 2, 1, 1920, []byte{0}, []byte{0, 2}),
	)

	digestOne, err := ComputeOpusAudioIntegrity(bytes.NewReader(one))
	if err != nil {
		t.Fatal(err)
	}
	digestTwo, err := ComputeOpusAudioIntegrity(bytes.NewReader(two))
	if err != nil {
		t.Fatal(err)
	}
	if digestOne.SHA256 == digestTwo.SHA256 {
		t.Fatal("different Opus packet boundaries produced the same digest")
	}
}

func TestOpusIntegrityIgnoresInformationalInputRate(t *testing.T) {
	build := func(inputRate uint32) []byte {
		head := testOpusHead(1, 312)
		binary.LittleEndian.PutUint32(head[12:16], inputRate)
		return joinOggPages(
			testOggPage(0x02, 5, 0, 0, head, testOpusTags("tags")),
			testOggPage(0x04, 5, 1, 1272, []byte{0xf8, 0xff, 0xfe}),
		)
	}
	first, err := ComputeOpusAudioIntegrity(bytes.NewReader(build(16000)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ComputeOpusAudioIntegrity(bytes.NewReader(build(48000)))
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 != second.SHA256 {
		t.Fatal("informational OpusHead input rate changed digest")
	}
}

func TestOpusIntegrityHandlesTagsAcrossPages(t *testing.T) {
	tags := testOpusTags(string(bytes.Repeat([]byte{'x'}, 300)))
	firstTags := tags[:255]
	lastTags := tags[255:]
	stream := joinOggPages(
		testOggPage(0x02, 17, 0, 0, testOpusHead(1, 312)),
		testOggPageWithSegments(0, 17, 1, 0, []byte{255}, firstTags),
		testOggPageWithSegments(0x05, 17, 2, 1272, []byte{byte(len(lastTags)), 3}, append(lastTags, 0xf8, 0xff, 0xfe)),
	)

	got, err := ComputeOpusAudioIntegrity(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if got.PacketCount != 1 || got.SampleCount != 648 {
		t.Fatalf("unexpected integrity: %+v", got)
	}
}

func TestOpusIntegrityExcludesOggGranuleButReportsEndTrim(t *testing.T) {
	build := func(finalGranule uint64) []byte {
		return joinOggPages(
			testOggPage(0x02, 7, 0, 0, testOpusHead(1, 312), testOpusTags("tags")),
			testOggPage(0x04, 7, 1, finalGranule, []byte{0xf8, 0xff, 0xfe}),
		)
	}
	first, err := ComputeOpusAudioIntegrity(bytes.NewReader(build(1272)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ComputeOpusAudioIntegrity(bytes.NewReader(build(2232)))
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 != second.SHA256 {
		t.Fatal("Ogg granule framing changed compressed-packet digest")
	}
	if first.SampleCount != 648 || second.SampleCount != 648 {
		t.Fatalf("sample counts = %d and %d", first.SampleCount, second.SampleCount)
	}
	trimmed, err := ComputeOpusAudioIntegrity(bytes.NewReader(build(900)))
	if err != nil {
		t.Fatal(err)
	}
	if trimmed.SHA256 == first.SHA256 || trimmed.SampleCount != 588 {
		t.Fatalf("trimmed integrity = %+v, want a distinct digest and 588 samples", trimmed)
	}
}

func TestOpusIntegrityCoversPlaybackRelevantHeader(t *testing.T) {
	build := func(outputGain int16) []byte {
		head := testOpusHead(1, 312)
		binary.LittleEndian.PutUint16(head[16:18], uint16(outputGain))
		return joinOggPages(
			testOggPage(0x02, 7, 0, 0, head, testOpusTags("tags")),
			testOggPage(0x04, 7, 1, 1272, []byte{0xf8, 0xff, 0xfe}),
		)
	}
	first, err := ComputeOpusAudioIntegrity(bytes.NewReader(build(0)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ComputeOpusAudioIntegrity(bytes.NewReader(build(256)))
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 == second.SHA256 {
		t.Fatal("playback-relevant Opus output gain did not change digest")
	}
}

func TestOpusIntegrityRejectsBadOggChecksum(t *testing.T) {
	stream := joinOggPages(
		testOggPage(0x02, 1, 0, 0, testOpusHead(1, 0), testOpusTags("tags")),
		testOggPage(0x04, 1, 1, 960, []byte{1, 2, 3}),
	)
	stream[len(stream)-1] ^= 0xff
	if _, err := ComputeOpusAudioIntegrity(bytes.NewReader(stream)); err == nil || !bytes.Contains([]byte(err.Error()), []byte("checksum")) {
		t.Fatalf("error = %v, want checksum failure", err)
	}
}

func TestOpusIntegrityRejectsMalformedStreams(t *testing.T) {
	tests := map[string][]byte{
		"empty":   nil,
		"not ogg": []byte("not-an-ogg-file"),
		"missing tags": joinOggPages(
			testOggPage(0x02, 1, 0, 0, testOpusHead(1, 0)),
			testOggPage(0x04, 1, 1, 960, []byte{1, 2, 3}),
		),
		"no eos": joinOggPages(
			testOggPage(0x02, 1, 0, 0, testOpusHead(1, 0), testOpusTags("tags")),
			testOggPage(0, 1, 1, 960, []byte{1, 2, 3}),
		),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ComputeOpusAudioIntegrity(bytes.NewReader(raw)); err == nil {
				t.Fatal("expected malformed stream error")
			}
		})
	}
}

func testOpusHead(channels byte, preSkip uint16) []byte {
	packet := make([]byte, 19)
	copy(packet, "OpusHead")
	packet[8] = 1
	packet[9] = channels
	binary.LittleEndian.PutUint16(packet[10:12], preSkip)
	binary.LittleEndian.PutUint32(packet[12:16], 48000)
	return packet
}

func testOpusTags(vendor string) []byte {
	packet := make([]byte, 8+4+len(vendor)+4)
	copy(packet, "OpusTags")
	binary.LittleEndian.PutUint32(packet[8:12], uint32(len(vendor)))
	copy(packet[12:], vendor)
	return packet
}

func testOggPage(headerType byte, serial, sequence uint32, granule uint64, packets ...[]byte) []byte {
	segments := make([]byte, 0, len(packets))
	payload := make([]byte, 0)
	for _, packet := range packets {
		if len(packet) >= 255 {
			panic("test packet must fit in one lacing segment")
		}
		segments = append(segments, byte(len(packet)))
		payload = append(payload, packet...)
	}
	return testOggPageWithSegments(headerType, serial, sequence, granule, segments, payload)
}

func testOggPageWithSegments(headerType byte, serial, sequence uint32, granule uint64, segments, payload []byte) []byte {
	header := make([]byte, 27+len(segments))
	copy(header, "OggS")
	header[5] = headerType
	binary.LittleEndian.PutUint64(header[6:14], granule)
	binary.LittleEndian.PutUint32(header[14:18], serial)
	binary.LittleEndian.PutUint32(header[18:22], sequence)
	header[26] = byte(len(segments))
	copy(header[27:], segments)
	page := append(header, payload...)
	crc := uint32(0)
	for _, value := range page {
		crc = oggCRCStep(crc, value)
	}
	binary.LittleEndian.PutUint32(page[22:26], crc)
	return page
}

func joinOggPages(pages ...[]byte) []byte {
	return bytes.Join(pages, nil)
}
