package talk

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gocassini/pkg/core/session"
	"gocassini/pkg/core/store"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// errSessionArtifactClosed marks operations attempted after close(). Track
// readers racing shutdown hit this benignly, so callers must not treat it
// as a capture failure (and must stop retrying stream opens).
var errSessionArtifactClosed = errors.New("session artifact is closed")

type sessionCaptureArtifact struct {
	sessionDir  string
	streamsDir  string
	sessionPath string
	eventsPath  string
	sessionID   string
	sessionMeta session.Session

	// monoOrigin retains the monotonic component returned by time.Now, while
	// monoBaseNS retains that instant's Unix-nanosecond coordinate. Persisted
	// *MonoNS values are base + monotonic elapsed time: streams share one stable
	// clock, and external capture-boundary tools retain the existing Unix-ns
	// coordinate during normal operation.
	monoOrigin time.Time
	monoBaseNS uint64

	eventsFile   *os.File
	eventsWriter *bufio.Writer

	mu           sync.Mutex
	closed       bool
	streamSeq    int
	streams      map[string]*sessionCaptureStream
	logicalBy    map[string]string
	participants map[string]int

	// captureErrCount/firstCaptureErr make media loss sticky: any failure
	// that loses packets (stream open, rtplog write, stream close/index)
	// is remembered so close() fails the run instead of letting a silently
	// truncated recording finalize as ready (D-357).
	captureErrCount int
	firstCaptureErr error
}

type sessionCaptureStream struct {
	stream      session.PacketStream
	writer      *store.Writer
	logPath     string
	indexPath   string
	packetCount int
	closed      bool
	lastRecvNS  uint64
}

type trackDescriptor struct {
	kind      string
	codec     string
	mid       string
	rid       string
	clockRate uint32
	fmtp      map[string]string
}

type sessionCaptureSummary struct {
	Enabled           bool   `json:"enabled"`
	Closed            bool   `json:"closed"`
	SessionID         string `json:"session_id"`
	SessionJSONPath   string `json:"session_json"`
	EventsPath        string `json:"events_ndjson"`
	StreamsDir        string `json:"streams_dir"`
	StreamCount       int    `json:"stream_count"`
	PacketCount       int    `json:"packet_count"`
	ActiveStreamCount int    `json:"active_stream_count"`
	CaptureErrorCount int    `json:"capture_error_count"`
	FirstCaptureError string `json:"first_capture_error,omitempty"`
}

func newSessionCaptureArtifact(finalOutputPath, callURL, roomToken, recorderName string) (*sessionCaptureArtifact, error) {
	sessionID := makeSessionID()
	base := strings.TrimSuffix(filepath.Base(finalOutputPath), filepath.Ext(finalOutputPath))
	if base == "" {
		base = "meeting"
	}
	base = sanitizeSessionPathPart(base)

	root := filepath.Dir(finalOutputPath)
	if root == "." || root == "" {
		root = os.TempDir()
	}
	sessionDir := filepath.Join(root, "sessions", fmt.Sprintf("%s_%s", base, sessionID))
	streamsDir := filepath.Join(sessionDir, "streams")
	if err := os.MkdirAll(streamsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create session streams dir: %w", err)
	}

	sessionPath := filepath.Join(sessionDir, "session.json")
	eventsPath := filepath.Join(sessionDir, "events.ndjson")
	eventsFile, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open events file: %w", err)
	}

	// Keep the original time.Time for its process-monotonic component. Calling
	// UTC is only appropriate for the separately persisted wall-clock label.
	now := time.Now()
	monoBaseNS := uint64(now.UnixNano())
	meta := session.Session{
		Version:        session.SchemaVersion,
		SessionID:      fmt.Sprintf("%s_%s", base, sessionID),
		StartedWallUTC: now.UTC().Format(time.RFC3339Nano),
		StartedMonoNS:  monoBaseNS,
		Platform: session.Platform{
			Name:       "nextcloudtalk",
			Deployment: "custom",
			Room:       roomToken,
			RecorderIdentity: session.RecorderIdentity{
				Display: recorderName,
				Silent:  true,
			},
		},
		EventsVersion:    1,
		EventsSourcePath: filepath.Base(eventsPath),
	}
	artifact := &sessionCaptureArtifact{
		sessionDir:   sessionDir,
		streamsDir:   streamsDir,
		sessionPath:  sessionPath,
		eventsPath:   eventsPath,
		sessionID:    sessionID,
		sessionMeta:  meta,
		monoOrigin:   now,
		monoBaseNS:   monoBaseNS,
		eventsFile:   eventsFile,
		eventsWriter: bufio.NewWriter(eventsFile),
		streams:      map[string]*sessionCaptureStream{},
		logicalBy:    map[string]string{},
		participants: map[string]int{},
	}

	if err := artifact.persistSessionLocked(); err != nil {
		_ = eventsFile.Close()
		return nil, err
	}
	if err := artifact.emitEvent(map[string]any{
		"type":          "session_started",
		"call_url":      callURL,
		"final_output":  finalOutputPath,
		"room_token":    roomToken,
		"recorder_name": recorderName,
	}, now); err != nil {
		_ = eventsFile.Close()
		return nil, err
	}
	return artifact, nil
}

