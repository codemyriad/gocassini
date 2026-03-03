package talk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gocassini/internal/cassette"
	"gocassini/internal/config"
	"gocassini/internal/nextcloud"
	"gocassini/internal/recorder"
	"gocassini/internal/signaling"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/ivfwriter"
	"github.com/pion/webrtc/v4/pkg/media/oggwriter"
)

const requestOfferResponseTimeout = 8 * time.Second

type Recorder struct {
	cfg config.Config

	baseURL   string
	roomToken string

	ocs      *nextcloud.OCSClient
	settings *nextcloud.SignalingSettings

	nextcloudSessionID string
	signalingSessionID string

	signaling *signaling.Client

	writer      *cassette.Writer
	rtpRecorder *recorder.RTPRecorder

	finalOutputPath string
	segmentsDir     string
	startedAt       time.Time

	sessionMu        sync.Mutex
	sessionsByRemote map[string]*sessionCapture
	sessionOrder     []*sessionCapture

	mu          sync.Mutex
	subscribers map[string]*subscriberPeer
}

type sessionCapture struct {
	RemoteSessionID string
	ParticipantName string
	Index           int
	StartedAt       time.Time

	AudioPath string
	VideoPath string
	MKVPath   string

	AudioWriter *oggwriter.OggWriter
	VideoWriter *ivfwriter.IVFWriter

	AudioPackets int
	VideoPackets int
	HasAudio     bool
	HasVideo     bool
}

type subscriberPeer struct {
	owner           *Recorder
	remoteSessionID string
	pc              *webrtc.PeerConnection

	mu                   sync.Mutex
	offerReceived        bool
	currentSID           string
	requestOfferAttempts int
	awaitingOfferSince   time.Time
	offerExhaustedLogged bool
	endOfCandidatesSent  bool
}

func Run(ctx context.Context, cfg config.Config) error {
	r := &Recorder{
		cfg:              cfg,
		subscribers:      make(map[string]*subscriberPeer),
		sessionsByRemote: make(map[string]*sessionCapture),
		startedAt:        time.Now().UTC(),
	}
	return r.run(ctx)
}

func (r *Recorder) run(ctx context.Context) error {
	baseURL, roomToken, err := nextcloud.ParseCallURL(r.cfg.CallURL)
	if err != nil {
		return err
	}
	r.baseURL = baseURL
	r.roomToken = roomToken
	r.finalOutputPath = deriveFinalOutputPath(r.cfg.OutputPath, r.cfg.FinalOutputPath)

	if err := ensureOutputDir(r.cfg.OutputPath); err != nil {
		return err
	}
	if err := ensureOutputDir(r.finalOutputPath); err != nil {
		return err
	}
	segmentsDir, err := prepareSegmentsDir(r.finalOutputPath, r.cfg.SegmentsDir)
	if err != nil {
		return err
	}
	r.segmentsDir = segmentsDir

	w, err := cassette.NewWriter(r.cfg.OutputPath)
	if err != nil {
		return err
	}
	r.writer = w
	r.rtpRecorder = recorder.NewRTPRecorder(w)

	r.ocs = nextcloud.NewOCSClient(r.baseURL, r.cfg.Insecure)
	if err := r.bootstrap(ctx); err != nil {
		_ = r.cleanup(context.Background())
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, 3)

	go func() {
		errCh <- r.eventLoop(runCtx)
	}()
	go func() {
		errCh <- r.requestOfferLoop(runCtx)
	}()

	log.Printf(
		"talk recorder running: room=%s duration=%s archive=%s final=%s segments=%s",
		r.roomToken,
		r.cfg.Duration,
		r.cfg.OutputPath,
		r.finalOutputPath,
		r.segmentsDir,
	)

	select {
	case <-time.After(r.cfg.Duration):
		log.Printf("talk recorder duration reached: %s", r.cfg.Duration)
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			cancel()
			_ = r.cleanup(context.Background())
			return err
		}
	case <-runCtx.Done():
	}

	cancel()

	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cleanupCancel()
	if err := r.cleanup(cleanupCtx); err != nil {
		return err
	}
	return nil
}

func (r *Recorder) bootstrap(ctx context.Context) error {
	if err := r.ocs.GetRoom(ctx, r.roomToken); err != nil {
		return fmt.Errorf("room check failed: %w", err)
	}

	nextcloudSessionID, err := r.ocs.MarkParticipantActive(ctx, r.roomToken, r.cfg.GuestName)
	if err != nil {
		return fmt.Errorf("participants/active failed: %w", err)
	}
	r.nextcloudSessionID = nextcloudSessionID

	if err := r.ocs.SetGuestName(ctx, r.roomToken, r.cfg.GuestName); err != nil {
		log.Printf("guest display name not set (%v); continuing", err)
	}

	settings, err := r.ocs.FetchSignalingSettings(ctx, r.roomToken)
	if err != nil {
		return fmt.Errorf("signaling settings failed: %w", err)
	}
	r.settings = settings

	wsServer := r.settings.PrimarySignalingServer()
	if wsServer == "" {
		return errors.New("signaling settings missing signaling server")
	}

	r.signaling = signaling.NewClient(toWSURL(wsServer), r.cfg.Insecure)
	if err := r.signaling.Connect(ctx); err != nil {
		return err
	}

	if err := r.hello(ctx); err != nil {
		return err
	}
	if err := r.joinSignalingRoom(ctx); err != nil {
		return err
	}
	if err := r.ocs.JoinCall(ctx, r.roomToken, r.cfg.JoinFlags); err != nil {
		if strings.Contains(err.Error(), "code=404") {
			log.Printf("join call returned 404 on this deployment; continuing with signaling-room only mode")
		} else {
			return fmt.Errorf("join call failed: %w", err)
		}
	}

	log.Printf("talk bootstrap complete: base=%s token=%s session=%s signaling_session=%s", r.baseURL, r.roomToken, r.nextcloudSessionID, r.signalingSessionID)
	return nil
}

