package app

import (
	"context"
	"fmt"
	"log"
	"time"

	"gocassini/internal/cassette"
	"gocassini/internal/config"
	"gocassini/internal/recorder"
	"gocassini/internal/talk"

	"github.com/pion/rtp"
)

func Run(cfg config.Config) error {
	switch cfg.Mode {
	case "simulate":
		return runSimulate(cfg)
	case "talk":
		return talk.Run(context.Background(), cfg)
	default:
		return fmt.Errorf("unsupported mode %q", cfg.Mode)
	}
}

func runSimulate(cfg config.Config) error {
	w, err := cassette.NewWriter(cfg.OutputPath)
	if err != nil {
		return err
	}
	defer w.Close()

	r := recorder.NewRTPRecorder(w)
	baseArrival := time.Now().Add(-2 * time.Second)

	for trackIdx := 0; trackIdx < cfg.SimTracks; trackIdx++ {
		kind := "audio"
		codec := "audio/opus"
		clock := uint32(48000)
		payloadType := uint8(111)
		if trackIdx%2 == 1 {
			kind = "video"
			codec = "video/VP8"
			clock = 90000
			payloadType = 96
		}

		trackRef, err := r.RegisterTrack(cassette.TrackMetadata{
			StartedAtUnixNano: time.Now().UnixNano(),
			ParticipantName:   fmt.Sprintf("participant-%d", trackIdx+1),
			ParticipantID:     fmt.Sprintf("user-%d", trackIdx+1),
			RemoteSessionID:   fmt.Sprintf("session-%d", trackIdx+1),
			TrackID:           fmt.Sprintf("track-%d", trackIdx+1),
			StreamID:          "main",
			RID:               "",
			Kind:              kind,
			Codec:             codec,
			ClockRate:         clock,
			SSRC:              uint32(10000 + trackIdx),
			PayloadType:       payloadType,
		})
		if err != nil {
			return fmt.Errorf("register track %d: %w", trackIdx+1, err)
		}

		for i := 0; i < cfg.SimPackets; i++ {
			pkt := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    payloadType,
					SequenceNumber: uint16(i + trackIdx*1000),
					Timestamp:      uint32(i * int(clock/50)),
					SSRC:           uint32(10000 + trackIdx),
					Marker:         i%50 == 0,
				},
				Payload: []byte{byte(trackIdx), byte(i % 256), 0xAB, 0xCD},
			}
			arrival := baseArrival.Add(time.Duration(i*20+trackIdx*3) * time.Millisecond)
			if err := w.WriteRTP(trackRef, pkt, arrival); err != nil {
				return fmt.Errorf("write packet track=%d idx=%d: %w", trackIdx+1, i, err)
			}
		}

		if err := r.EndTrack(trackRef, "simulate-end", time.Now()); err != nil {
			return fmt.Errorf("end track %d: %w", trackIdx+1, err)
		}
	}

	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush archive: %w", err)
	}

	head, records, err := cassette.ReadAll(cfg.OutputPath)
	if err != nil {
		return fmt.Errorf("readback check: %w", err)
	}
	log.Printf("simulate complete: output=%s version=%d records=%d", cfg.OutputPath, head.Version, len(records))
	return nil
}
