package cassette

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/pion/rtp"
)

type Writer struct {
	mu          sync.Mutex
	file        *os.File
	buffer      *bufio.Writer
	nextTrackID uint32
	closed      bool
}

func NewWriter(path string) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create archive: %w", err)
	}
	bw := bufio.NewWriterSize(f, 1<<20)

	if _, err := bw.WriteString(Magic); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write magic: %w", err)
	}
	if err := binary.Write(bw, binary.BigEndian, Version); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write version: %w", err)
	}
	if err := binary.Write(bw, binary.BigEndian, time.Now().UnixNano()); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write created-at: %w", err)
	}

	return &Writer{
		file:   f,
		buffer: bw,
	}, nil
}

func (w *Writer) RegisterTrack(meta TrackMetadata) (uint32, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return 0, errors.New("writer is closed")
	}

	w.nextTrackID++
	meta.TrackRef = w.nextTrackID
	if meta.StartedAtUnixNano == 0 {
		meta.StartedAtUnixNano = time.Now().UnixNano()
	}

	payload, err := json.Marshal(meta)
	if err != nil {
		return 0, fmt.Errorf("marshal track metadata: %w", err)
	}
	if err := writeRecord(w.buffer, RecordTrackStart, payload); err != nil {
		return 0, err
	}

	return meta.TrackRef, nil
}

func (w *Writer) WriteRTP(trackRef uint32, pkt *rtp.Packet, arrival time.Time) error {
	if pkt == nil {
		return errors.New("nil RTP packet")
	}

	raw, err := pkt.Marshal()
	if err != nil {
		return fmt.Errorf("marshal RTP packet: %w", err)
	}

	payload := make([]byte, 16+len(raw))
	binary.BigEndian.PutUint32(payload[0:4], trackRef)
	binary.BigEndian.PutUint64(payload[4:12], uint64(arrival.UnixNano()))
	binary.BigEndian.PutUint32(payload[12:16], uint32(len(raw)))
	copy(payload[16:], raw)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("writer is closed")
	}

	return writeRecord(w.buffer, RecordRTPPacket, payload)
}

func (w *Writer) EndTrack(trackRef uint32, reason string, endedAt time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("writer is closed")
	}

	end := TrackEnd{
		TrackRef:        trackRef,
		EndedAtUnixNano: endedAt.UnixNano(),
		Reason:          reason,
	}
	payload, err := json.Marshal(end)
	if err != nil {
		return fmt.Errorf("marshal track end: %w", err)
	}
	return writeRecord(w.buffer, RecordTrackEnd, payload)
}

func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("writer is closed")
	}
	return w.buffer.Flush()
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true

	if err := w.buffer.Flush(); err != nil {
		_ = w.file.Close()
		return err
	}
	return w.file.Close()
}

func writeRecord(w *bufio.Writer, typ RecordType, payload []byte) error {
	if err := w.WriteByte(byte(typ)); err != nil {
		return fmt.Errorf("write record type: %w", err)
	}
	if err := binary.Write(w, binary.BigEndian, uint32(len(payload))); err != nil {
		return fmt.Errorf("write record len: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("write record payload: %w", err)
	}
	return nil
}