func (r *Recorder) cleanup(ctx context.Context) error {
	var firstErr error
	composeOK := false
	composeErrText := ""
	intermediateCleaned := false

	r.mu.Lock()
	peers := make([]*subscriberPeer, 0, len(r.subscribers))
	for _, p := range r.subscribers {
		peers = append(peers, p)
	}
	r.subscribers = map[string]*subscriberPeer{}
	r.mu.Unlock()

	for _, p := range peers {
		if err := p.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if r.signaling != nil {
		_ = r.signaling.Send(map[string]any{"type": "bye", "bye": map[string]any{}})
		if err := r.signaling.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		r.signaling = nil
	}

	if r.ocs != nil {
		if err := r.ocs.LeaveCall(ctx, r.roomToken); err != nil {
			log.Printf("leave call failed (continuing): %v", err)
		}
		if err := r.ocs.LeaveParticipantActive(ctx, r.roomToken); err != nil {
			log.Printf("leave participants/active failed (continuing): %v", err)
		}
	}

	if r.writer != nil {
		if err := r.writer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		r.writer = nil
	}

	r.sessionMu.Lock()
	sessions := make([]*sessionCapture, len(r.sessionOrder))
	copy(sessions, r.sessionOrder)
	r.sessionMu.Unlock()

	for _, session := range sessions {
		if session.AudioWriter != nil {
			if err := session.AudioWriter.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
			session.AudioWriter = nil
		}
		if session.VideoWriter != nil {
			if err := session.VideoWriter.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
			session.VideoWriter = nil
		}
	}

	if err := r.composeFinalOutput(sessions); err != nil {
		composeErrText = err.Error()
		if firstErr == nil {
			firstErr = err
		}
		log.Printf("compose final output failed: %v", err)
	} else {
		composeOK = true
		log.Printf("composed final multi-track output: %s", r.finalOutputPath)
		if r.cfg.CleanupIntermediate {
			if err := os.RemoveAll(r.segmentsDir); err != nil {
				log.Printf("cleanup intermediate files failed: %v", err)
			} else {
				intermediateCleaned = true
				log.Printf("cleaned intermediate files: %s", r.segmentsDir)
			}
		} else {
			log.Printf("kept intermediate files: %s", r.segmentsDir)
		}
	}

	reportPath := deriveReportPath(r.finalOutputPath, r.cfg.OutputPath)
	if err := r.writeReport(
		reportPath,
		sessions,
		composeOK,
		composeErrText,
		intermediateCleaned,
	); err != nil {
		if firstErr == nil {
			firstErr = err
		}
		log.Printf("write report failed: %v", err)
	} else {
		log.Printf("wrote report: %s", reportPath)
	}

	return firstErr
}

func (r *Recorder) hello(ctx context.Context) error {
	if r.settings == nil {
		return errors.New("signaling settings not loaded")
	}

	helloAuth := r.settings.HelloAuthParams
	versions := make([]string, 0, 2)
	if _, ok := helloAuth["2.0"]; ok {
		versions = append(versions, "2.0")
	}
	if _, ok := helloAuth["1.0"]; ok {
		versions = append(versions, "1.0")
	}
	if len(versions) == 0 {
		return errors.New("signaling settings did not include helloAuthParams")
	}

	backendURL := strings.TrimRight(r.baseURL, "/") + "/ocs/v2.php/apps/spreed/api/v3/signaling/backend"

	for _, version := range versions {
		authRaw := helloAuth[version]
		var authParams any
		if err := json.Unmarshal(authRaw, &authParams); err != nil {
			return fmt.Errorf("decode helloAuthParams[%s]: %w", version, err)
		}

		req := map[string]any{
			"type": "hello",
			"hello": map[string]any{
				"version": version,
				"auth": map[string]any{
					"url":    backendURL,
					"params": authParams,
				},
				"features": []any{"chat-relay"},
			},
		}

		resp, err := r.signaling.Request(ctx, req, 15*time.Second)
		if err != nil {
			log.Printf("hello version %s request failed: %v", version, err)
			continue
		}
		if asString(resp["type"]) != "hello" {
			log.Printf("hello version %s returned type=%s", version, asString(resp["type"]))
			continue
		}

		helloMap := asMap(resp["hello"])
		r.signalingSessionID = asString(helloMap["sessionid"])
		if r.signalingSessionID == "" {
			return errors.New("hello response missing signaling sessionid")
		}
		log.Printf("hello ok (version %s)", version)
		return nil
	}

	return errors.New("all signaling hello attempts failed")
}

func (r *Recorder) joinSignalingRoom(ctx context.Context) error {
	req := map[string]any{
		"type": "room",
		"room": map[string]any{
			"roomid":    r.roomToken,
			"sessionid": r.nextcloudSessionID,
		},
	}
	resp, err := r.signaling.Request(ctx, req, 15*time.Second)
	if err != nil {
		return fmt.Errorf("signaling room join failed: %w", err)
	}
	if asString(resp["type"]) == "error" {
		return fmt.Errorf("signaling room join returned error: %v", resp)
	}
	log.Printf("joined signaling room")
	return nil
}

func (r *Recorder) eventLoop(ctx context.Context) error {
	for {
		event, err := r.signaling.NextEvent(ctx, 1*time.Second)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				continue
			}
			return fmt.Errorf("signaling event loop: %w", err)
		}

		eventType := asString(event["type"])
		if eventType == "_connection_error" {
			return fmt.Errorf("signaling connection error: %s", asString(event["error"]))
		}

		if r.cfg.DebugSignaling {
			log.Printf("signaling event type=%s", eventType)
		}

		switch eventType {
		case "event":
			if err := r.handleRoomEvent(asMap(event["event"])); err != nil {
				return err
			}
		case "message":
			message := asMap(event["message"])
			data := asMap(message["data"])
			if len(data) == 0 {
				continue
			}
			sender := asMap(message["sender"])
			if asString(data["from"]) == "" {
				if sid := asString(sender["sessionid"]); sid != "" {
					data["from"] = sid
				}
			}
			if err := r.handleSignalingData(data); err != nil {
				return err
			}
		}
	}
}

