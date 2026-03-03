package session

// SchemaVersion is the current on-disk session format version.
const SchemaVersion = 1

// Session is the index-level session description for an in-progress or completed
// recording. It intentionally does not include derived metrics.
type Session struct {
	Version          int            `json:"version"`
	SessionID        string         `json:"session_id"`
	StartedWallUTC   string         `json:"started_wall_utc"`
	StartedMonoNS    uint64         `json:"started_mono_ns"`
	Platform         Platform       `json:"platform"`
	Transceivers     []Transceiver  `json:"transceivers,omitempty"`
	LogicalTracks    []LogicalTrack `json:"logical_tracks,omitempty"`
	PacketStreams    []PacketStream `json:"packet_streams,omitempty"`
	Participants     []Participant  `json:"participants,omitempty"`
	EventsVersion    int            `json:"events_version"`
	EventsSourcePath string         `json:"events_source_path,omitempty"`
}

type Platform struct {
	Name             string           `json:"name"`
	Deployment       string           `json:"deployment"`
	Room             string           `json:"room"`
	RecorderIdentity RecorderIdentity `json:"recorder_identity"`
}

type RecorderIdentity struct {
	Display string `json:"display"`
	Silent  bool   `json:"silent"`
}

type Transceiver struct {
	MID       string  `json:"mid"`
	Kind      string  `json:"kind"`
	Direction string  `json:"direction"`
	ClockRate uint32  `json:"clock_rate"`
	Codecs    []Codec `json:"codecs"`
}

type Codec struct {
	Codec     string            `json:"codec"`
	ClockRate uint32            `json:"clock_rate"`
	Fmtp      map[string]string `json:"fmtp,omitempty"`
}

type LogicalTrack struct {
	LTID          string `json:"ltid"`
	Kind          string `json:"kind"`
	Source        string `json:"source"`
	ParticipantID string `json:"participant_id"`
	MID           string `json:"mid"`
	RID           string `json:"rid"`
	CreatedMonoNS uint64 `json:"created_mono_ns"`
}

type PacketStream struct {
	StreamID     string            `json:"stream_id"`
	LTID         string            `json:"ltid"`
	MID          string            `json:"mid"`
	RID          string            `json:"rid"`
	PrimarySSRC  uint32            `json:"primary_ssrc"`
	CSRC         uint32            `json:"csrc"`
	Codec        string            `json:"codec"`
	ClockRate    uint32            `json:"clock_rate"`
	PT           uint8             `json:"pt"`
	FmtpSnapshot map[string]string `json:"fmtp_snapshot,omitempty"`
	StartMonoNS  uint64            `json:"start_mono_ns"`
}

type Participant struct {
	PID     string `json:"pid"`
	Display string `json:"display"`
}
