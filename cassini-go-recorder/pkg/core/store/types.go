package store

import "encoding/json"

const (
	Magic      = "RTPL0\000\000\000"
	CurrentVer = 1
	FlagUseCRC = 1 << 0
)

type StreamKind uint8

const (
	KindRTP  StreamKind = 1
	KindRTCP StreamKind = 2
)

type StreamHeader struct {
	Version     uint16            `json:"version"`
	StreamID    string            `json:"stream_id"`
	MID         string            `json:"mid"`
	RID         string            `json:"rid"`
	Codec       string            `json:"codec"`
	ClockRate   uint32            `json:"clock_rate"`
	Fmtp        map[string]string `json:"fmtp,omitempty"`
	Direction   string            `json:"direction"`
	StartMonoNS uint64            `json:"start_mono_ns"`
	PT          uint8             `json:"pt"`
}

type Record struct {
	RecvMonoNS uint64 `json:"-"`
	Kind       StreamKind
	WireBytes  []byte
}

type rtplogHeader struct {
	Magic  string `json:"magic"`
	Flags  uint16 `json:"flags"`
	Header StreamHeader
}

type streamIndex struct {
	RecvMonoNS uint64
	Offset     uint64
}

func (h StreamHeader) MarshalJSON() ([]byte, error) {
	type alias StreamHeader
	aux := struct {
		alias
		Version uint16 `json:"version"`
	}{
		alias:   alias(h),
		Version: CurrentVer,
	}
	return json.Marshal(aux)
}