func (r *Recorder) requestOfferLoop(ctx context.Context) error {
	if r.cfg.RequestOfferInterval <= 0 {
		return nil
	}
	ticker := time.NewTicker(r.cfg.RequestOfferInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		r.mu.Lock()
		peers := make([]*subscriberPeer, 0, len(r.subscribers))
		for _, p := range r.subscribers {
			peers = append(peers, p)
		}
		r.mu.Unlock()

		for _, p := range peers {
			if p.hasOffer() {
				continue
			}
			if err := p.requestOffer(); err != nil {
				log.Printf("requestoffer failed for %s: %v", p.remoteSessionID, err)
			}
		}
	}
}

func (r *Recorder) handleRoomEvent(roomEvent map[string]any) error {
	if len(roomEvent) == 0 {
		return nil
	}
	target := asString(roomEvent["target"])
	eventType := asString(roomEvent["type"])

	if target == "room" && eventType == "join" {
		for _, item := range asSlice(roomEvent["join"]) {
			joinItem := asMap(item)
			if len(joinItem) == 0 {
				continue
			}
			if _, ok := joinItem["roomsessionid"]; !ok {
				continue
			}
			remoteSessionID := asString(joinItem["sessionid"])
			if remoteSessionID == "" {
				continue
			}
			if _, err := r.ensureSubscriber(remoteSessionID); err != nil {
				return err
			}
		}
		return nil
	}

	if target == "room" && eventType == "leave" {
		for _, item := range asSlice(roomEvent["leave"]) {
			remoteSessionID := asString(item)
			if remoteSessionID == "" {
				continue
			}
			if err := r.removeSubscriber(remoteSessionID); err != nil {
				log.Printf("remove subscriber %s failed: %v", remoteSessionID, err)
			}
		}
	}

	return nil
}

func (r *Recorder) handleSignalingData(data map[string]any) error {
	roomType := asString(data["roomType"])
	if roomType != "" && roomType != "video" {
		return nil
	}

	msgType := asString(data["type"])
	if msgType != "offer" && msgType != "candidate" && msgType != "endOfCandidates" {
		return nil
	}

	fromSession := asString(data["from"])
	if fromSession == "" {
		return nil
	}

	peer, err := r.ensureSubscriber(fromSession)
	if err != nil {
		return err
	}
	if peer == nil {
		return nil
	}
	return peer.handleMessage(data)
}

func (r *Recorder) ensureSubscriber(remoteSessionID string) (*subscriberPeer, error) {
	if remoteSessionID == "" || remoteSessionID == r.signalingSessionID {
		return nil, nil
	}

	r.mu.Lock()
	existing := r.subscribers[remoteSessionID]
	r.mu.Unlock()
	if existing != nil {
		return existing, nil
	}

	peer, err := r.newSubscriberPeer(remoteSessionID)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	if current := r.subscribers[remoteSessionID]; current != nil {
		r.mu.Unlock()
		_ = peer.close()
		return current, nil
	}
	r.subscribers[remoteSessionID] = peer
	r.mu.Unlock()

	log.Printf("subscribing to remote session %s", remoteSessionID)
	if err := peer.requestOffer(); err != nil {
		log.Printf("initial requestoffer failed for %s: %v", remoteSessionID, err)
	}

	return peer, nil
}

func (r *Recorder) removeSubscriber(remoteSessionID string) error {
	r.mu.Lock()
	peer := r.subscribers[remoteSessionID]
	delete(r.subscribers, remoteSessionID)
	r.mu.Unlock()
	if peer == nil {
		return nil
	}
	log.Printf("closing subscriber for remote session %s", remoteSessionID)
	return peer.close()
}

