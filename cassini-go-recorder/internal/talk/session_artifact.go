package talk

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gocassini/pkg/core/session"
	"gocassini/pkg/core/store"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type sessionCaptureArtifact struct {
	sessionDir   string
	streamsDir   string
	sessionPath  string
	eventsPath   string
	sessionID    string
	sessionMeta  session.Session

	eventsFile   *os.File
	eventsWriter *bufio.Writer

	mu            sync.Mutex
	closed        bool
	streamSeq     int
	streams       map[string]*sessionCaptureStream
	logicalBy     map[string]string
	participants  map[string]struct{}
}

type sessionCaptureStream struct {
	stream      session.PacketStream
	writer      *store.Writer
	logPath     string
	indexPath   string
	packetCount int
	closed      bool
}

type sessionCaptureSummary struct {
	Enabled           bool   `json:"enabled"`
	Closed            bool   `json:"closed"`
	SessionID         string `json:"session_id"`
	SessionJSONPath    string `json:"session_json"`
	EventsPath        string `json:"events_ndjson"`
	StreamsDir        string `json:"streams_dir"`
	StreamCount       int    `json:"stream_count"`
	PacketCount       int    `json:"packet_count"`
	ActiveStreamCount int    `json:"active_stream_count"`
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

	now := time.Now().UTC()
	meta := session.Session{
		Version:        session.SchemaVersion,
		SessionID:      fmt.Sprintf("%s_%s", base, sessionID),
		StartedWallUTC: now.Format(time.RFC3339Nano),
		StartedMonoNS:  uint64(now.UnixNano()),
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
		sessionDir:    sessionDir,
		streamsDir:    streamsDir,
		sessionPath:   sessionPath,
		eventsPath:    eventsPath,
		sessionID:     sessionID,
		sessionMeta:   meta,
		eventsFile:    eventsFile,
		eventsWriter:  bufio.NewWriter(eventsFile),
		streams:       map[string]*sessionCaptureStream{},
		logicalBy:     map[string]string{},
		participants:  map[string]struct{}{},
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
	}, uint64(now.UnixNano())); err != nil {
		_ = eventsFile.Close()
		return nil, err
	}
	return artifact, nil
}

func (a *sessionCaptureArtifact) openTrack(remoteSessionID, participantName string, track *webrtc.TrackRemote, arrival time.Time) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return "", fmt.Errorf("session artifact is closed")
	}
	if track == nil {
		return "", fmt.Errorf("track is nil")
	}

	kind := strings.ToLower(track.Kind().String())
	codec := strings.ToLower(track.Codec().MimeType)
	mid := track.StreamID()
	if mid == "" {
		mid = track.ID()
	}
	rid := track.RID()

	ltid := a.ensureLogicalTrackLocked(remoteSessionID, participantName, kind, mid, rid, arrival)
	streamID := fmt.Sprintf("s_%06d", a.streamSeq+1)
	a.streamSeq++

	streamPath := filepath.Join(a.streamsDir, streamID+".rtplog")
	indexPath := filepath.Join(a.streamsDir, streamID+".idx")
	header := store.StreamHeader{
		StreamID:    streamID,
		MID:         sanitizeSessionPathPart(mid),
		RID:         sanitizeSessionPathPart(rid),
		Codec:       codec,
		ClockRate:   track.Codec().ClockRate,
		Fmtp:        parseFmtp(track.Codec().SDPFmtpLine),
		Direction:   "recvonly",
		StartMonoNS: uint64(arrival.UnixNano()),
		PT:          uint8(track.PayloadType()),
	}
	w, err := store.NewWriter(streamPath, header)
	if err != nil {
		return "", fmt.Errorf("create stream writer: %w", err)
	}

	state := &sessionCaptureStream{
		stream: session.PacketStream{
			StreamID:     streamID,
			LTID:         ltid,
			MID:          sanitizeSessionPathPart(mid),
			RID:          sanitizeSessionPathPart(rid),
			PrimarySSRC:  uint32(track.SSRC()),
			Codec:        codec,
			ClockRate:    track.Codec().ClockRate,
			FmtpSnapshot: parseFmtp(track.Codec().SDPFmtpLine),
			StartMonoNS:   uint64(arrival.UnixNano()),
		},
		writer:    w,
		logPath:   streamPath,
		indexPath: indexPath,
	}
	a.streams[streamID] = state
	a.sessionMeta.PacketStreams = append(a.sessionMeta.PacketStreams, state.stream)
	a.ensureParticipantLocked(remoteSessionID, participantName)
	if err := a.emitEvent(map[string]any{
		"type":              "stream_opened",
		"remote_session_id": remoteSessionID,
		"participant_name":  participantName,
		"ltid":              ltid,
		"stream_id":         streamID,
		"mid":               sanitizeSessionPathPart(mid),
		"rid":               sanitizeSessionPathPart(rid),
		"kind":              kind,
		"codec":             codec,
	}, uint64(arrival.UnixNano())); err != nil {
		_ = w.Close()
		delete(a.streams, streamID)
		a.sessionMeta.PacketStreams = a.sessionMeta.PacketStreams[:len(a.sessionMeta.PacketStreams)-1]
		return "", err
	}

	if err := a.persistSessionLocked(); err != nil {
		_ = w.Close()
		delete(a.streams, streamID)
		a.sessionMeta.PacketStreams = a.sessionMeta.PacketStreams[:len(a.sessionMeta.PacketStreams)-1]
		return "", err
	}
	return streamID, nil
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
	a.mu.Unlock()

	if err := stream.writer.Write(store.Record{
		RecvMonoNS: uint64(recv.UnixNano()),
		Kind:       store.KindRTP,
		WireBytes:  wire,
	}); err != nil {
		return err
	}

	a.mu.Lock()
	stream.packetCount++
	a.mu.Unlock()
	return nil
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
		closeErr = writer.Close()
	}
	if err := store.BuildIndex(stream.logPath, stream.indexPath); err != nil {
		if closeErr == nil {
			closeErr = err
		}
	}
	if err := a.emitEvent(map[string]any{
		"type":         "stream_closed",
		"stream_id":    streamID,
		"reason":       reason,
		"packet_count": packetCount,
	}, uint64(endedAt.UnixNano())); err != nil && closeErr == nil {
		closeErr = err
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
	eventsWriter := a.eventsWriter
	a.eventsWriter = nil
	eventsFile := a.eventsFile
	a.eventsFile = nil
	a.mu.Unlock()

	for _, stream := range streams {
		_ = a.closeStream(stream.stream.StreamID, "recorder-close", time.Now())
	}

	if eventsWriter != nil {
		if err := eventsWriter.Flush(); err != nil {
			_ = eventsFile.Close()
			return err
		}
	}
	if eventsFile != nil {
		if err := eventsFile.Close(); err != nil {
			return err
		}
	}

	return a.persistSession()
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
	return sessionCaptureSummary{
		Enabled:           a.eventsFile != nil || a.eventsWriter != nil,
		Closed:            a.closed,
		SessionID:         a.sessionID,
		SessionJSONPath:    a.sessionPath,
		EventsPath:        a.eventsPath,
		StreamsDir:        a.streamsDir,
		StreamCount:       streamCount,
		PacketCount:       packetCount,
		ActiveStreamCount: activeStreams,
	}
}