func (a *sessionCaptureArtifact) openStream(
	remoteSessionID, participantID, participantName string,
	desc trackDescriptor,
	primarySSRC uint32,
	payloadType uint8,
	arrival time.Time,
) (string, error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return "", errSessionArtifactClosed
	}

	midSafe := sanitizeSessionPathPart(desc.mid)
	ridSafe := sanitizeSessionPathPart(desc.rid)
	startMonoNS := a.monoNS(arrival)
	pid := a.ensureParticipantLocked(remoteSessionID, participantID, participantName)
	ltid := a.ensureLogicalTrackLocked(pid, desc.kind, desc.mid, desc.rid, startMonoNS)
	streamID := fmt.Sprintf("s_%06d", a.streamSeq+1)
	a.streamSeq++

	streamPath := filepath.Join(a.streamsDir, streamID+".rtplog")
	indexPath := filepath.Join(a.streamsDir, streamID+".idx")
	header := store.StreamHeader{
		StreamID:    streamID,
		MID:         midSafe,
		RID:         ridSafe,
		Codec:       desc.codec,
		ClockRate:   desc.clockRate,
		Fmtp:        cloneStringMap(desc.fmtp),
		Direction:   "recvonly",
		StartMonoNS: startMonoNS,
		PT:          payloadType,
	}
	w, err := store.NewWriter(streamPath, header)
	if err != nil {
		a.mu.Unlock()
		err = fmt.Errorf("create stream writer: %w", err)
		a.noteCaptureFailure("open", streamID, err)
		return "", err
	}

	state := &sessionCaptureStream{
		stream: session.PacketStream{
			StreamID:     streamID,
			LTID:         ltid,
			MID:          midSafe,
			RID:          ridSafe,
			PrimarySSRC:  primarySSRC,
			Codec:        desc.codec,
			ClockRate:    desc.clockRate,
			PT:           payloadType,
			FmtpSnapshot: cloneStringMap(desc.fmtp),
			StartMonoNS:  startMonoNS,
		},
		writer:     w,
		logPath:    streamPath,
		indexPath:  indexPath,
		lastRecvNS: startMonoNS,
	}
	a.streams[streamID] = state
	a.sessionMeta.PacketStreams = append(a.sessionMeta.PacketStreams, state.stream)
	a.mu.Unlock()

	if err := a.emitEvent(map[string]any{
		"type":              "stream_opened",
		"remote_session_id": remoteSessionID,
		"participant_id":    pid,
		"participant_name":  participantName,
		"ltid":              ltid,
		"stream_id":         streamID,
		"mid":               midSafe,
		"rid":               ridSafe,
		"kind":              desc.kind,
		"codec":             desc.codec,
		"primary_ssrc":      primarySSRC,
		"pt":                payloadType,
	}, arrival); err != nil {
		_ = w.Close()
		a.removeStream(streamID)
		a.noteCaptureFailure("open", streamID, err)
		return "", err
	}

	if err := a.persistSession(); err != nil {
		_ = w.Close()
		a.removeStream(streamID)
		a.noteCaptureFailure("open", streamID, err)
		return "", err
	}
	return streamID, nil
}