func (r *Recorder) onRemoteTrack(ctx context.Context, track *webrtc.TrackRemote, remoteSessionID string) {
	kind := strings.ToLower(track.Kind().String())
	codec := strings.ToLower(track.Codec().MimeType)

	session, err := r.ensureSessionCapture(remoteSessionID)
	if err != nil {
		log.Printf("ensure session capture failed sid=%s: %v", remoteSessionID, err)
		return
	}

	if err := r.ensureIntermediateWriter(session, kind, codec); err != nil {
		log.Printf("prepare intermediate writer failed sid=%s kind=%s codec=%s: %v", remoteSessionID, kind, codec, err)
	}

	participant := recorder.ParticipantRef{
		ParticipantName: session.ParticipantName,
		ParticipantID:   "",
		RemoteSessionID: remoteSessionID,
	}
	log.Printf("remote track: sid=%s kind=%s track=%s stream=%s codec=%s", remoteSessionID, kind, track.ID(), track.StreamID(), track.Codec().MimeType)

	meta := cassette.TrackMetadata{
		StartedAtUnixNano: time.Now().UnixNano(),
		ParticipantName:   participant.ParticipantName,
		ParticipantID:     participant.ParticipantID,
		RemoteSessionID:   participant.RemoteSessionID,
		TrackID:           track.ID(),
		StreamID:          track.StreamID(),
		RID:               track.RID(),
		Kind:              kind,
		Codec:             track.Codec().MimeType,
		ClockRate:         track.Codec().ClockRate,
		SSRC:              uint32(track.SSRC()),
		PayloadType:       uint8(track.PayloadType()),
	}

	trackRef, err := r.rtpRecorder.RegisterTrack(meta)
	if err != nil {
		log.Printf("register track failed sid=%s track=%s: %v", remoteSessionID, track.ID(), err)
		return
	}

	trackEnded := false
	for {
		pkt, _, readErr := track.ReadRTP()
		if readErr != nil {
			reason := "read-error"
			if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
				reason = "context-cancelled"
			} else if strings.Contains(strings.ToLower(readErr.Error()), "eof") {
				reason = "eof"
			}
			_ = r.rtpRecorder.EndTrack(trackRef, reason, time.Now())
			trackEnded = true
			if !errors.Is(readErr, context.Canceled) && !strings.Contains(strings.ToLower(readErr.Error()), "eof") {
				log.Printf("capture track read failed sid=%s track=%s: %v", remoteSessionID, track.ID(), readErr)
			}
			break
		}

		if err := r.rtpRecorder.WritePacket(trackRef, pkt, time.Now()); err != nil {
			_ = r.rtpRecorder.EndTrack(trackRef, "archive-write-error", time.Now())
			trackEnded = true
			log.Printf("archive packet write failed sid=%s track=%s: %v", remoteSessionID, track.ID(), err)
			break
		}

		r.sessionMu.Lock()
		switch kind {
		case "audio":
			if session.AudioWriter != nil {
				if err := session.AudioWriter.WriteRTP(pkt); err != nil {
					log.Printf("audio intermediate write failed sid=%s track=%s: %v", remoteSessionID, track.ID(), err)
				}
			}
			session.AudioPackets++
		case "video":
			if session.VideoWriter != nil {
				if err := session.VideoWriter.WriteRTP(pkt); err != nil {
					log.Printf("video intermediate write failed sid=%s track=%s: %v", remoteSessionID, track.ID(), err)
				}
			}
			session.VideoPackets++
		}
		r.sessionMu.Unlock()
	}

	if !trackEnded {
		if err := r.rtpRecorder.EndTrack(trackRef, "ended", time.Now()); err != nil {
			log.Printf("end track marker failed sid=%s track=%s: %v", remoteSessionID, track.ID(), err)
		}
	}

	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("capture track failed sid=%s track=%s: %v", remoteSessionID, track.ID(), err)
	}
}

func (r *Recorder) ensureSessionCapture(remoteSessionID string) (*sessionCapture, error) {
	r.sessionMu.Lock()
	defer r.sessionMu.Unlock()

	if existing := r.sessionsByRemote[remoteSessionID]; existing != nil {
		return existing, nil
	}

	index := len(r.sessionOrder) + 1
	participantName := fmt.Sprintf("participant-%s", shortID(remoteSessionID))

	session := &sessionCapture{
		RemoteSessionID: remoteSessionID,
		ParticipantName: participantName,
		Index:           index,
		StartedAt:       time.Now().UTC(),
		AudioPath:       filepath.Join(r.segmentsDir, fmt.Sprintf("session-%02d-audio.ogg", index)),
		VideoPath:       filepath.Join(r.segmentsDir, fmt.Sprintf("session-%02d-video.ivf", index)),
		MKVPath:         filepath.Join(r.segmentsDir, fmt.Sprintf("session-%02d.mkv", index)),
	}

	r.sessionsByRemote[remoteSessionID] = session
	r.sessionOrder = append(r.sessionOrder, session)
	return session, nil
}