func (a *sessionCaptureArtifact) ensureLogicalTrackLocked(remoteSessionID, participantName, kind, mid, rid string, observed time.Time) string {
	key := logicalTrackKey(remoteSessionID, kind, mid, rid)
	if ltid := a.logicalBy[key]; ltid != "" {
		return ltid
	}

	ltid := fmt.Sprintf("p:%s:%s:%s", sanitizeSessionPathPart(remoteSessionID), sanitizeSessionPathPart(kind), sanitizeSessionPathPart(mid))
	if rid != "" {
		ltid += ":" + sanitizeSessionPathPart(rid)
	}
	a.logicalBy[key] = ltid
	a.sessionMeta.LogicalTracks = append(a.sessionMeta.LogicalTracks, session.LogicalTrack{
		LTID:          ltid,
		Kind:          kind,
		Source:        inferSource(kind),
		ParticipantID: sanitizeSessionPathPart(remoteSessionID),
		MID:           sanitizeSessionPathPart(mid),
		RID:           sanitizeSessionPathPart(rid),
		CreatedMonoNS: uint64(observed.UnixNano()),
	})
	a.ensureParticipantLocked(remoteSessionID, participantName)
	return ltid
}

func (a *sessionCaptureArtifact) ensureParticipantLocked(remoteSessionID, participantName string) {
	if _, ok := a.participants[remoteSessionID]; ok {
		return
	}

	display := strings.TrimSpace(participantName)
	if display == "" {
		display = "participant-" + sanitizeSessionPathPart(remoteSessionID)
	}
	a.participants[remoteSessionID] = struct{}{}
	a.sessionMeta.Participants = append(a.sessionMeta.Participants, session.Participant{
		PID:     sanitizeSessionPathPart(remoteSessionID),
		Display: display,
	})
}

func (a *sessionCaptureArtifact) emitEvent(fields map[string]any, monoNS uint64) error {
	if a.eventsWriter == nil {
		return nil
	}
	payload := map[string]any{
		"mono_ns": monoNS,
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

func logicalTrackKey(remoteSessionID, kind, mid, rid string) string {
	key := sanitizeSessionPathPart(remoteSessionID) + "|" + strings.ToLower(strings.TrimSpace(kind))
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

func makeSessionID() string {
	return time.Now().UTC().Format("20060102T150405.000000000Z")
}
