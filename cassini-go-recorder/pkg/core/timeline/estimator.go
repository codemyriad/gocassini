package timeline

import (
	"fmt"
	"math"
	"sync"

	"github.com/pion/rtcp"
)

type Estimator interface {
	ObserveRTP(ssrc uint32, recvNS uint64, rtpTS uint32, marker bool, isKeyframe bool, clockRate uint32)
	ObserveRTCP(ssrc uint32, recvNS uint64, raw []byte)
	PTS(ssrc uint32, rtpTS uint32) (uint64, bool)
	SetClockRate(ssrc uint32, clockRate uint32)
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

	slopeNSPerTick float64
	interceptNS    float64

	srSeen       bool
	lastSRRecvNS uint64
	lastSRRTP    int64

	seen     bool
	ptsReady bool
	lastPTS  uint64
}

const (
	defaultClockRate       = uint32(90000)
	srSlopeAlpha           = 0.10
	srOffsetAlpha          = 0.20
	srMaxOffsetStepNS      = 5_000_000.0
	srObservedSlopeMinMult = 0.50
	srObservedSlopeMaxMult = 1.50
)

func NewSegmentEstimator() *SegmentEstimator {
	return &SegmentEstimator{
		streams: map[uint32]*segmentState{},
	}
}

func (e *SegmentEstimator) ObserveRTP(ssrc uint32, recvNS uint64, rtpTS uint32, marker bool, isKeyframe bool, clockRate uint32) {
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
		s.clockRate = chooseClockRate(clockRate)
		s.slopeNSPerTick = 1e9 / float64(s.clockRate)
		s.interceptNS = float64(s.startMono) - s.slopeNSPerTick*float64(s.startRTP)
		s.lastPTS = recvNS
		s.ptsReady = false
		return
	}
	if clockRate > 0 && (s.clockRate == 0 || s.clockRate == defaultClockRate) {
		s.clockRate = clockRate
		if !s.srSeen {
			reference := s.predict(float64(s.lastRTP))
			s.slopeNSPerTick = 1e9 / float64(s.clockRate)
			s.interceptNS = reference - s.slopeNSPerTick*float64(s.lastRTP)
		}
	}
	s.lastRTP = s.unwrapRTP(rtpTS)
}

func (e *SegmentEstimator) ObserveRTCP(ssrc uint32, recvNS uint64, raw []byte) {
	if len(raw) == 0 {
		return
	}
	packets, err := rtcp.Unmarshal(raw)
	if err != nil {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	s := e.streams[ssrc]
	if s == nil || !s.seen {
		return
	}
	for _, packet := range packets {
		sr, ok := packet.(*rtcp.SenderReport)
		if !ok {
			continue
		}
		s.observeSenderReport(recvNS, sr.RTPTime)
	}
}

func (e *SegmentEstimator) SetClockRate(ssrc uint32, clockRate uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()

	stream := e.streams[ssrc]
	if stream == nil || clockRate == 0 {
		return
	}
	if stream.clockRate == clockRate {
		return
	}
	if stream.seen && !stream.srSeen {
		reference := stream.predict(float64(stream.lastRTP))
		stream.clockRate = clockRate
		stream.slopeNSPerTick = 1e9 / float64(clockRate)
		stream.interceptNS = reference - stream.slopeNSPerTick*float64(stream.lastRTP)
		return
	}
	stream.clockRate = clockRate
}

func (e *SegmentEstimator) PTS(ssrc uint32, rtpTS uint32) (uint64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	s := e.streams[ssrc]
	if s == nil || !s.seen {
		return 0, false
	}
	if s.clockRate == 0 {
		s.clockRate = defaultClockRate
		if s.slopeNSPerTick == 0 {
			s.slopeNSPerTick = 1e9 / float64(s.clockRate)
			s.interceptNS = float64(s.startMono) - s.slopeNSPerTick*float64(s.startRTP)
		}
	}

	unwrapped := s.unwrapRTP(rtpTS)
	predicted := s.predict(float64(unwrapped))
	if predicted < 0 {
		predicted = 0
	}
	pts := uint64(predicted)
	if s.ptsReady && pts <= s.lastPTS {
		pts = s.lastPTS + 1
	}
	if !s.ptsReady && pts < s.startMono {
		pts = s.startMono
	}
	s.ptsReady = true
	s.lastPTS = pts
	return pts, true
}

func (s *segmentState) unwrapRTP(raw uint32) int64 {
	if !s.seen {
		return int64(raw)
	}

	return unwrap32(s.lastRTP, raw)
}

func (s *segmentState) predict(rtp float64) float64 {
	return s.slopeNSPerTick*rtp + s.interceptNS
}

func (s *segmentState) observeSenderReport(recvNS uint64, rtpTS uint32) {
	anchor := s.lastRTP
	if s.srSeen {
		anchor = s.lastSRRTP
	}
	currRTP := unwrap32(anchor, rtpTS)
	if !s.srSeen {
		s.srSeen = true
		s.lastSRRecvNS = recvNS
		s.lastSRRTP = currRTP
		return
	}

	deltaRTP := currRTP - s.lastSRRTP
	deltaRecv := recvNS - s.lastSRRecvNS
	s.lastSRRecvNS = recvNS
	s.lastSRRTP = currRTP
	if deltaRTP <= 0 || deltaRecv == 0 {
		return
	}

	observedSlope := float64(deltaRecv) / float64(deltaRTP)
	if observedSlope <= 0 {
		return
	}
	expectedSlope := 1e9 / float64(chooseClockRate(s.clockRate))
	if observedSlope < expectedSlope*srObservedSlopeMinMult || observedSlope > expectedSlope*srObservedSlopeMaxMult {
		return
	}

	refRTP := float64(currRTP)
	refPTS := s.predict(refRTP)
	s.slopeNSPerTick = blend(s.slopeNSPerTick, observedSlope, srSlopeAlpha)
	s.interceptNS = refPTS - s.slopeNSPerTick*refRTP

	errorNS := float64(recvNS) - s.predict(refRTP)
	adjustment := clamp(errorNS*srOffsetAlpha, -srMaxOffsetStepNS, srMaxOffsetStepNS)
	s.interceptNS += adjustment
}

func chooseClockRate(clockRate uint32) uint32 {
	if clockRate > 0 {
		return clockRate
	}
	return defaultClockRate
}

func blend(current, observed, alpha float64) float64 {
	return current*(1-alpha) + observed*alpha
}

func clamp(value, min, max float64) float64 {
	return math.Max(min, math.Min(max, value))
}

func unwrap32(last int64, raw uint32) int64 {
	raw64 := int64(raw)
	delta := raw64 - (last & 0xffffffff)
	for delta > (1 << 31) {
		delta -= 1 << 32
	}
	for delta < -(1 << 31) {
		delta += 1 << 32
	}
	return last + delta
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