func (r *Recorder) ensureIntermediateWriter(session *sessionCapture, kind string, codec string) error {
	r.sessionMu.Lock()
	defer r.sessionMu.Unlock()

	switch kind {
	case "audio":
		if session.AudioWriter != nil {
			return nil
		}
		if !strings.Contains(codec, "opus") {
			log.Printf("audio codec not supported for intermediate packet mux sid=%s codec=%s", session.RemoteSessionID, codec)
			return nil
		}
		writer, err := oggwriter.New(session.AudioPath, 48000, 2)
		if err != nil {
			return fmt.Errorf("create ogg writer: %w", err)
		}
		session.AudioWriter = writer
		session.HasAudio = true
		return nil
	case "video":
		if session.VideoWriter != nil {
			return nil
		}
		if !strings.Contains(codec, "vp8") {
			log.Printf("video codec not supported for intermediate packet mux sid=%s codec=%s", session.RemoteSessionID, codec)
			return nil
		}
		writer, err := ivfwriter.New(session.VideoPath)
		if err != nil {
			return fmt.Errorf("create ivf writer: %w", err)
		}
		session.VideoWriter = writer
		session.HasVideo = true
		return nil
	default:
		return nil
	}
}

func (r *Recorder) composeFinalOutput(sessions []*sessionCapture) error {
	if len(sessions) == 0 {
		return errors.New("no captured sessions available for compose")
	}

	prepared := make([]*sessionCapture, 0, len(sessions))
	for _, session := range sessions {
		if err := composeSessionMKV(session); err != nil {
			log.Printf("compose session mkv failed sid=%s: %v", session.RemoteSessionID, err)
			continue
		}
		if fileExists(session.MKVPath) {
			prepared = append(prepared, session)
		}
	}

	if len(prepared) == 0 {
		return errors.New("no session MKVs available for final merge")
	}

	if len(prepared) == 1 {
		if err := copyFile(prepared[0].MKVPath, r.finalOutputPath); err != nil {
			return err
		}
		return nil
	}

	earliest := prepared[0].StartedAt
	for _, s := range prepared[1:] {
		if s.StartedAt.Before(earliest) {
			earliest = s.StartedAt
		}
	}

	args := []string{"-y", "-v", "error"}
	for _, session := range prepared {
		desired := session.StartedAt.Sub(earliest).Seconds()
		sourceStart, _ := probeMinStreamStartSeconds(session.MKVPath)
		offset := desired - sourceStart
		if math.Abs(offset) > 1e-6 {
			args = append(args, "-itsoffset", fmt.Sprintf("%.6f", offset))
		}
		args = append(args, "-i", session.MKVPath)
	}

	videoIndex := 0
	audioIndex := 0
	for i, session := range prepared {
		if session.HasVideo {
			args = append(args, "-map", fmt.Sprintf("%d:v?", i))
			args = append(args,
				fmt.Sprintf("-metadata:s:v:%d", videoIndex),
				fmt.Sprintf("title=%s video", session.ParticipantName),
				fmt.Sprintf("-metadata:s:v:%d", videoIndex),
				fmt.Sprintf("remote_session_id=%s", session.RemoteSessionID),
			)
			videoIndex++
		}

		if session.HasAudio {
			args = append(args, "-map", fmt.Sprintf("%d:a?", i))
			args = append(args,
				fmt.Sprintf("-metadata:s:a:%d", audioIndex),
				fmt.Sprintf("title=%s audio", session.ParticipantName),
				fmt.Sprintf("-metadata:s:a:%d", audioIndex),
				fmt.Sprintf("remote_session_id=%s", session.RemoteSessionID),
			)
			audioIndex++
		}
	}

	args = append(args,
		"-c", "copy",
		"-metadata", fmt.Sprintf("title=Cassini Go Recording %s", r.roomToken),
		"-metadata", fmt.Sprintf("call_url=%s", r.cfg.CallURL),
		"-metadata", fmt.Sprintf("room_token=%s", r.roomToken),
		"-metadata", fmt.Sprintf("observer_name=%s", r.cfg.GuestName),
		r.finalOutputPath,
	)

	if err := runCommand("ffmpeg", args...); err != nil {
		return fmt.Errorf("final merge failed: %w", err)
	}
	return nil
}

func composeSessionMKV(session *sessionCapture) error {
	hasAudio := fileExists(session.AudioPath)
	hasVideo := fileExists(session.VideoPath)
	session.HasAudio = hasAudio
	session.HasVideo = hasVideo
	if !hasAudio && !hasVideo {
		return errors.New("no intermediate audio/video files")
	}

	args := []string{"-y", "-v", "error"}
	if hasVideo {
		args = append(args, "-i", session.VideoPath)
	}
	if hasAudio {
		args = append(args, "-i", session.AudioPath)
	}

	if hasVideo {
		args = append(args, "-map", "0:v:0")
		if hasAudio {
			args = append(args, "-map", "1:a:0")
		}
	} else {
		args = append(args, "-map", "0:a:0")
	}

	args = append(args,
		"-c", "copy",
		"-metadata", fmt.Sprintf("remote_session_id=%s", session.RemoteSessionID),
		"-metadata", fmt.Sprintf("participant_name=%s", session.ParticipantName),
		session.MKVPath,
	)

	return runCommand("ffmpeg", args...)
}

