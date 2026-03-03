package cassette

import "github.com/pion/rtp"

type RecordType uint8

const (
	RecordTrackStart RecordType = 1
	RecordRTPPacket  RecordType = 2
	RecordTrackEnd   RecordType = 3
)

const (
	Magic   = "CSR1"
	Version = uint16(1)
)

type Header struct {
	Version           uint16
	CreatedAtUnixNano int64
}

type TrackMetadata struct {
	TrackRef          uint32 `json:"track_ref"`
	StartedAtUnixNano int64  `json:"started_at_unix_nano"`
	ParticipantName   string `json:"participant_name"`
	ParticipantID     string `json:"participant_id"`
	RemoteSessionID   string `json:"remote_session_id"`
	TrackID           string `json:"track_id"`
	StreamID          string `json:"stream_id"`
	RID               string `json:"rid"`
	Kind              string `json:"kind"`
	Codec             string `json:"codec"`
	ClockRate         uint32 `json:"clock_rate"`
	SSRC              uint32 `json:"ssrc"`
	PayloadType       uint8  `json:"payload_type"`
}

type TrackEnd struct {
	TrackRef        uint32 `json:"track_ref"`
	EndedAtUnixNano int64  `json:"ended_at_unix_nano"`
	Reason          string `json:"reason"`
}

type RTPRecord struct {
	TrackRef        uint32
	ArrivalUnixNano int64
	RawPacket       []byte
	ParsedRTP       *rtp.Packet
}

type Record struct {
	Type       RecordType
	TrackStart *TrackMetadata
	RTP        *RTPRecord
	TrackEnd   *TrackEnd
}