// noteCaptureFailure records a media-losing failure (sticky). The first
// error is kept and surfaced by close() so the run is marked failed; the
// callers still receive the error for per-call logging and handling.
func (a *sessionCaptureArtifact) noteCaptureFailure(stage, streamID string, err error) {
	if err == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.captureErrCount++
	if a.firstCaptureErr == nil {
		a.firstCaptureErr = fmt.Errorf("%s stream %s: %w", stage, streamID, err)
	}
}

// captureFailure returns the first media-losing failure recorded during
// the session, if any.
func (a *sessionCaptureArtifact) captureFailure() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.firstCaptureErr
}

func (a *sessionCaptureArtifact) updateParticipantDisplay(remoteSessionID, participantID, participantName string) error {
	display := strings.TrimSpace(participantName)
	if display == "" {
		return nil
	}

	pid := normalizedParticipantID(participantID, remoteSessionID)

	a.mu.Lock()
	defer a.mu.Unlock()

	idx, ok := a.participants[pid]
	if !ok {
		return nil
	}
	current := strings.TrimSpace(a.sessionMeta.Participants[idx].Display)
	if current == display {
		return nil
	}
	if current != "" && !isPlaceholderParticipantName(current, pid) {
		return nil
	}
	a.sessionMeta.Participants[idx].Display = display
	return a.persistSessionLocked()
}

func (a *sessionCaptureArtifact) writeRTP(streamID string, pkt *rtp.Packet, recv time.Time) error {
	wire, err := pkt.Marshal()
	if err != nil {
		return fmt.Errorf("marshal RTP packet: %w", err)
	}

	a.mu.Lock()
	stream := a.streams[streamID]
	if stream == nil || stream.writer == nil || stream.closed {
		a.mu.Unlock()
		return fmt.Errorf("stream not writable: %s", streamID)
	}
	recvMonoNS := clampMonoNS(stream, a.monoNS(recv))
	writer := stream.writer
	a.mu.Unlock()

	if err := writer.Write(store.Record{
		RecvMonoNS: recvMonoNS,
		Kind:       store.KindRTP,
		WireBytes:  wire,
	}); err != nil {
		if a.streamCloseRace(stream, err) {
			return fmt.Errorf("stream not writable: %s", streamID)
		}
		// A failed rtplog write loses media and can corrupt the record
		// framing from this offset on; remember it so the run cannot
		// finalize as ready.
		a.noteCaptureFailure("write", streamID, err)
		return err
	}

	a.mu.Lock()
	stream.packetCount++
	a.mu.Unlock()
	return nil
}