func (r *Recorder) newSubscriberPeer(remoteSessionID string) (*subscriberPeer, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: buildICEServers(r.settings, r.cfg.TurnMode)})
	if err != nil {
		return nil, fmt.Errorf("create peer connection for %s: %w", remoteSessionID, err)
	}

	peer := &subscriberPeer{
		owner:           r,
		remoteSessionID: remoteSessionID,
		pc:              pc,
	}

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("subscriber %s ICE state=%s", remoteSessionID, state.String())
	})
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			if err := peer.sendEndOfCandidates("ice-gathering-complete"); err != nil {
				log.Printf("send endOfCandidates failed sid=%s: %v", remoteSessionID, err)
			}
			return
		}
		init := candidate.ToJSON()
		candidatePayload := map[string]any{
			"candidate": init.Candidate,
		}
		if init.SDPMid != nil {
			candidatePayload["sdpMid"] = *init.SDPMid
		}
		if init.SDPMLineIndex != nil {
			candidatePayload["sdpMLineIndex"] = int(*init.SDPMLineIndex)
		}
		if err := r.sendPeerMessage(remoteSessionID, "candidate", map[string]any{"candidate": candidatePayload}, peer.currentSIDValue()); err != nil {
			log.Printf("send candidate failed sid=%s: %v", remoteSessionID, err)
		}
	})
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		go r.onRemoteTrack(context.Background(), track, remoteSessionID)
	})

	return peer, nil
}

func (r *Recorder) sendRequestOffer(remoteSessionID string) error {
	return r.sendPeerMessage(remoteSessionID, "requestoffer", nil, "")
}

func (r *Recorder) sendPeerMessage(toSession, msgType string, payload map[string]any, sid string) error {
	data := map[string]any{
		"to":       toSession,
		"roomType": "video",
		"type":     msgType,
	}
	if payload != nil {
		data["payload"] = payload
	}
	if sid != "" {
		data["sid"] = sid
	}
	if r.cfg.DebugSignaling {
		log.Printf("send signaling message to=%s type=%s sid=%s", toSession, msgType, sid)
	}

	wrapper := map[string]any{
		"type": "message",
		"message": map[string]any{
			"recipient": map[string]any{
				"type":      "session",
				"sessionid": toSession,
			},
			"data": data,
		},
	}
	return r.signaling.Send(wrapper)
}

func (p *subscriberPeer) requestOffer() error {
	now := time.Now()
	p.mu.Lock()
	if !p.awaitingOfferSince.IsZero() {
		if now.Sub(p.awaitingOfferSince) < requestOfferResponseTimeout {
			p.mu.Unlock()
			return nil
		}
		p.awaitingOfferSince = time.Time{}
	}

	maxAttempts := p.owner.cfg.MaxRequestOfferAttempts
	if maxAttempts > 0 && p.requestOfferAttempts >= maxAttempts {
		if !p.offerExhaustedLogged {
			log.Printf("subscriber %s received no offer after %d requestoffer attempts; backing off", p.remoteSessionID, p.requestOfferAttempts)
			p.offerExhaustedLogged = true
		}
		p.mu.Unlock()
		return nil
	}

	p.requestOfferAttempts++
	p.awaitingOfferSince = now
	p.mu.Unlock()

	return p.owner.sendRequestOffer(p.remoteSessionID)
}

func (p *subscriberPeer) hasOffer() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.offerReceived
}

func (p *subscriberPeer) currentSIDValue() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.currentSID
}

func (p *subscriberPeer) sendEndOfCandidates(reason string) error {
	p.mu.Lock()
	if p.endOfCandidatesSent {
		p.mu.Unlock()
		return nil
	}
	p.endOfCandidatesSent = true
	sid := p.currentSID
	p.mu.Unlock()

	if p.owner.cfg.DebugSignaling {
		log.Printf("subscriber %s -> send endOfCandidates (%s)", p.remoteSessionID, reason)
	}
	return p.owner.sendPeerMessage(p.remoteSessionID, "endOfCandidates", map[string]any{}, sid)
}

func (p *subscriberPeer) handleMessage(data map[string]any) error {
	msgType := asString(data["type"])
	payload := asMap(data["payload"])

	switch msgType {
	case "offer":
		sdp := asString(payload["sdp"])
		if sdp == "" {
			return nil
		}

		p.mu.Lock()
		p.offerReceived = true
		p.awaitingOfferSince = time.Time{}
		p.currentSID = asString(data["sid"])
		p.endOfCandidatesSent = false
		sid := p.currentSID
		p.mu.Unlock()

		remoteDesc := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}
		if err := p.pc.SetRemoteDescription(remoteDesc); err != nil {
			return fmt.Errorf("set remote offer for %s: %w", p.remoteSessionID, err)
		}

		answer, err := p.pc.CreateAnswer(nil)
		if err != nil {
			return fmt.Errorf("create answer for %s: %w", p.remoteSessionID, err)
		}
		gatherComplete := webrtc.GatheringCompletePromise(p.pc)
		if err := p.pc.SetLocalDescription(answer); err != nil {
			return fmt.Errorf("set local answer for %s: %w", p.remoteSessionID, err)
		}

		select {
		case <-gatherComplete:
		case <-time.After(4 * time.Second):
			log.Printf("ICE gather timeout for subscriber %s; continuing", p.remoteSessionID)
		}

		local := p.pc.LocalDescription()
		if local == nil {
			return fmt.Errorf("local description missing for %s", p.remoteSessionID)
		}

		if err := p.owner.sendPeerMessage(
			p.remoteSessionID,
			"answer",
			map[string]any{"type": local.Type.String(), "sdp": local.SDP},
			sid,
		); err != nil {
			return fmt.Errorf("send answer for %s: %w", p.remoteSessionID, err)
		}

		return p.sendEndOfCandidates("post-answer")

	case "candidate":
		candidatePayload := extractCandidatePayload(payload)
		if len(candidatePayload) == 0 {
			return nil
		}

		raw := asString(candidatePayload["candidate"])
		if raw == "" {
			return nil
		}
		if !strings.HasPrefix(raw, "candidate:") {
			raw = "candidate:" + raw
		}

		ice := webrtc.ICECandidateInit{Candidate: raw}
		if sdpMid := asString(candidatePayload["sdpMid"]); sdpMid != "" {
			ice.SDPMid = &sdpMid
		}
		if idx, ok := asUint16(candidatePayload["sdpMLineIndex"]); ok {
			ice.SDPMLineIndex = &idx
		}
		if err := p.pc.AddICECandidate(ice); err != nil {
			return fmt.Errorf("add ICE candidate for %s: %w", p.remoteSessionID, err)
		}
		return nil

	case "endOfCandidates":
		return nil
	}

	return nil
}

