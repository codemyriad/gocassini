package talk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gocassini/internal/config"
	"gocassini/internal/nextcloud"
	"gocassini/internal/signaling"
	coreremux "gocassini/pkg/core/remux"

	"github.com/pion/webrtc/v4"
)

const requestOfferResponseTimeout = 8 * time.Second

type roomEmptyTimerAction uint8

const (
	roomEmptyTimerNoop roomEmptyTimerAction = iota
	roomEmptyTimerArm
	roomEmptyTimerDisarm
)

type Recorder struct {
	cfg config.Config

	baseURL        string
	connectBaseURL string
	roomToken      string

	ocs      *nextcloud.OCSClient
	settings *nextcloud.SignalingSettings

	nextcloudSessionID string
	signalingSessionID string

	signaling *signaling.Client

	sessionArtifact *sessionCaptureArtifact
	sessionPath     string
	artifactRemux   *coreremux.BuildResult

	finalOutputPath string
	segmentsDir     string
	startedAt       time.Time

	sessionMu        sync.Mutex
	sessionsByRemote map[string]*sessionCapture
	sessionOrder     []*sessionCapture
	identityByRemote map[string]participantIdentity

	mu          sync.Mutex
	subscribers map[string]*subscriberPeer

	subscriberUpdates chan struct{}
}

type sessionCapture struct {
	RemoteSessionID string
	ParticipantName string
	ParticipantID   string
	Index           int
	StartedAt       time.Time

	AudioPackets int
	VideoPackets int
}

type participantIdentity struct {
	DisplayName   string
	ParticipantID string
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
		cfg:               cfg,
		subscribers:       make(map[string]*subscriberPeer),
		sessionsByRemote:  make(map[string]*sessionCapture),
		identityByRemote:  make(map[string]participantIdentity),
		startedAt:         time.Now().UTC(),
		subscriberUpdates: make(chan struct{}, 1),
	}
	return r.run(ctx)
}