func (a *sessionCaptureArtifact) writeRTCP(streamID string, packets []rtcp.Packet, recv time.Time) error {
	wirePackets := make([][]byte, 0, len(packets))
	for _, packet := range packets {
		wire, err := packet.Marshal()
		if err != nil {
			return fmt.Errorf("marshal RTCP packet: %w", err)
		}
		if len(wire) == 0 {
			continue
		}
		wirePackets = append(wirePackets, wire)
	}
	if len(wirePackets) == 0 {
		return nil
	}

	a.mu.Lock()
	stream := a.streams[streamID]
	if stream == nil || stream.writer == nil || stream.closed {
		a.mu.Unlock()
		return fmt.Errorf("stream not writable: %s", streamID)
	}
	writer := stream.writer
	recvTimes := make([]uint64, 0, len(wirePackets))
	recvMonoNS := a.monoNS(recv)
	for range wirePackets {
		recvMonoNS = clampMonoNS(stream, recvMonoNS)
		recvTimes = append(recvTimes, recvMonoNS)
		recvMonoNS++
	}
	a.mu.Unlock()

	for idx, wire := range wirePackets {
		if err := writer.Write(store.Record{
			RecvMonoNS: recvTimes[idx],
			Kind:       store.KindRTCP,
			WireBytes:  wire,
		}); err != nil {
			if a.streamCloseRace(stream, err) {
				return fmt.Errorf("stream not writable: %s", streamID)
			}
			a.noteCaptureFailure("write", streamID, err)
			return err
		}
	}

	a.mu.Lock()
	stream.packetCount += len(wirePackets)
	a.mu.Unlock()
	return nil
}