func (p *subscriberPeer) close() error {
	return p.pc.Close()
}

func toWSURL(serverURL string) string {
	u := strings.TrimRight(serverURL, "/")
	if strings.HasPrefix(u, "https://") {
		u = "wss://" + strings.TrimPrefix(u, "https://")
	} else if strings.HasPrefix(u, "http://") {
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	return u + "/spreed"
}

func buildICEServers(settings *nextcloud.SignalingSettings, turnMode string) []webrtc.ICEServer {
	if settings == nil {
		return nil
	}
	out := make([]webrtc.ICEServer, 0, len(settings.StunServers)+len(settings.TurnServers))

	for _, s := range settings.StunServers {
		urls := normalizeURLs(s.URLs)
		if len(urls) == 0 {
			continue
		}
		out = append(out, webrtc.ICEServer{URLs: urls, Username: s.Username, Credential: s.Credential})
	}

	if turnMode == "off" {
		return out
	}

	for _, s := range settings.TurnServers {
		urls := normalizeURLs(s.URLs)
		if turnMode == "udp-only" {
			filtered := urls[:0]
			for _, u := range urls {
				if !strings.Contains(strings.ToLower(u), "transport=tcp") {
					filtered = append(filtered, u)
				}
			}
			urls = filtered
		}
		if len(urls) == 0 {
			continue
		}
		out = append(out, webrtc.ICEServer{URLs: urls, Username: s.Username, Credential: s.Credential})
	}

	return out
}

func normalizeURLs(raw any) []string {
	switch v := raw.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func ensureOutputDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(filepath.Clean(dir), 0o755)
}

func (r *Recorder) writeReport(
	reportPath string,
	sessions []*sessionCapture,
	composeOK bool,
	composeErr string,
	intermediateCleaned bool,
) error {
	archiveExists, archiveSize := fileState(r.cfg.OutputPath)
	finalExists, finalSize := fileState(r.finalOutputPath)
	segmentsExists := dirExists(r.segmentsDir)

	trackCount, packetCount, archiveStatsErr := collectArchiveStats(r.cfg.OutputPath)
	warnings := make([]string, 0, 4)
	if composeErr != "" {
		warnings = append(warnings, "final compose failed: "+composeErr)
	}
	if archiveStatsErr != nil {
		warnings = append(warnings, "archive stats unavailable: "+archiveStatsErr.Error())
	}

	type sessionOutput struct {
		RemoteSessionID string `json:"remote_session_id"`
		ParticipantName string `json:"participant_name"`
		Index           int    `json:"index"`
		StartedAt       string `json:"started_at"`

		AudioPackets int `json:"audio_packets"`
		VideoPackets int `json:"video_packets"`

		AudioPath      string `json:"audio_path"`
		AudioExists    bool   `json:"audio_exists"`
		AudioSizeBytes int64  `json:"audio_size_bytes"`

		VideoPath      string `json:"video_path"`
		VideoExists    bool   `json:"video_exists"`
		VideoSizeBytes int64  `json:"video_size_bytes"`

		SessionMKVPath      string `json:"session_mkv_path"`
		SessionMKVExists    bool   `json:"session_mkv_exists"`
		SessionMKVSizeBytes int64  `json:"session_mkv_size_bytes"`
	}

	sessionOutputs := make([]sessionOutput, 0, len(sessions))
	for _, s := range sessions {
		audioExists, audioSize := fileState(s.AudioPath)
		videoExists, videoSize := fileState(s.VideoPath)
		sessionMKVExists, sessionMKVSize := fileState(s.MKVPath)
		sessionOutputs = append(sessionOutputs, sessionOutput{
			RemoteSessionID:     s.RemoteSessionID,
			ParticipantName:     s.ParticipantName,
			Index:               s.Index,
			StartedAt:           s.StartedAt.UTC().Format(time.RFC3339Nano),
			AudioPackets:        s.AudioPackets,
			VideoPackets:        s.VideoPackets,
			AudioPath:           s.AudioPath,
			AudioExists:         audioExists,
			AudioSizeBytes:      audioSize,
			VideoPath:           s.VideoPath,
			VideoExists:         videoExists,
			VideoSizeBytes:      videoSize,
			SessionMKVPath:      s.MKVPath,
			SessionMKVExists:    sessionMKVExists,
			SessionMKVSizeBytes: sessionMKVSize,
		})
	}

	report := map[string]any{
		"generated_at":     rfc3339Now(),
		"started_at":       r.startedAt.UTC().Format(time.RFC3339Nano),
		"call_url":         r.cfg.CallURL,
		"base_url":         r.baseURL,
		"room_token":       r.roomToken,
		"guest_name":       r.cfg.GuestName,
		"duration_seconds": int(r.cfg.Duration / time.Second),
		"join_flags":       r.cfg.JoinFlags,
		"turn_mode":        r.cfg.TurnMode,

		"archive_output": map[string]any{
			"path":       r.cfg.OutputPath,
			"exists":     archiveExists,
			"size_bytes": archiveSize,
		},
		"final_output": map[string]any{
			"path":          r.finalOutputPath,
			"exists":        finalExists,
			"size_bytes":    finalSize,
			"compose_ok":    composeOK,
			"compose_error": composeErr,
		},
		"segments": map[string]any{
			"path":                  r.segmentsDir,
			"exists":                segmentsExists,
			"cleanup_requested":     r.cfg.CleanupIntermediate,
			"cleanup_performed":     intermediateCleaned,
			"intermediate_retained": !intermediateCleaned,
		},
		"archive_stats": map[string]any{
			"track_count":      trackCount,
			"rtp_packet_count": packetCount,
		},
		"session_outputs": sessionOutputs,
		"warnings":        warnings,
	}

	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report JSON: %w", err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(reportPath, body, 0o644); err != nil {
		return fmt.Errorf("write report file: %w", err)
	}
	return nil
}

