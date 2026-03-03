package mux

type TrackDesc struct {
	LTID      string
	Kind      string
	Codec     string
	ClockRate uint32
	Fmtp      map[string]string
}

type Muxer interface {
	AddTrack(td TrackDesc) (int, error)
	WriteSample(trackID int, ptsNS uint64, data []byte, isKeyframe bool) error
	Close() error
}
