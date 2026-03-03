package cassette

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/pion/rtp"
)

type Reader struct {
	file   *os.File
	buffer *bufio.Reader
	header Header
}

func OpenReader(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	br := bufio.NewReaderSize(f, 1<<20)

	magic := make([]byte, len(Magic))
	if _, err := io.ReadFull(br, magic); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("read magic: %w", err)
	}
	if string(magic) != Magic {
		_ = f.Close()
		return nil, fmt.Errorf("bad magic: %q", string(magic))
	}

	var version uint16
	if err := binary.Read(br, binary.BigEndian, &version); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("read version: %w", err)
	}
	var createdAt int64
	if err := binary.Read(br, binary.BigEndian, &createdAt); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("read created-at: %w", err)
	}

	return &Reader{
		file:   f,
		buffer: br,
		header: Header{Version: version, CreatedAtUnixNano: createdAt},
	}, nil
}

func (r *Reader) Header() Header {
	return r.header
}

func (r *Reader) Next() (*Record, error) {
	t, err := r.buffer.ReadByte()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("read record type: %w", err)
	}

	var n uint32
	if err := binary.Read(r.buffer, binary.BigEndian, &n); err != nil {
		return nil, fmt.Errorf("read record length: %w", err)
	}

	payload := make([]byte, n)
	if _, err := io.ReadFull(r.buffer, payload); err != nil {
		return nil, fmt.Errorf("read record payload: %w", err)
	}

	typ := RecordType(t)
	rec := &Record{Type: typ}

	switch typ {
	case RecordTrackStart:
		var meta TrackMetadata
		if err := json.Unmarshal(payload, &meta); err != nil {
			return nil, fmt.Errorf("unmarshal track start: %w", err)
		}
		rec.TrackStart = &meta
	case RecordRTPPacket:
		rtpRec, err := parseRTPRecord(payload)
		if err != nil {
			return nil, err
		}
		rec.RTP = rtpRec
	case RecordTrackEnd:
		var end TrackEnd
		if err := json.Unmarshal(payload, &end); err != nil {
			return nil, fmt.Errorf("unmarshal track end: %w", err)
		}
		rec.TrackEnd = &end
	default:
		return nil, fmt.Errorf("unknown record type: %d", typ)
	}

	return rec, nil
}

func (r *Reader) Close() error {
	if r.file == nil {
		return nil
	}
	return r.file.Close()
}

func ReadAll(path string) (Header, []Record, error) {
	r, err := OpenReader(path)
	if err != nil {
		return Header{}, nil, err
	}
	defer r.Close()

	records := make([]Record, 0, 256)
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Header{}, nil, err
		}
		records = append(records, *rec)
	}
	return r.Header(), records, nil
}

func parseRTPRecord(payload []byte) (*RTPRecord, error) {
	if len(payload) < 16 {
		return nil, fmt.Errorf("rtp record too short: %d", len(payload))
	}
	trackRef := binary.BigEndian.Uint32(payload[0:4])
	arrival := int64(binary.BigEndian.Uint64(payload[4:12]))
	rawLen := binary.BigEndian.Uint32(payload[12:16])

	if int(rawLen) != len(payload)-16 {
		return nil, fmt.Errorf("rtp record length mismatch raw=%d payload=%d", rawLen, len(payload)-16)
	}

	raw := make([]byte, rawLen)
	copy(raw, payload[16:])

	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(raw); err != nil {
		return nil, fmt.Errorf("unmarshal RTP packet: %w", err)
	}

	return &RTPRecord{
		TrackRef:        trackRef,
		ArrivalUnixNano: arrival,
		RawPacket:       raw,
		ParsedRTP:       pkt,
	}, nil
}
