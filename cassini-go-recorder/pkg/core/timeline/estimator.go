package timeline

import (
	"fmt"
	"sync"
)

type Estimator interface {
	ObserveRTP(ssrc uint32, recvNS uint64, rtpTS uint32, marker bool, isKeyframe bool)
	ObserveRTCP(ssrc uint32, recvNS uint64, raw []byte)
	PTS(ssrc uint32, rtpTS uint32) (uint64, bool)
	CloseStream(ssrc uint32)
}

type SegmentEstimator struct {
	mu      sync.Mutex
	streams map[uint32]*segmentState
}

type segmentState struct {
	clockRate uint32
	startMono uint64
	startRTP  int64
	lastRTP   int64
	seen      bool
	lastPTS   uint64
}

func NewSegmentEstimator() *SegmentEstimator {
	return &SegmentEstimator{
		streams: map[uint32]*segmentState{},
	}
}

func (e *SegmentEstimator) ObserveRTP(ssrc uint32, recvNS uint64, rtpTS uint32, marker bool, isKeyframe bool) {
	_ = marker
	_ = isKeyframe
	e.mu.Lock()
	defer e.mu.Unlock()

	s := e.streams[ssrc]
	if s == nil {
		s = &segmentState{}
		e.streams[ssrc] = s
	}

	if !s.seen {
		s.seen = true
		s.startMono = recvNS
		s.startRTP = int64(rtpTS)
		s.lastRTP = int64(rtpTS)
		s.clockRate = 0
		s.lastPTS = recvNS
		return
	}
	s.lastRTP = s.unwrapRTP(rtpTS)
}

func (e *SegmentEstimator) ObserveRTCP(ssrc uint32, recvNS uint64, raw []byte) {
	// Placeholder for sender-report-based correction. For now keep metadata.
	_ = recvNS
	_ = raw
	_ = ssrc
}

func (e *SegmentEstimator) PTS(ssrc uint32, rtpTS uint32) (uint64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	s := e.streams[ssrc]
	if s == nil || !s.seen {
		return 0, false
	}
	if s.clockRate == 0 {
		s.clockRate = 90000
	}

	unwrapped := s.unwrapRTP(rtpTS)
	delta := float64(unwrapped - s.startRTP)
	pts := s.startMono + uint64((delta/float64(s.clockRate))*1e9)
	if pts <= s.lastPTS {
		pts = s.lastPTS + 1
	}
	s.lastPTS = pts
	return pts, true
}

func (s *segmentState) unwrapRTP(raw uint32) int64 {
	if !s.seen {
		return int64(raw)
	}

	raw64 := int64(raw)
	delta := raw64 - (s.lastRTP & 0xffffffff)
	for delta > (1 << 31) {
		delta -= 1 << 32
	}
	for delta < -(1 << 31) {
		delta += 1 << 32
	}
	return s.lastRTP + delta
}

func (e *SegmentEstimator) CloseStream(ssrc uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.streams, ssrc)
}

func (e *SegmentEstimator) String() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return fmt.Sprintf("segments=%d", len(e.streams))
}