func (r *Recorder) run(ctx context.Context) error {
	baseURL, roomToken, err := nextcloud.ParseCallURL(r.cfg.CallURL)
	if err != nil {
		return err
	}
	r.baseURL = baseURL
	r.connectBaseURL = strings.TrimRight(strings.TrimSpace(r.cfg.ConnectBaseURL), "/")
	if r.connectBaseURL == "" {
		r.connectBaseURL = baseURL
	}
	r.roomToken = roomToken
	r.finalOutputPath = deriveFinalOutputPath(r.cfg.OutputPath, r.cfg.FinalOutputPath)

	if err := ensureOutputDir(r.finalOutputPath); err != nil {
		return err
	}
	segmentsDir, err := prepareSegmentsDir(r.finalOutputPath, r.cfg.SegmentsDir)
	if err != nil {
		return err
	}
	r.segmentsDir = segmentsDir

	sessionArtifact, err := newSessionCaptureArtifact(r.finalOutputPath, r.cfg.CallURL, r.roomToken, r.cfg.GuestName)
	if err != nil {
		return fmt.Errorf("session artifact init failed: %w", err)
	}
	r.sessionArtifact = sessionArtifact
	r.sessionPath = sessionArtifact.sessionPath
	log.Printf("session artifact capture enabled: session_id=%s path=%s", sessionArtifact.sessionID, sessionArtifact.sessionDir)

	r.ocs = nextcloud.NewOCSClient(r.connectBaseURL, r.cfg.Insecure)
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

	durationLabel := "none"
	if r.cfg.Duration > 0 {
		durationLabel = r.cfg.Duration.String()
	}
	log.Printf(
		"talk recorder running: room=%s duration_limit=%s stop_when_room_empty=%t room_empty_grace=%s final=%s segments=%s",
		r.roomToken,
		durationLabel,
		r.cfg.StopWhenRoomEmpty,
		r.cfg.RoomEmptyGrace,
		r.finalOutputPath,
		r.segmentsDir,
	)

	var durationTimer *time.Timer
	var durationCh <-chan time.Time
	if r.cfg.Duration > 0 {
		durationTimer = time.NewTimer(r.cfg.Duration)
		durationCh = durationTimer.C
	}

	var roomEmptyTimer *time.Timer
	var roomEmptyTimerCh <-chan time.Time
	roomHasSeenRemote := false
	roomEmptyTimerArmed := false
	if r.cfg.StopWhenRoomEmpty {
		r.notifySubscriberChange()
	}

	stopReason := ""

runLoop:
	for {
		select {
		case <-durationCh:
			stopReason = fmt.Sprintf("duration limit reached: %s", r.cfg.Duration)
			break runLoop
		case <-roomEmptyTimerCh:
			if r.subscriberCount() == 0 {
				stopReason = fmt.Sprintf("room empty for %s after remote participants left", r.cfg.RoomEmptyGrace)
				break runLoop
			}
			roomEmptyTimerArmed = false
			roomEmptyTimerCh = nil
		case <-r.subscriberUpdates:
			if !r.cfg.StopWhenRoomEmpty {
				continue
			}

			subscriberCount := r.subscriberCount()
			var action roomEmptyTimerAction
			roomHasSeenRemote, action = nextRoomEmptyTimerAction(roomHasSeenRemote, roomEmptyTimerArmed, subscriberCount)
			switch action {
			case roomEmptyTimerArm:
				if roomEmptyTimer == nil {
					roomEmptyTimer = time.NewTimer(r.cfg.RoomEmptyGrace)
				} else {
					stopAndDrainTimer(roomEmptyTimer)
					roomEmptyTimer.Reset(r.cfg.RoomEmptyGrace)
				}
				roomEmptyTimerArmed = true
				roomEmptyTimerCh = roomEmptyTimer.C
				log.Printf("room empty; stopping in %s unless a participant rejoins", r.cfg.RoomEmptyGrace)
			case roomEmptyTimerDisarm:
				if roomEmptyTimer != nil {
					stopAndDrainTimer(roomEmptyTimer)
				}
				roomEmptyTimerArmed = false
				roomEmptyTimerCh = nil
				log.Printf("participant activity resumed; room-empty stop canceled")
			}
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				cancel()
				if durationTimer != nil {
					stopAndDrainTimer(durationTimer)
				}
				if roomEmptyTimer != nil {
					stopAndDrainTimer(roomEmptyTimer)
				}
				_ = r.cleanup(context.Background())
				return err
			}
		case <-runCtx.Done():
			stopReason = "context canceled"
			break runLoop
		}
	}

	if durationTimer != nil {
		stopAndDrainTimer(durationTimer)
	}
	if roomEmptyTimer != nil {
		stopAndDrainTimer(roomEmptyTimer)
	}
	if stopReason != "" {
		log.Printf("talk recorder stopping: %s", stopReason)
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

	log.Printf("talk bootstrap complete: base=%s connect=%s token=%s session=%s signaling_session=%s", r.baseURL, r.connectBaseURL, r.roomToken, r.nextcloudSessionID, r.signalingSessionID)
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

	var artifactSummary *sessionCaptureSummary
	if r.sessionArtifact != nil {
		summary := r.sessionArtifact.summary()
		artifactSummary = &summary
		if err := r.sessionArtifact.close(); err != nil && firstErr == nil {
			firstErr = err
		}
		r.sessionArtifact = nil
	}

	if err := r.composeFinalOutput(); err != nil {
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

	if r.cfg.WriteReport {
		r.sessionMu.Lock()
		sessions := make([]*sessionCapture, len(r.sessionOrder))
		copy(sessions, r.sessionOrder)
		r.sessionMu.Unlock()

		reportPath := deriveReportPath(r.finalOutputPath, r.cfg.OutputPath)
		if err := r.writeReport(
			reportPath,
			sessions,
			composeOK,
			composeErrText,
			intermediateCleaned,
			artifactSummary,
		); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("write report failed: %v", err)
		} else {
			log.Printf("wrote report: %s", reportPath)
		}
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
	authParamsByVersion := make(map[string]any, len(versions))
	for _, version := range versions {
		authRaw := helloAuth[version]
		var authParams any
		if err := json.Unmarshal(authRaw, &authParams); err != nil {
			return fmt.Errorf("decode helloAuthParams[%s]: %w", version, err)
		}
		authParamsByVersion[version] = authParams
	}

	const maxHelloRounds = 3
	for round := 1; round <= maxHelloRounds; round++ {
		for _, version := range versions {
			req := map[string]any{
				"type": "hello",
				"hello": map[string]any{
					"version": version,
					"auth": map[string]any{
						"url":    backendURL,
						"params": authParamsByVersion[version],
					},
					"features": []any{"chat-relay"},
				},
			}

			resp, err := r.signaling.Request(ctx, req, 15*time.Second)
			if err != nil {
				log.Printf("hello version %s request failed (attempt %d/%d): %v", version, round, maxHelloRounds, err)
				continue
			}
			if asString(resp["type"]) != "hello" {
				log.Printf("hello version %s returned type=%s (attempt %d/%d)", version, asString(resp["type"]), round, maxHelloRounds)
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

		if round < maxHelloRounds {
			backoff := time.Duration(round) * 500 * time.Millisecond
			log.Printf("hello handshake not ready; retrying in %s (attempt %d/%d)", backoff, round+1, maxHelloRounds)
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case <-time.After(backoff):
			}
		}
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

	switch target {
	case "room":
		switch eventType {
		case "join":
			for _, item := range asSlice(roomEvent["join"]) {
				joinItem := asMap(item)
				if len(joinItem) == 0 {
					continue
				}
				remoteSessionID, displayName, participantID := parseRoomJoinIdentity(joinItem)
				if remoteSessionID == "" {
					continue
				}
				r.rememberParticipantIdentity(remoteSessionID, displayName, participantID)
				if _, err := r.ensureSubscriber(remoteSessionID); err != nil {
					return err
				}
			}
		case "leave":
			r.removeParticipantSessions(asSlice(roomEvent["leave"]))
		}
	case "participants":
		return r.handleParticipantsEvent(asMap(roomEvent["update"]))
	}

	return nil
}

func (r *Recorder) handleParticipantsEvent(update map[string]any) error {
	if len(update) == 0 {
		return nil
	}

	if asBool(update["all"]) {
		if flags, ok := asInt(update["incall"]); ok && flags == 0 {
			r.clearRemoteParticipants()
			return nil
		}
	}

	users := asSlice(update["users"])
	if len(users) == 0 {
		users = asSlice(update["changed"])
	}
	if len(users) == 0 {
		return nil
	}

	if asBool(update["all"]) {
		active := make(map[string]participantIdentity, len(users))
		for _, raw := range users {
			sessionID, identity, activeInCall := parseParticipantUpdate(asMap(raw))
			if sessionID == "" || !activeInCall {
				continue
			}
			active[sessionID] = identity
		}
		return r.syncRemoteParticipants(active)
	}

	for _, raw := range users {
		sessionID, identity, activeInCall := parseParticipantUpdate(asMap(raw))
		if sessionID == "" {
			continue
		}
		if !activeInCall {
			r.removeParticipantSessions([]any{sessionID})
			continue
		}
		r.rememberParticipantIdentity(sessionID, identity.DisplayName, identity.ParticipantID)
		peer, err := r.ensureSubscriber(sessionID)
		if err != nil {
			return err
		}
		r.retryRequestOfferForCallTransition(peer)
	}

	return nil
}

// retryRequestOfferForCallTransition forces a fresh requestoffer when a
// participants update tells us a session is now in the call. The first
// requestoffer often races spreed's call-state propagation and gets
// silently dropped at hub.go ("not in same call"); without this nudge
// the recorder waits out the 8s response timeout × max-attempts before
// retrying, which exceeds a typical short-recording window.
func (r *Recorder) retryRequestOfferForCallTransition(peer *subscriberPeer) {
	if peer == nil {
		return
	}
	if !peer.clearOfferThrottleForCallTransition() {
		return
	}
	if err := peer.requestOffer(); err != nil {
		log.Printf("requestoffer retry after call transition failed for %s: %v", peer.remoteSessionID, err)
	}
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
	if err := peer.handleMessage(data); err != nil {
		log.Printf("subscriber signaling handling failed sid=%s type=%s: %v", fromSession, msgType, err)
		return nil
	}
	return nil
}

func (r *Recorder) ensureSubscriber(remoteSessionID string) (*subscriberPeer, error) {
	if remoteSessionID == "" || remoteSessionID == r.signalingSessionID {
		return nil, nil
	}

	r.mu.Lock()
	existing := r.subscribers[remoteSessionID]
	r.mu.Unlock()
	if existing != nil {
		if existing.resetIfExhausted() {
			log.Printf("retrying requestoffer for %s (participant joined call)", remoteSessionID)
			if err := existing.requestOffer(); err != nil {
				log.Printf("requestoffer retry failed for %s: %v", remoteSessionID, err)
			}
		}
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
	r.notifySubscriberChange()

	log.Printf("subscribing to remote session %s", remoteSessionID)
	if err := peer.requestOffer(); err != nil {
		log.Printf("initial requestoffer failed for %s: %v", remoteSessionID, err)
	}

	return peer, nil
}

func (r *Recorder) removeSubscriber(remoteSessionID string) error {
	r.mu.Lock()
	_, hadPeer := r.subscribers[remoteSessionID]
	peer := r.subscribers[remoteSessionID]
	delete(r.subscribers, remoteSessionID)
	r.mu.Unlock()
	if hadPeer {
		r.notifySubscriberChange()
	}
	if peer == nil {
		return nil
	}
	log.Printf("closing subscriber for remote session %s", remoteSessionID)
	return peer.close()
}

func (r *Recorder) removeParticipantSessions(sessions []any) {
	for _, item := range sessions {
		remoteSessionID := asString(item)
		if remoteSessionID == "" {
			continue
		}
		r.forgetParticipantIdentity(remoteSessionID)
		if err := r.removeSubscriber(remoteSessionID); err != nil {
			log.Printf("remove subscriber %s failed: %v", remoteSessionID, err)
		}
	}
}

func (r *Recorder) clearRemoteParticipants() {
	r.mu.Lock()
	sessions := make([]any, 0, len(r.subscribers))
	for remoteSessionID := range r.subscribers {
		sessions = append(sessions, remoteSessionID)
	}
	r.mu.Unlock()
	r.removeParticipantSessions(sessions)
}

func (r *Recorder) syncRemoteParticipants(active map[string]participantIdentity) error {
	r.mu.Lock()
	current := make([]string, 0, len(r.subscribers))
	for remoteSessionID := range r.subscribers {
		current = append(current, remoteSessionID)
	}
	r.mu.Unlock()

	for _, remoteSessionID := range current {
		if _, ok := active[remoteSessionID]; ok {
			continue
		}
		r.removeParticipantSessions([]any{remoteSessionID})
	}

	for remoteSessionID, identity := range active {
		r.rememberParticipantIdentity(remoteSessionID, identity.DisplayName, identity.ParticipantID)
		peer, err := r.ensureSubscriber(remoteSessionID)
		if err != nil {
			return err
		}
		r.retryRequestOfferForCallTransition(peer)
	}

	return nil
}

func nextRoomEmptyTimerAction(hasSeenRemote bool, timerArmed bool, subscriberCount int) (bool, roomEmptyTimerAction) {
	if subscriberCount > 0 {
		if timerArmed {
			return true, roomEmptyTimerDisarm
		}
		return true, roomEmptyTimerNoop
	}
	if !hasSeenRemote {
		if timerArmed {
			return hasSeenRemote, roomEmptyTimerDisarm
		}
		return hasSeenRemote, roomEmptyTimerNoop
	}
	return hasSeenRemote, roomEmptyTimerArm
}

func (r *Recorder) notifySubscriberChange() {
	if r.subscriberUpdates == nil {
		return
	}
	select {
	case r.subscriberUpdates <- struct{}{}:
	default:
	}
}

func (r *Recorder) subscriberCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.subscribers)
}

func stopAndDrainTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (r *Recorder) rememberParticipantIdentity(remoteSessionID, displayName, participantID string) {
	if remoteSessionID == "" {
		return
	}

	displayName = strings.TrimSpace(displayName)
	participantID = strings.TrimSpace(participantID)

	var shouldUpdateArtifact bool
	r.sessionMu.Lock()
	current := r.identityByRemote[remoteSessionID]
	if displayName == "" {
		displayName = current.DisplayName
	}
	if participantID == "" {
		participantID = current.ParticipantID
	}
	r.identityByRemote[remoteSessionID] = participantIdentity{
		DisplayName:   displayName,
		ParticipantID: participantID,
	}

	if session := r.sessionsByRemote[remoteSessionID]; session != nil {
		if displayName != "" {
			session.ParticipantName = displayName
			shouldUpdateArtifact = true
		}
		if participantID != "" {
			session.ParticipantID = participantID
		}
	}
	r.sessionMu.Unlock()

	if shouldUpdateArtifact && r.sessionArtifact != nil {
		if err := r.sessionArtifact.updateParticipantDisplay(remoteSessionID, participantID, displayName); err != nil {
			log.Printf("update participant display failed sid=%s: %v", remoteSessionID, err)
		}
	}
}

func (r *Recorder) forgetParticipantIdentity(remoteSessionID string) {
	if remoteSessionID == "" {
		return
	}
	r.sessionMu.Lock()
	delete(r.identityByRemote, remoteSessionID)
	r.sessionMu.Unlock()
}

func parseRoomJoinIdentity(joinItem map[string]any) (string, string, string) {
	scopes := []map[string]any{
		joinItem,
		asMap(joinItem["session"]),
		asMap(joinItem["participant"]),
		asMap(joinItem["user"]),
		asMap(joinItem["actor"]),
	}

	remoteSessionID := firstNonEmpty(
		scopes,
		"sessionid",
		"sessionId",
		"roomSessionId",
		"roomsessionid",
	)
	displayName := firstNonEmpty(
		scopes,
		"displayName",
		"displayname",
		"participantName",
		"name",
		"actorDisplayName",
		"userDisplayName",
	)
	participantID := firstNonEmpty(
		scopes,
		"userid",
		"userId",
		"actorid",
		"actorId",
		"participantId",
		"participantID",
		"uid",
	)

	return remoteSessionID, displayName, participantID
}

func parseParticipantUpdate(user map[string]any) (string, participantIdentity, bool) {
	scopes := []map[string]any{
		user,
		asMap(user["session"]),
		asMap(user["participant"]),
		asMap(user["user"]),
	}

	sessionID := firstNonEmpty(
		scopes,
		"sessionid",
		"sessionId",
		"roomSessionId",
		"roomsessionid",
	)
	if sessionID == "" {
		return "", participantIdentity{}, false
	}
	if asBool(user["internal"]) {
		return sessionID, participantIdentity{}, false
	}
	if flags, ok := asInt(user["inCall"]); ok && flags == 0 {
		return sessionID, participantIdentity{}, false
	}

	return sessionID, participantIdentity{
		DisplayName: firstNonEmpty(
			scopes,
			"displayName",
			"displayname",
			"participantName",
			"name",
			"actorDisplayName",
			"userDisplayName",
		),
		ParticipantID: firstNonEmpty(
			scopes,
			"userid",
			"userId",
			"actorid",
			"actorId",
			"participantId",
			"participantID",
			"uid",
		),
	}, true
}

func firstNonEmpty(scopes []map[string]any, keys ...string) string {
	for _, scope := range scopes {
		for _, key := range keys {
			value := strings.TrimSpace(asString(scope[key]))
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func (r *Recorder) onRemoteTrack(ctx context.Context, track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver, remoteSessionID string) {
	kind := strings.ToLower(track.Kind().String())

	session, err := r.ensureSessionCapture(remoteSessionID)
	if err != nil {
		log.Printf("ensure session capture failed sid=%s: %v", remoteSessionID, err)
		return
	}

	streamCaptureID := ""
	var streamCaptureMu sync.RWMutex
	streamCaptureSSRC := uint32(track.SSRC())
	streamCapturePT := uint8(track.PayloadType())
	trackDesc := descriptorFromTrack(track)
	if r.sessionArtifact != nil {
		streamCaptureID, err = r.sessionArtifact.openStream(
			remoteSessionID,
			session.ParticipantID,
			session.ParticipantName,
			trackDesc,
			streamCaptureSSRC,
			streamCapturePT,
			time.Now(),
		)
		if err != nil {
			log.Printf("session artifact stream open failed sid=%s kind=%s: %v", remoteSessionID, kind, err)
			streamCaptureID = ""
		}
	}
	getStreamCaptureID := func() string {
		streamCaptureMu.RLock()
		defer streamCaptureMu.RUnlock()
		return streamCaptureID
	}
	setStreamCaptureID := func(value string) {
		streamCaptureMu.Lock()
		streamCaptureID = value
		streamCaptureMu.Unlock()
	}

	if r.sessionArtifact != nil && receiver != nil {
		go func() {
			for {
				packets, _, readErr := receiver.ReadRTCP()
				if readErr != nil {
					return
				}
				activeStreamID := getStreamCaptureID()
				if activeStreamID == "" || len(packets) == 0 {
					continue
				}
				if err := r.sessionArtifact.writeRTCP(activeStreamID, packets, time.Now()); err != nil {
					log.Printf("session artifact rtcp write failed sid=%s stream=%s track=%s: %v", remoteSessionID, activeStreamID, track.ID(), err)
				}
			}
		}()
	}

	log.Printf("remote track: sid=%s kind=%s track=%s stream=%s codec=%s", remoteSessionID, kind, track.ID(), track.StreamID(), track.Codec().MimeType)

	reason := "ended"
	for {
		recv := time.Now()
		pkt, _, readErr := track.ReadRTP()
		if readErr != nil {
			reason = "read-error"
			if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
				reason = "context-cancelled"
			} else if strings.Contains(strings.ToLower(readErr.Error()), "eof") {
				reason = "eof"
			}
			if !errors.Is(readErr, context.Canceled) && !strings.Contains(strings.ToLower(readErr.Error()), "eof") {
				log.Printf("capture track read failed sid=%s track=%s: %v", remoteSessionID, track.ID(), readErr)
			}
			break
		}

		activeStreamID := getStreamCaptureID()
		if activeStreamID != "" {
			if pkt.SSRC != streamCaptureSSRC || pkt.PayloadType != streamCapturePT {
				rotateReason := streamSegmentRotationReason(streamCaptureSSRC, pkt.SSRC, streamCapturePT, pkt.PayloadType)
				if err := r.sessionArtifact.closeStream(activeStreamID, rotateReason, recv); err != nil {
					log.Printf("session artifact stream rotate-close failed sid=%s stream=%s track=%s: %v", remoteSessionID, activeStreamID, track.ID(), err)
				}
				nextStreamID, openErr := r.sessionArtifact.openStream(
					remoteSessionID,
					session.ParticipantID,
					session.ParticipantName,
					trackDesc,
					pkt.SSRC,
					pkt.PayloadType,
					recv,
				)
				if openErr != nil {
					log.Printf("session artifact stream rotate-open failed sid=%s old_stream=%s track=%s: %v", remoteSessionID, activeStreamID, track.ID(), openErr)
					setStreamCaptureID("")
				} else {
					setStreamCaptureID(nextStreamID)
					streamCaptureSSRC = pkt.SSRC
					streamCapturePT = pkt.PayloadType
				}
			}
		}
		activeStreamID = getStreamCaptureID()
		if activeStreamID != "" {
			if err := r.sessionArtifact.writeRTP(activeStreamID, pkt, recv); err != nil {
				log.Printf("session artifact packet write failed sid=%s stream=%s track=%s: %v", remoteSessionID, activeStreamID, track.ID(), err)
			}
		}

		r.sessionMu.Lock()
		switch kind {
		case "audio":
			session.AudioPackets++
		case "video":
			session.VideoPackets++
		}
		r.sessionMu.Unlock()
	}

	activeStreamID := getStreamCaptureID()
	if activeStreamID != "" {
		if err := r.sessionArtifact.closeStream(activeStreamID, reason, time.Now()); err != nil {
			log.Printf("session artifact stream close failed sid=%s stream=%s: %v", remoteSessionID, activeStreamID, err)
		}
		setStreamCaptureID("")
	}

	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("capture track failed sid=%s track=%s: %v", remoteSessionID, track.ID(), err)
	}
}

func streamSegmentRotationReason(prevSSRC, nextSSRC uint32, prevPT, nextPT uint8) string {
	switch {
	case prevSSRC != nextSSRC && prevPT != nextPT:
		return fmt.Sprintf("segment-rotate:ssrc:%d->%d,pt:%d->%d", prevSSRC, nextSSRC, prevPT, nextPT)
	case prevSSRC != nextSSRC:
		return fmt.Sprintf("segment-rotate:ssrc:%d->%d", prevSSRC, nextSSRC)
	case prevPT != nextPT:
		return fmt.Sprintf("segment-rotate:pt:%d->%d", prevPT, nextPT)
	default:
		return "segment-rotate:unknown"
	}
}

func (r *Recorder) ensureSessionCapture(remoteSessionID string) (*sessionCapture, error) {
	r.sessionMu.Lock()
	defer r.sessionMu.Unlock()

	if existing := r.sessionsByRemote[remoteSessionID]; existing != nil {
		return existing, nil
	}

	index := len(r.sessionOrder) + 1
	identity := r.identityByRemote[remoteSessionID]
	participantName := strings.TrimSpace(identity.DisplayName)
	if participantName == "" {
		participantName = fmt.Sprintf("participant-%s", shortID(remoteSessionID))
	}

	session := &sessionCapture{
		RemoteSessionID: remoteSessionID,
		ParticipantName: participantName,
		ParticipantID:   firstNonEmpty([]map[string]any{{"participant_id": identity.ParticipantID}, {"remote_session_id": remoteSessionID}}, "participant_id", "remote_session_id"),
		Index:           index,
		StartedAt:       time.Now().UTC(),
	}

	r.sessionsByRemote[remoteSessionID] = session
	r.sessionOrder = append(r.sessionOrder, session)
	return session, nil
}

func (r *Recorder) composeFinalOutput() error {
	r.artifactRemux = nil
	if r.sessionPath == "" {
		return errors.New("session artifact is not available")
	}
	return r.composeFinalOutputFromSessionArtifact()
}

func (r *Recorder) composeFinalOutputFromSessionArtifact() error {
	if r.sessionPath == "" {
		return errors.New("session artifact is not available")
	}

	workDir := filepath.Join(r.segmentsDir, "artifact-remux-work")
	result, err := coreremux.BuildFromSession(
		r.sessionPath,
		r.finalOutputPath,
		coreremux.BuildOptions{
			WorkDir:      workDir,
			KeepWork:     true,
			StrictCodecs: false,
			Title:        fmt.Sprintf("Cassini Go Recording %s", r.roomToken),
		},
	)
	if err != nil {
		return err
	}

	log.Printf(
		"composed final output from session artifact: session=%s output=%s work=%s segments=%d",
		result.SessionJSONPath,
		result.OutputPath,
		result.WorkDir,
		result.Segments,
	)
	r.artifactRemux = &result
	return nil
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
	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		go r.onRemoteTrack(context.Background(), track, receiver, remoteSessionID)
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

// resetIfExhausted resets the requestoffer attempt counter when the remote
// participant has transitioned into the call. Returns true if a reset was
// performed (i.e. attempts were exhausted and no offer was received yet).
func (p *subscriberPeer) resetIfExhausted() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.offerExhaustedLogged || p.offerReceived {
		return false
	}
	p.requestOfferAttempts = 0
	p.offerExhaustedLogged = false
	p.awaitingOfferSince = time.Time{}
	return true
}

// clearOfferThrottleForCallTransition drops the in-flight throttle so the
// next requestOffer call sends immediately. Used when we learn the remote
// participant has freshly entered the call — the first requestOffer
// likely raced spreed's call-state propagation and was silently rejected
// at hub.go ("not in the same call"). Returns true if a retry should be
// attempted now. No-op when an offer was already received, when no
// attempt has been made yet, or when attempts are exhausted (let
// resetIfExhausted handle that case).
func (p *subscriberPeer) clearOfferThrottleForCallTransition() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.offerReceived || p.requestOfferAttempts == 0 || p.offerExhaustedLogged {
		return false
	}
	p.awaitingOfferSince = time.Time{}
	return true
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
	artifactSummary *sessionCaptureSummary,
) error {
	reportSessionArtifact := map[string]any{"enabled": false}
	if artifactSummary != nil {
		reportSessionArtifact = map[string]any{
			"enabled":             artifactSummary.Enabled,
			"closed":              artifactSummary.Closed,
			"session_id":          artifactSummary.SessionID,
			"session_json":        artifactSummary.SessionJSONPath,
			"events_ndjson":       artifactSummary.EventsPath,
			"streams_dir":         artifactSummary.StreamsDir,
			"stream_count":        artifactSummary.StreamCount,
			"packet_count":        artifactSummary.PacketCount,
			"active_stream_count": artifactSummary.ActiveStreamCount,
		}
	}

	finalExists, finalSize := fileState(r.finalOutputPath)
	segmentsExists := dirExists(r.segmentsDir)

	warnings := make([]string, 0, 2)
	if composeErr != "" {
		warnings = append(warnings, "final compose failed: "+composeErr)
	}

	type sessionOutput struct {
		RemoteSessionID string `json:"remote_session_id"`
		ParticipantName string `json:"participant_name"`
		ParticipantID   string `json:"participant_id"`
		Index           int    `json:"index"`
		StartedAt       string `json:"started_at"`

		AudioPackets int `json:"audio_packets"`
		VideoPackets int `json:"video_packets"`
	}

	sessionOutputs := make([]sessionOutput, 0, len(sessions))
	for _, s := range sessions {
		sessionOutputs = append(sessionOutputs, sessionOutput{
			RemoteSessionID: s.RemoteSessionID,
			ParticipantName: s.ParticipantName,
			ParticipantID:   s.ParticipantID,
			Index:           s.Index,
			StartedAt:       s.StartedAt.UTC().Format(time.RFC3339Nano),
			AudioPackets:    s.AudioPackets,
			VideoPackets:    s.VideoPackets,
		})
	}

	report := map[string]any{
		"generated_at":     rfc3339Now(),
		"started_at":       r.startedAt.UTC().Format(time.RFC3339Nano),
		"call_url":         r.cfg.CallURL,
		"base_url":         r.baseURL,
		"connect_base_url": r.connectBaseURL,
		"room_token":       r.roomToken,
		"guest_name":       r.cfg.GuestName,
		"duration_seconds": int(r.cfg.Duration / time.Second),
		"stop_when_room_empty": map[string]any{
			"enabled":       r.cfg.StopWhenRoomEmpty,
			"grace_seconds": r.cfg.RoomEmptyGrace.Seconds(),
		},
		"join_flags": r.cfg.JoinFlags,
		"turn_mode":  r.cfg.TurnMode,

		"requested_output_path": r.cfg.OutputPath,
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
		"session_artifact": reportSessionArtifact,
		"artifact_remux":   buildArtifactRemuxReport(r.artifactRemux),
		"session_outputs":  sessionOutputs,
		"warnings":         warnings,
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

func buildArtifactRemuxReport(result *coreremux.BuildResult) map[string]any {
	if result == nil {
		return map[string]any{"used": false}
	}

	totalAdjustNS, maxAbsAdjustNS, adjustedStreams := coreremux.SummarizePlanAdjustments(result.StreamPlans)

	return map[string]any{
		"used":              true,
		"session_json":      result.SessionJSONPath,
		"output_path":       result.OutputPath,
		"work_dir":          result.WorkDir,
		"segments":          result.Segments,
		"stream_plans":      result.StreamPlans,
		"adjusted_streams":  adjustedStreams,
		"total_adjust_ns":   totalAdjustNS,
		"max_abs_adjust_ns": maxAbsAdjustNS,
		"max_abs_adjust_ms": float64(maxAbsAdjustNS) / 1e6,
		"mean_adjust_ns_per_stream": func() int64 {
			if len(result.StreamPlans) == 0 {
				return 0
			}
			return totalAdjustNS / int64(len(result.StreamPlans))
		}(),
	}
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

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		i := int(t)
		return i, float64(i) == t
	case json.Number:
		i, err := t.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
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