func collectArchiveStats(path string) (int, int, error) {
	reader, err := cassette.OpenReader(path)
	if err != nil {
		return 0, 0, err
	}
	defer reader.Close()

	trackCount := 0
	packetCount := 0
	for {
		rec, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return 0, 0, err
		}
		switch rec.Type {
		case cassette.RecordTrackStart:
			trackCount++
		case cassette.RecordRTPPacket:
			packetCount++
		}
	}
	return trackCount, packetCount, nil
}

func fileState(path string) (bool, int64) {
	if path == "" {
		return false, 0
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false, 0
	}
	return true, info.Size()
}

func dirExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func deriveReportPath(finalOutput string, archiveOutput string) string {
	if finalOutput != "" {
		return finalOutput + ".json"
	}
	return archiveOutput + ".json"
}

func rfc3339Now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func deriveFinalOutputPath(archiveOutput string, finalOutput string) string {
	if finalOutput != "" {
		return finalOutput
	}
	if strings.HasSuffix(strings.ToLower(archiveOutput), ".csr") {
		return archiveOutput[:len(archiveOutput)-4] + ".mkv"
	}
	if strings.HasSuffix(strings.ToLower(archiveOutput), ".mkv") {
		return archiveOutput
	}
	return archiveOutput + ".mkv"
}

func prepareSegmentsDir(finalOutput string, configured string) (string, error) {
	if configured != "" {
		if err := os.MkdirAll(configured, 0o755); err != nil {
			return "", fmt.Errorf("create configured segments dir: %w", err)
		}
		return configured, nil
	}

	parent := filepath.Dir(finalOutput)
	if parent == "" || parent == "." {
		parent = os.TempDir()
	}
	base := strings.TrimSuffix(filepath.Base(finalOutput), filepath.Ext(finalOutput))
	if base == "" {
		base = "gocassini"
	}
	dir, err := os.MkdirTemp(parent, base+"-segments-")
	if err != nil {
		return "", fmt.Errorf("create segments temp dir: %w", err)
	}
	return dir, nil
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Size() > 0
}

func copyFile(src string, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open src file: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create dst file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy file: %w", err)
	}
	return out.Close()
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		errText := strings.TrimSpace(string(out))
		if errText == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, errText)
	}
	return nil
}

func probeMinStreamStartSeconds(path string) (float64, bool) {
	cmd := exec.Command(
		"ffprobe",
		"-v",
		"error",
		"-show_entries",
		"stream=start_time",
		"-of",
		"default=noprint_wrappers=1:nokey=1",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}

	lines := strings.Split(string(out), "\n")
	min := math.MaxFloat64
	have := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, "N/A") {
			continue
		}
		value, convErr := strconv.ParseFloat(line, 64)
		if convErr != nil {
			continue
		}
		if value < min {
			min = value
		}
		have = true
	}
	if !have {
		return 0, false
	}
	return min, true
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	if s == nil {
		return []any{}
	}
	return s
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asUint16(v any) (uint16, bool) {
	switch t := v.(type) {
	case float64:
		if t < 0 || t > 65535 {
			return 0, false
		}
		return uint16(t), true
	case int:
		if t < 0 || t > 65535 {
			return 0, false
		}
		return uint16(t), true
	default:
		return 0, false
	}
}

func shortID(v string) string {
	if len(v) > 8 {
		return v[:8]
	}
	if v == "" {
		return "unknown"
	}
	return v
}

func extractCandidatePayload(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	candidate := payload["candidate"]
	switch c := candidate.(type) {
	case map[string]any:
		return c
	case string:
		if c == "" {
			return nil
		}
		return map[string]any{"candidate": c}
	default:
		if val := asString(payload["candidate"]); val != "" {
			return map[string]any{"candidate": val}
		}
		return nil
	}
}
