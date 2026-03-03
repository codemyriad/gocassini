package streamid

import "fmt"

// StreamKey identifies a logical stream from signaling-level identifiers.
type StreamKey struct {
	MID  string
	RID  string
	Kind string
}

func NewStreamKey(mid, rid, kind string) StreamKey {
	return StreamKey{
		MID:  mid,
		RID:  rid,
		Kind: kind,
	}
}

func (s StreamKey) String() string {
	if s.RID == "" {
		return fmt.Sprintf("%s:%s", s.MID, s.Kind)
	}
	return fmt.Sprintf("%s:%s:%s", s.MID, s.RID, s.Kind)
}