// streamCloseRace reports whether a failed writer.Write lost a race
// against a concurrent closeStream/close (SSRC/PT rotation racing the
// RTCP reader, shutdown racing track readers) rather than hitting real
// I/O trouble. The write happens outside a.mu, so a stream fetched as
// writable can be closed before the write lands; the store writer then
// rejects the record with ErrWriterClosed before touching the file. Such
// packets were dropped by an intentional close — benign, like the
// errSessionArtifactClosed path — and must not be recorded as media loss.
// The stream flag is re-checked under a.mu as a second signal because it
// flips before the writer itself closes.
func (a *sessionCaptureArtifact) streamCloseRace(stream *sessionCaptureStream, err error) bool {
	if errors.Is(err, store.ErrWriterClosed) {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return stream.closed
}

func (a *sessionCaptureArtifact) closeStream(streamID, reason string, endedAt time.Time) error {
	a.mu.Lock()
	stream, ok := a.streams[streamID]
	if !ok {
		a.mu.Unlock()
		return fmt.Errorf("stream not found: %s", streamID)
	}
	if stream.closed {
		a.mu.Unlock()
		return nil
	}
	stream.closed = true
	writer := stream.writer
	stream.writer = nil
	packetCount := stream.packetCount
	a.mu.Unlock()

	var closeErr error
	if writer != nil {
		if err := writer.Close(); err != nil {
			closeErr = fmt.Errorf("close stream log: %w", err)
		}
	}
	if err := store.BuildIndex(stream.logPath, stream.indexPath); err != nil && closeErr == nil {
		closeErr = fmt.Errorf("build stream index: %w", err)
	}
	if err := a.emitEvent(map[string]any{
		"type":         "stream_closed",
		"stream_id":    streamID,
		"reason":       reason,
		"packet_count": packetCount,
	}, endedAt); err != nil && closeErr == nil {
		closeErr = err
	}
	if closeErr != nil {
		// A failed close means buffered packets never reached disk (or the
		// stream cannot be read back); treat it as media loss.
		a.noteCaptureFailure("close", streamID, closeErr)
	}
	return closeErr
}

func (a *sessionCaptureArtifact) close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	streams := make([]*sessionCaptureStream, 0, len(a.streams))
	for _, stream := range a.streams {
		streams = append(streams, stream)
	}
	a.closed = true
	a.mu.Unlock()

	var firstErr error
	for _, stream := range streams {
		if err := a.closeStream(stream.stream.StreamID, "recorder-close", time.Now()); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if err := a.emitEvent(map[string]any{
		"type":   "session_closed",
		"reason": "recorder-close",
	}, time.Now()); err != nil && firstErr == nil {
		firstErr = err
	}

	a.mu.Lock()
	eventsWriter := a.eventsWriter
	eventsFile := a.eventsFile
	a.eventsWriter = nil
	a.eventsFile = nil
	a.mu.Unlock()

	if eventsWriter != nil {
		if err := eventsWriter.Flush(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if eventsFile != nil {
		if err := eventsFile.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if err := a.persistSession(); err != nil && firstErr == nil {
		firstErr = err
	}

	// A media-losing failure recorded during capture outranks shutdown
	// bookkeeping errors: it is the reason this recording must not be
	// finalized as a complete success.
	if err := a.captureFailure(); err != nil {
		return fmt.Errorf("session capture lost media: %w", err)
	}
	return firstErr
}

func (a *sessionCaptureArtifact) summary() sessionCaptureSummary {
	a.mu.Lock()
	defer a.mu.Unlock()
	streamCount := len(a.streams)
	packetCount := 0
	activeStreams := 0
	for _, stream := range a.streams {
		packetCount += stream.packetCount
		if !stream.closed {
			activeStreams++
		}
	}
	firstCaptureError := ""
	if a.firstCaptureErr != nil {
		firstCaptureError = a.firstCaptureErr.Error()
	}
	return sessionCaptureSummary{
		Enabled:           a.sessionPath != "" && a.eventsPath != "",
		Closed:            a.closed,
		SessionID:         a.sessionID,
		SessionJSONPath:   a.sessionPath,
		EventsPath:        a.eventsPath,
		StreamsDir:        a.streamsDir,
		StreamCount:       streamCount,
		PacketCount:       packetCount,
		ActiveStreamCount: activeStreams,
		CaptureErrorCount: a.captureErrCount,
		FirstCaptureError: firstCaptureError,
	}
}

func (a *sessionCaptureArtifact) ensureLogicalTrackLocked(participantID, kind, mid, rid string, createdMonoNS uint64) string {
	key := logicalTrackKey(participantID, kind, mid, rid)
	if ltid := a.logicalBy[key]; ltid != "" {
		return ltid
	}

	ltid := fmt.Sprintf("p:%s:%s:%s", sanitizeSessionPathPart(participantID), sanitizeSessionPathPart(kind), sanitizeSessionPathPart(mid))
	if rid != "" {
		ltid += ":" + sanitizeSessionPathPart(rid)
	}
	a.logicalBy[key] = ltid
	a.sessionMeta.LogicalTracks = append(a.sessionMeta.LogicalTracks, session.LogicalTrack{
		LTID:          ltid,
		Kind:          kind,
		Source:        inferSource(kind),
		ParticipantID: sanitizeSessionPathPart(participantID),
		MID:           sanitizeSessionPathPart(mid),
		RID:           sanitizeSessionPathPart(rid),
		CreatedMonoNS: createdMonoNS,
	})
	return ltid
}

func (a *sessionCaptureArtifact) ensureParticipantLocked(remoteSessionID, participantID, participantName string) string {
	pid := normalizedParticipantID(participantID, remoteSessionID)
	display := strings.TrimSpace(participantName)
	if display == "" {
		display = "participant-" + pid
	}

	if idx, ok := a.participants[pid]; ok {
		previous := strings.TrimSpace(a.sessionMeta.Participants[idx].Display)
		if isPlaceholderParticipantName(previous, pid) && !isPlaceholderParticipantName(display, pid) {
			a.sessionMeta.Participants[idx].Display = display
		}
		return pid
	}

	a.participants[pid] = len(a.sessionMeta.Participants)
	a.sessionMeta.Participants = append(a.sessionMeta.Participants, session.Participant{
		PID:     pid,
		Display: display,
	})
	return pid
}

func (a *sessionCaptureArtifact) emitEvent(fields map[string]any, observed time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.eventsWriter == nil {
		return nil
	}
	payload := map[string]any{
		"mono_ns": a.monoNS(observed),
	}
	for key, val := range fields {
		payload[key] = val
	}
	line, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := a.eventsWriter.Write(line); err != nil {
		return err
	}
	return a.eventsWriter.Flush()
}

// monoNS maps an observation carrying Go's monotonic clock reading into the
// session timeline. In production all callers pass time.Now-derived values,
// so time.Sub uses monotonic elapsed time even if the system wall clock moves.
// Anchoring that elapsed value at the session's initial Unix-ns coordinate
// preserves normal external boundary comparisons without following later
// realtime clock corrections.
func (a *sessionCaptureArtifact) monoNS(observed time.Time) uint64 {
	elapsed := observed.Sub(a.monoOrigin)
	if elapsed <= 0 {
		return a.monoBaseNS
	}
	return a.monoBaseNS + uint64(elapsed)
}

func (a *sessionCaptureArtifact) persistSessionLocked() error {
	body, err := json.MarshalIndent(a.sessionMeta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session metadata: %w", err)
	}
	body = append(body, '\n')
	tmp := a.sessionPath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("write session json: %w", err)
	}
	if err := os.Rename(tmp, a.sessionPath); err != nil {
		return fmt.Errorf("move session json: %w", err)
	}
	return nil
}

func (a *sessionCaptureArtifact) persistSession() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.persistSessionLocked()
}

func (a *sessionCaptureArtifact) removeStream(streamID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.streams, streamID)
	for i := range a.sessionMeta.PacketStreams {
		if a.sessionMeta.PacketStreams[i].StreamID != streamID {
			continue
		}
		a.sessionMeta.PacketStreams = append(a.sessionMeta.PacketStreams[:i], a.sessionMeta.PacketStreams[i+1:]...)
		break
	}
}

func parseFmtp(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := make(map[string]string)
	for _, item := range strings.Split(raw, ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key, val := parseFmtpPair(item)
		if key == "" {
			continue
		}
		out[key] = val
	}
	return out
}

func parseFmtpPair(raw string) (string, string) {
	if !strings.Contains(raw, "=") {
		return raw, "true"
	}
	parts := strings.SplitN(raw, "=", 2)
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func inferSource(kind string) string {
	if strings.EqualFold(kind, "video") {
		return "camera"
	}
	return kind
}

func logicalTrackKey(participantID, kind, mid, rid string) string {
	key := sanitizeSessionPathPart(participantID) + "|" + strings.ToLower(strings.TrimSpace(kind))
	key += "|" + sanitizeSessionPathPart(mid)
	if rid != "" {
		key += "|" + sanitizeSessionPathPart(rid)
	}
	return key
}

func sanitizeSessionPathPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	value = strings.ReplaceAll(value, "/", "_")
	value = strings.ReplaceAll(value, `\`, "_")
	return value
}

func normalizedParticipantID(participantID, remoteSessionID string) string {
	trimmed := strings.TrimSpace(participantID)
	if trimmed == "" {
		trimmed = strings.TrimSpace(remoteSessionID)
	}
	return sanitizeSessionPathPart(trimmed)
}

func isPlaceholderParticipantName(display, participantID string) bool {
	display = strings.TrimSpace(display)
	return display == "" || display == "participant-"+sanitizeSessionPathPart(participantID)
}

func makeSessionID() string {
	return time.Now().UTC().Format("20060102T150405.000000000Z")
}

func clampMonoNS(stream *sessionCaptureStream, recvMonoNS uint64) uint64 {
	if stream.lastRecvNS == 0 || recvMonoNS > stream.lastRecvNS {
		stream.lastRecvNS = recvMonoNS
		return recvMonoNS
	}
	stream.lastRecvNS++
	return stream.lastRecvNS
}

func descriptorFromTrack(track *webrtc.TrackRemote) trackDescriptor {
	mid := track.StreamID()
	if mid == "" {
		mid = track.ID()
	}
	return trackDescriptor{
		kind:      strings.ToLower(track.Kind().String()),
		codec:     strings.ToLower(track.Codec().MimeType),
		mid:       mid,
		rid:       track.RID(),
		clockRate: track.Codec().ClockRate,
		fmtp:      parseFmtp(track.Codec().SDPFmtpLine),
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
