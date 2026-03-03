package recorder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gocassini/internal/cassette"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type ParticipantRef struct {
	ParticipantName string
	ParticipantID   string
	RemoteSessionID string
}

type RTPRecorder struct {
	writer *cassette.Writer
}

func NewRTPRecorder(writer *cassette.Writer) *RTPRecorder {
	return &RTPRecorder{writer: writer}
}

func (r *RTPRecorder) RegisterTrack(meta cassette.TrackMetadata) (uint32, error) {
	return r.writer.RegisterTrack(meta)
}

func (r *RTPRecorder) WritePacket(trackRef uint32, pkt *rtp.Packet, arrival time.Time) error {
	if pkt == nil {
		return errors.New("nil RTP packet")
	}
	return r.writer.WriteRTP(trackRef, pkt, arrival)
}

func (r *RTPRecorder) EndTrack(trackRef uint32, reason string, endedAt time.Time) error {
	return r.writer.EndTrack(trackRef, reason, endedAt)
}

func (r *RTPRecorder) CaptureTrack(ctx context.Context, track *webrtc.TrackRemote, participant ParticipantRef) error {
	if track == nil {
		return errors.New("nil track")
	}

	codec := track.Codec()
	meta := cassette.TrackMetadata{
		StartedAtUnixNano: time.Now().UnixNano(),
		ParticipantName:   participant.ParticipantName,
		ParticipantID:     participant.ParticipantID,
		RemoteSessionID:   participant.RemoteSessionID,
		TrackID:           track.ID(),
		StreamID:          track.StreamID(),
		RID:               track.RID(),
		Kind:              strings.ToLower(track.Kind().String()),
		Codec:             codec.MimeType,
		ClockRate:         codec.ClockRate,
		SSRC:              uint32(track.SSRC()),
		PayloadType:       uint8(track.PayloadType()),
	}

	trackRef, err := r.writer.RegisterTrack(meta)
	if err != nil {
		return fmt.Errorf("register track: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			_ = r.writer.EndTrack(trackRef, "context-cancelled", time.Now())
			return context.Cause(ctx)
		default:
		}

		pkt, _, err := track.ReadRTP()
		if err != nil {
			if errors.Is(err, io.EOF) {
				_ = r.writer.EndTrack(trackRef, "eof", time.Now())
				return nil
			}
			_ = r.writer.EndTrack(trackRef, "read-error", time.Now())
			return fmt.Errorf("read RTP: %w", err)
		}

		if err := r.writer.WriteRTP(trackRef, pkt, time.Now()); err != nil {
			_ = r.writer.EndTrack(trackRef, "write-error", time.Now())
			return fmt.Errorf("write RTP: %w", err)
		}
	}
}
