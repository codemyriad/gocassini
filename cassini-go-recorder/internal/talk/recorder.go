package talk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gocassini/internal/config"
	"gocassini/internal/nextcloud"
	"gocassini/internal/signaling"
	coreremux "gocassini/pkg/core/remux"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

const (
	requestOfferResponseTimeout    = 8 * time.Second
	talkRecordingSecretEnv         = "CASSINI_TALK_RECORDING_SECRET"
	talkSignalingInternalSecretEnv = "CASSINI_TALK_SIGNALING_INTERNAL_SECRET"
)

// Recovery cadence for in-call participants we have captured no media from
// (D-386). After the fast requestoffer burst (MaxRequestOfferAttempts at the
// 8s response timeout) the recorder keeps retrying at slowRequestOfferInterval
// rather than parking the peer forever. If that still yields no media after
// captureRebuildGrace, the subscriber is torn down and rebuilt with a fresh
// peer connection (covering an offer that was answered but whose media never
// flowed), up to maxCaptureRebuilds times before falling back to slow retries.
const (
	slowRequestOfferInterval = 30 * time.Second
	captureRebuildGrace      = 45 * time.Second
	maxCaptureRebuilds       = 3
)

// firstMediaGrace closes the sub-captureRebuildGrace dead zone (D-509): a peer
// whose handshake COMPLETED but whose media never flowed was reachable by no
// guard at all until it aged past the 45s captureRebuildGrace, because that
// grace is anchored at peer creation and so conflates "no offer yet" (still
// inside its requestoffer burst) with "answered, nothing arrived". A ~30s call
// therefore finalized with "no remuxable streams" while the reconcile loop
// watched. firstMediaGrace gives the answered state its own, tighter anchor
// (answeredAt), so recovery engages inside a short recording instead of after
// it. The 45s createdAt grace stays untouched as a floor.
//
// The early rebuild additionally requires that ICE is NOT connected. The answer
// is deliberately sent before gathering completes (see handleMessage), and
// pion's stunGatherTimeout is 5s PER configured server, so a slow-but-healthy
// peer can legitimately deliver first media well past 12s; rebuilding it on a
// bare timer would tear down a working transport and capture nothing — strictly
// worse than doing nothing. Gating on the transport being observably down makes
// the early branch a strict subset of what the 45s floor would rebuild anyway,
// only sooner. An ICE-connected but silent peer is left to that floor, exactly
// as today.
const firstMediaGrace = 12 * time.Second

// Bounds on retransmitting an answer whose write failed (D-509). Two orthogonal
// budgets, because the failure they cover is a signaling resume and a resume
// that SUCCEEDS is slow: defaultResumeAttempts=4 at a 1s backoff with 5s dial
// and 5s hello timeouts means a resume can take ~15-25s and still restore the
// same session (internal/signaling/client.go). A count-only budget at the 2s
// reconcile tick would expire while the wedge was curing itself.
//
//   - answerRetransmitBudget bounds wall-clock, sized to the standalone
//     signaling server's ~30s session-hold window (the same thing the resume
//     schedule is sized to) plus slack. Past it the session is gone and there
//     is nothing left to retransmit to.
//   - maxAnswerRetransmits bounds writes actually attempted against a live
//     socket. A tick that fails with signaling.ErrNotConnected is the resume
//     window doing its job, not evidence the peer is broken, so it does not
//     burn this budget.
const (
	answerRetransmitBudget = 35 * time.Second
	maxAnswerRetransmits   = 5
)

// inCallFlagInCall is bit 1 of the Talk in-call flags: the participant has
// joined the call. Remaining bits (audio=2, video=4, SIP=8) describe what
// the participant publishes, so call membership is exactly flags&1 != 0.
const inCallFlagInCall = 1

// ErrUnjoinable marks bootstrap failures where the Talk server definitively
// rejected the recorder's attempt to join the room or call (HTTP 4xx), e.g.
// a non-public conversation the guest recorder cannot see (404) or a call
// join refused because recording consent is required (400). Retrying cannot
// succeed, so callers should fail fast instead of leaving a doomed recording
// session hanging while Talk shows it as active.
var ErrUnjoinable = errors.New("talk room unjoinable")

// ErrAbnormalStop tags fatal mid-call failures (e.g. the signaling
// connection dropping) whose cleanup nevertheless finalized cleanly:
// every capture stream closed without media loss and the final output
// composed. The recording is usable, so callers may salvage it (finalize
// the run bundle as ready, recording the stop reason) instead of
// stranding an hour of valid footage over a last-minute disconnect.
var ErrAbnormalStop = errors.New("talk recorder stopped abnormally")

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
	// inCallEver remembers every remote session we ever stood a subscriber
	// up for (i.e. saw in-call). Guarded by mu, monotonic, never pruned on
	// leave: cleanup() reconciles it against the sessions that actually
	// captured audio to surface in-call participants we recorded nothing
	// for (D-386) — a participant abandoned after requestoffer exhaustion or
	// whose media never flowed leaves no stream and no capture error, so
	// without this set the loss is invisible.
	inCallEver map[string]struct{}
	// stopping is set by cleanup() under mu; once true ensureSubscriber
	// refuses to create peers (no resurrecting subscribers into the
	// drained map) and addTrackReader refuses new capture goroutines.
	stopping bool

	// trackWG counts per-track capture goroutines spawned from pion
	// callbacks (see addTrackReader) so cleanup() can join them before
	// tearing shared state down.
	trackWG sync.WaitGroup

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

type localICESignal struct {
	messageType string
	payload     map[string]any
	sid         string
}

type subscriberPeer struct {
	owner           *Recorder
	remoteSessionID string
	pc              *webrtc.PeerConnection

	// createdAt and rebuildCount are set once at construction (newSubscriberPeer
	// / rebuildSubscriber) before the peer is registered, then immutable for
	// the peer's lifetime, so reconcileSubscribers reads them without a lock.
	// createdAt anchors the no-capture grace; rebuildCount carries the rebuild
	// cap across successive rebuilds of the same remote session (D-386).
	createdAt    time.Time
	rebuildCount int

	mu sync.Mutex
	// offerReceived is set true only after the offer/answer handshake completes
	// (the answer was sent). It gates requestOfferLoop from re-requesting. It
	// used to be set the moment an offer arrived, before SetRemoteDescription /
	// CreateAnswer / answer-send could fail; a failed answer then suppressed
	// retries forever, silently dropping the participant (D-386).
	offerReceived        bool
	currentSID           string
	requestOfferAttempts int
	awaitingOfferSince   time.Time
	offerExhaustedLogged bool
	endOfCandidatesSent  bool

	// answeredAt is when the last answer was successfully sent — the anchor for
	// firstMediaGrace. It is deliberately NOT cleared by beginNegotiation: it
	// means "we have been answered since T and still have no media", which stays
	// true across a renegotiation.
	answeredAt time.Time

	// pendingAnswer holds an answer whose SDP was built (SetLocalDescription
	// succeeded) but whose signaling WRITE failed — overwhelmingly the
	// signaling client's resume window, during which Send returns
	// ErrNotConnected while the session is redialed and resumed (D-509 gap 2).
	// The MCU is still waiting for this exact answer, so the recovery is to
	// finish the send, not to renegotiate: pendingAnswerSID stamps the
	// negotiation it belongs to and a retransmit is only ever attempted while
	// that SID is still current. Cleared by markAnswerSentLocked (the send
	// landed) and by beginNegotiation (a new offer superseded it).
	pendingAnswer      map[string]any
	pendingAnswerSID   string
	pendingAnswerSince time.Time
	answerRetransmits  int

	// SetLocalDescription starts Pion's ICE gathering asynchronously. Its
	// OnICECandidate callback can therefore race the offer handler that sends
	// the SDP answer. The signaling client's write mutex makes each WebSocket
	// write safe, but whichever goroutine acquires it first wins; it does not
	// guarantee that the answer precedes candidates. Each candidate belongs to
	// the answer's negotiation and must not overtake it, so keep all local ICE
	// signaling behind this per-peer gate and drain it in callback order only
	// after the answer write succeeds (D-454).
	answerSent             bool
	localICEFlushing       bool
	pendingLocalICESignals []localICESignal
}

func Run(ctx context.Context, cfg config.Config) error {
	r := &Recorder{
		cfg:               cfg,
		subscribers:       make(map[string]*subscriberPeer),
		inCallEver:        make(map[string]struct{}),
		sessionsByRemote:  make(map[string]*sessionCapture),
		identityByRemote:  make(map[string]participantIdentity),
		startedAt:         time.Now().UTC(),
		subscriberUpdates: make(chan struct{}, 1),
	}
	return r.run(ctx)
}

func (r *Recorder) run(ctx context.Context) error {
	r.resolveProcessScopedTalkConfig()
	if err := r.resolveTalkTarget(); err != nil {
		return err
	}
	r.connectBaseURL = strings.TrimRight(strings.TrimSpace(r.cfg.ConnectBaseURL), "/")
	if r.connectBaseURL == "" {
		r.connectBaseURL = r.baseURL
	}
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
		logBootstrapFailure(err)
		_ = r.cleanup(context.Background())
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, 3)

	// loopWG joins eventLoop/requestOfferLoop so cleanup() never runs
	// while either loop still dereferences recorder state. errCh is
	// buffered, so the sends below cannot block the joins.
	var loopWG sync.WaitGroup
	loopWG.Add(2)
	go func() {
		defer loopWG.Done()
		errCh <- r.eventLoop(runCtx)
	}()
	go func() {
		defer loopWG.Done()
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
				loopWG.Wait()
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 12*time.Second)
				cleanupErr := r.cleanup(cleanupCtx)
				cleanupCancel()
				return fatalRunError(err, cleanupErr)
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
	loopWG.Wait()

	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cleanupCancel()
	if err := r.cleanup(cleanupCtx); err != nil {
		return err
	}
	return nil
}

func (r *Recorder) resolveProcessScopedTalkConfig() {
	r.cfg.TalkAuthMode = config.NormalizeTalkAuthMode(r.cfg.TalkAuthMode)
	if strings.TrimSpace(r.cfg.TalkRecordingSecret) == "" {
		r.cfg.TalkRecordingSecret = strings.TrimSpace(os.Getenv(talkRecordingSecretEnv))
	}
	if strings.TrimSpace(r.cfg.TalkSignalingInternalSecret) == "" {
		r.cfg.TalkSignalingInternalSecret = strings.TrimSpace(os.Getenv(talkSignalingInternalSecretEnv))
	}
}

func (r *Recorder) resolveTalkTarget() error {
	callURL := strings.TrimSpace(r.cfg.CallURL)
	talkBaseURL := strings.TrimRight(strings.TrimSpace(r.cfg.TalkBaseURL), "/")
	talkRoomToken := strings.TrimSpace(r.cfg.TalkRoomToken)

	if talkBaseURL != "" || talkRoomToken != "" {
		if talkBaseURL == "" || talkRoomToken == "" {
			return errors.New("talk target requires both talk base URL and room token")
		}
		if callURL != "" {
			callBaseURL, callRoomToken, err := nextcloud.ParseCallURL(callURL)
			if err != nil {
				return err
			}
			if callBaseURL != talkBaseURL || callRoomToken != talkRoomToken {
				return fmt.Errorf("call URL target %s room=%s does not match explicit talk target %s room=%s", callBaseURL, callRoomToken, talkBaseURL, talkRoomToken)
			}
		}
		r.baseURL = talkBaseURL
		r.roomToken = talkRoomToken
		return nil
	}

	if callURL == "" {
		return errors.New("call URL or explicit talk target is required")
	}
	baseURL, roomToken, err := nextcloud.ParseCallURL(callURL)
	if err != nil {
		return err
	}
	r.baseURL = baseURL
	r.roomToken = roomToken
	return nil
}

func (r *Recorder) talkAuthMode() string {
	mode := config.NormalizeTalkAuthMode(r.cfg.TalkAuthMode)
	if mode == "" {
		return config.TalkAuthModeGuestParticipant
	}
	return mode
}

func (r *Recorder) validateInternalBootstrapConfig() error {
	if strings.TrimSpace(r.cfg.TalkRecordingSecret) == "" {
		return fmt.Errorf("talk auth mode %s requires %s to be set", config.TalkAuthModeHPBInternal, talkRecordingSecretEnv)
	}
	if strings.TrimSpace(r.cfg.TalkSignalingInternalSecret) == "" {
		return fmt.Errorf("talk auth mode %s requires %s to be set", config.TalkAuthModeHPBInternal, talkSignalingInternalSecretEnv)
	}
	return nil
}

func (r *Recorder) bootstrapInternalHPB(ctx context.Context) error {
	settings, err := r.ocs.FetchRecordingSignalingSettings(ctx, r.roomToken, r.cfg.TalkRecordingSecret)
	if err != nil {
		return fmt.Errorf("recording-auth signaling settings failed: %w", err)
	}
	r.settings = settings

	wsServer := r.settings.PrimarySignalingServer()
	if wsServer == "" {
		return errors.New("signaling settings missing signaling server (standalone signaling required)")
	}

	r.signaling = signaling.NewClient(toWSURL(wsServer), r.cfg.Insecure)
	if err := r.signaling.Connect(ctx); err != nil {
		return err
	}

	if err := r.hello(ctx); err != nil {
		return err
	}
	if err := r.sendInternalInCall(); err != nil {
		return err
	}
	if err := r.joinSignalingRoom(ctx); err != nil {
		return err
	}

	log.Printf("talk bootstrap complete: auth_mode=%s base=%s connect=%s token=%s signaling_session=%s", r.talkAuthMode(), r.baseURL, r.connectBaseURL, r.roomToken, r.signalingSessionID)
	return nil
}

// fatalRunError merges a fatal run-loop error with the cleanup outcome.
// When cleanup fully succeeded — capture streams closed without media
// loss and the final output composed — the loop error is tagged with
// ErrAbnormalStop so callers can salvage the usable recording. Any
// cleanup failure means the output cannot be trusted as complete, so
// both errors surface and the run stays failed.
func fatalRunError(loopErr, cleanupErr error) error {
	if cleanupErr != nil {
		return errors.Join(loopErr, cleanupErr)
	}
	return fmt.Errorf("%w: %w", ErrAbnormalStop, loopErr)
}

// logBootstrapFailure emits the stderr markers operators and humans grep for
// when the recorder gives up before the "talk recorder running:" liveness
// line: a dedicated unjoinable marker for definitive join rejections, plus
// the "talk recorder stopping:" line the operator scrapes into the job's
// stop detail for classification.
func logBootstrapFailure(err error) {
	if errors.Is(err, ErrUnjoinable) {
		log.Printf("talk recorder unjoinable: %v", err)
	}
	log.Printf("talk recorder stopping: %v", err)
}

// wrapUnjoinable tags definitive HTTP 4xx join rejections with ErrUnjoinable
// so callers can fail fast with a distinct exit code; other errors pass
// through unchanged.
func wrapUnjoinable(err error) error {
	if nextcloud.IsClientError(err) {
		return fmt.Errorf("%w: %w", ErrUnjoinable, err)
	}
	return err
}

func (r *Recorder) bootstrap(ctx context.Context) error {
	switch r.talkAuthMode() {
	case config.TalkAuthModeGuestParticipant:
		return r.bootstrapGuestParticipant(ctx)
	case config.TalkAuthModeHPBInternal:
		if err := r.validateInternalBootstrapConfig(); err != nil {
			return err
		}
		return r.bootstrapInternalHPB(ctx)
	default:
		return fmt.Errorf("unsupported talk auth mode %q", r.cfg.TalkAuthMode)
	}
}

func (r *Recorder) bootstrapGuestParticipant(ctx context.Context) error {
	if err := r.ocs.GetRoom(ctx, r.roomToken); err != nil {
		return fmt.Errorf("room check failed: %w", wrapUnjoinable(err))
	}

	nextcloudSessionID, err := r.ocs.MarkParticipantActive(ctx, r.roomToken, r.cfg.GuestName)
	if err != nil {
		return fmt.Errorf("participants/active failed: %w", wrapUnjoinable(err))
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
		return errors.New("signaling settings missing signaling server (standalone signaling required)")
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
		// A 4xx here is definitive: the conversation is not joinable for
		// the guest recorder (404) or the join was refused (e.g. recording
		// consent enforcement, 400). Fail fast instead of lingering in a
		// signaling-room-only session that records nothing while Talk shows
		// an active recording.
		return fmt.Errorf("join call failed: %w", wrapUnjoinable(err))
	}

	log.Printf("talk bootstrap complete: auth_mode=%s base=%s connect=%s token=%s session=%s signaling_session=%s", r.talkAuthMode(), r.baseURL, r.connectBaseURL, r.roomToken, r.nextcloudSessionID, r.signalingSessionID)
	return nil
}

func (r *Recorder) cleanup(ctx context.Context) error {
	var firstErr error
	composeOK := false
	composeErrText := ""
	intermediateCleaned := false

	r.mu.Lock()
	r.stopping = true
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

	// Closing the peers unwinds their track readers (ReadRTP/ReadRTCP
	// return once the transport is gone); join them before closing the
	// session artifact so in-flight writes land and no goroutine observes
	// a half-torn-down recorder.
	r.waitForTrackReaders(ctx, 5*time.Second)

	// Note: r.signaling and r.sessionArtifact are intentionally never
	// nil-ed here. Both are assigned once before any goroutine starts;
	// stragglers (pion callbacks, readers past the bounded join above)
	// may still dereference them, and their post-close methods degrade
	// to errors instead of the nil-pointer panic a concurrent nil-write
	// would cause.
	if r.signaling != nil {
		_ = r.signaling.Send(map[string]any{"type": "bye", "bye": map[string]any{}})
		if err := r.signaling.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if r.ocs != nil && r.talkAuthMode() == config.TalkAuthModeGuestParticipant {
		if err := r.ocs.LeaveCall(ctx, r.roomToken); err != nil {
			log.Printf("leave call failed (continuing): %v", err)
		}
		if err := r.ocs.LeaveParticipantActive(ctx, r.roomToken); err != nil {
			log.Printf("leave participants/active failed (continuing): %v", err)
		}
	}

	var artifactSummary *sessionCaptureSummary
	if r.sessionArtifact != nil {
		if err := r.sessionArtifact.close(); err != nil && firstErr == nil {
			firstErr = err
		}
		// Snapshot after close(): stream finalize (flush/BuildIndex)
		// failures are recorded as capture failures during close, and
		// the report's capture_error_count/first_capture_error must
		// reflect the same media loss that fails the run.
		summary := r.sessionArtifact.summary()
		artifactSummary = &summary
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

	// Snapshot value copies under sessionMu: a track reader that outlived
	// the bounded join above could still be bumping packet counters on the
	// shared sessionCapture structs.
	r.sessionMu.Lock()
	sessions := make([]sessionCapture, 0, len(r.sessionOrder))
	for _, s := range r.sessionOrder {
		sessions = append(sessions, *s)
	}
	r.sessionMu.Unlock()

	// Reconcile in-call participants against captured audio and log a loud,
	// greppable marker for any we recorded nothing for (D-386). This runs
	// before (and regardless of) the WriteReport gate because in production
	// the external report is disabled, so the stderr marker scraped from the
	// recorder log is the only surface that makes a silently-missed
	// participant visible.
	gaps := r.detectCaptureGaps(sessions)

	if r.cfg.WriteReport {
		reportPath := deriveReportPath(r.finalOutputPath, r.cfg.OutputPath)
		if err := r.writeReport(
			reportPath,
			sessions,
			composeOK,
			composeErrText,
			intermediateCleaned,
			artifactSummary,
			gaps,
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

// addTrackReader registers a capture goroutine with the recorder so
// cleanup() can join it. Returns false once shutdown has begun; the
// caller must not spawn the goroutine then. Gating Add on r.stopping
// keeps the WaitGroup sound: the counter can only leave zero before
// cleanup() starts waiting.
func (r *Recorder) addTrackReader() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopping {
		return false
	}
	r.trackWG.Add(1)
	return true
}

// waitForTrackReaders joins the capture goroutines registered via
// addTrackReader, bounded by ctx and a fallback timeout so a stuck
// reader cannot park shutdown forever.
func (r *Recorder) waitForTrackReaders(ctx context.Context, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		r.trackWG.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-ctx.Done():
		log.Printf("track capture goroutines still running at cleanup deadline; continuing")
	case <-timer.C:
		log.Printf("track capture goroutines still running after %s; continuing cleanup", timeout)
	}
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
	if r.talkAuthMode() != config.TalkAuthModeHPBInternal {
		for _, version := range versions {
			authRaw := helloAuth[version]
			var authParams any
			if err := json.Unmarshal(authRaw, &authParams); err != nil {
				return fmt.Errorf("decode helloAuthParams[%s]: %w", version, err)
			}
			authParamsByVersion[version] = authParams
		}
	}

	const maxHelloRounds = 3
	for round := 1; round <= maxHelloRounds; round++ {
		for _, version := range versions {
			req, err := r.buildHelloRequest(version, backendURL, authParamsByVersion[version])
			if err != nil {
				return err
			}

			resp, err := r.signaling.Request(ctx, req, 15*time.Second)
			if err != nil {
				log.Printf("hello version %s request failed (attempt %d/%d): %v", version, round, maxHelloRounds, err)
				continue
			}
			if asString(resp["type"]) == "error" {
				return r.explainHelloError(resp)
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
			if r.talkAuthMode() == config.TalkAuthModeHPBInternal && !helloResponseHasFeature(resp, "mcu") {
				return errors.New("signaling server did not advertise MCU/HPB support")
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

func (r *Recorder) explainHelloError(resp map[string]any) error {
	code, message := signalingErrorCodeMessage(resp)
	switch code {
	case "invalid_client_type":
		return errors.New("internal clients are not supported by the signaling server; check that the signaling server internalsecret is configured")
	case "invalid_token", "auth_failed":
		if r.talkAuthMode() == config.TalkAuthModeHPBInternal {
			return fmt.Errorf("internal signaling auth failed: code=%s message=%s", code, message)
		}
	}
	return r.explainSignalingError("signaling hello failed", resp)
}

func (r *Recorder) explainSignalingError(prefix string, resp map[string]any) error {
	code, message := signalingErrorCodeMessage(resp)
	if code == "" && message == "" {
		return fmt.Errorf("%s: %v", prefix, resp)
	}
	if message == "" {
		return fmt.Errorf("%s: code=%s", prefix, code)
	}
	return fmt.Errorf("%s: code=%s message=%s", prefix, code, message)
}

func signalingErrorCodeMessage(resp map[string]any) (string, string) {
	errorMap := asMap(resp["error"])
	return asString(errorMap["code"]), strings.TrimSpace(asString(errorMap["message"]))
}

func helloResponseHasFeature(resp map[string]any, want string) bool {
	helloMap := asMap(resp["hello"])
	server := asMap(helloMap["server"])
	for _, feature := range asSlice(server["features"]) {
		if asString(feature) == want {
			return true
		}
	}
	return false
}

func (r *Recorder) buildHelloRequest(version, backendURL string, authParams any) (map[string]any, error) {
	auth := map[string]any{
		"url": backendURL,
	}
	if r.talkAuthMode() == config.TalkAuthModeHPBInternal {
		internalAuthParams, err := buildInternalHelloAuthParams(r.baseURL, r.cfg.TalkSignalingInternalSecret)
		if err != nil {
			return nil, err
		}
		auth["type"] = "internal"
		auth["params"] = internalAuthParams
	} else {
		auth["params"] = authParams
	}

	return map[string]any{
		"type": "hello",
		"hello": map[string]any{
			"version": version,
			"auth":    auth,
			"features": []any{
				"chat-relay",
			},
		},
	}, nil
}

func (r *Recorder) internalInCallRequest() map[string]any {
	return map[string]any{
		"type": "internal",
		"internal": map[string]any{
			"type": "incall",
			"incall": map[string]any{
				"incall": 1,
			},
		},
	}
}

func (r *Recorder) sendInternalInCall() error {
	if r.talkAuthMode() != config.TalkAuthModeHPBInternal {
		return nil
	}
	if err := r.signaling.Send(r.internalInCallRequest()); err != nil {
		return fmt.Errorf("internal incall failed: %w", err)
	}
	return nil
}

func (r *Recorder) roomJoinRequest() map[string]any {
	room := map[string]any{
		"roomid": r.roomToken,
	}
	if r.talkAuthMode() != config.TalkAuthModeHPBInternal {
		room["sessionid"] = r.nextcloudSessionID
	}
	return map[string]any{
		"type": "room",
		"room": room,
	}
}

func (r *Recorder) joinSignalingRoom(ctx context.Context) error {
	resp, err := r.signaling.Request(ctx, r.roomJoinRequest(), 15*time.Second)
	if err != nil {
		return fmt.Errorf("signaling room join failed: %w", err)
	}
	if asString(resp["type"]) == "error" {
		return r.explainSignalingError("signaling room join failed", resp)
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
			if err := r.handleSignalingData(ctx, data); err != nil {
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
		r.reconcileSubscribers(time.Now())
	}
}

// reconcileSubscribers drives recovery on every tick for in-call participants
// we have captured no media from. It reconciles desired capture (every in-call
// participant, represented by an existing subscriber per D-365) against actual
// capture, instead of giving up locally: a peer with no captured media is kept
// being requestoffer'd (slow retry, never parked), and once that has produced
// nothing the subscriber is rebuilt with a fresh peer connection (covering an
// offer that was answered but whose media never flowed). A HEALTHY peer that is
// capturing anything is left strictly alone (D-386).
//
// The loop is snapshot -> decide -> act: reconcileState() takes each peer's
// state under its own lock, reconcileActionFor decides purely from that
// snapshot (so every reconcile outcome is table-testable without an MCU, real
// ICE or wall-clock sleeps), and only then is an action performed with no lock
// held. Because deciding and acting are separate, an action must re-validate
// anything that can change under it — see retryPendingAnswer, whose whole
// correctness rests on that.
//
// Two recoveries beyond the D-386 baseline (D-509), for peers in opposite
// trouble:
//
//   - Answered but no media: the handshake completed and the transport is dead.
//     Rebuild on the answeredAt anchor (firstMediaGrace) rather than waiting out
//     the createdAt-anchored captureRebuildGrace, which a short call never
//     reaches. Only when ICE is observably not connected.
//   - Live media but an unfinished handshake: a renegotiation (camera on) whose
//     answer write failed leaves audio flowing and video permanently gated. The
//     answer SDP was already built; only the write failed, so the fix is to
//     finish the send. This runs BEFORE the captured>0 skip, which otherwise
//     makes such a peer structurally unreachable for the rest of the call.
//
// Bounded residue, deliberately not recovered here: a wedge with no
// retransmittable answer (SetRemoteDescription/CreateAnswer/SetLocalDescription
// failed) is a deterministic offer failure that a retry cannot fix, and
// rebuilding it would trade working audio for a speculative recovery; a peer
// still parked after maxCaptureRebuilds (D-500 edge 2); and a peer that is
// ICE-connected but silent, which stays with the unchanged 45s floor.
//
// now is passed in so every decision is deterministically testable.
func (r *Recorder) reconcileSubscribers(now time.Time) {
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		return
	}
	peers := make([]*subscriberPeer, 0, len(r.subscribers))
	for _, p := range r.subscribers {
		peers = append(peers, p)
	}
	r.mu.Unlock()
	if len(peers) == 0 {
		return
	}

	captured := r.capturedPacketTotals()

	for _, p := range peers {
		state := p.reconcileState()
		switch reconcileActionFor(now, state, captured[p.remoteSessionID]) {
		case actionRetryAnswer:
			p.retryPendingAnswer()
		case actionRebuild:
			r.rebuildSubscriber(p, now, rebuildReasonFor(now, state))
		case actionRequestOffer:
			if err := p.requestOffer(); err != nil {
				log.Printf("requestoffer failed for %s: %v", p.remoteSessionID, err)
			}
		case actionNone:
			// Healthy capture, or a bounded/parked peer we have deliberately
			// stopped acting on.
		}
	}
}

// reconcileAction is the single decision reconcileSubscribers makes per peer
// per tick. Making it a value rather than control flow is what lets the whole
// guard chain be exercised as a table test (D-509).
type reconcileAction int

const (
	actionNone reconcileAction = iota
	actionRequestOffer
	actionRetryAnswer
	actionRebuild
)

func (a reconcileAction) String() string {
	switch a {
	case actionRequestOffer:
		return "requestOffer"
	case actionRetryAnswer:
		return "retryAnswer"
	case actionRebuild:
		return "rebuild"
	default:
		return "none"
	}
}

// peerReconcileState is a subscriberPeer snapshot: everything the reconcile
// decision needs, read in one pass, so the decision itself is pure.
type peerReconcileState struct {
	createdAt          time.Time
	rebuildCount       int
	offerReceived      bool
	answeredAt         time.Time
	hasPendingAnswer   bool
	pendingAnswerSince time.Time
	answerRetransmits  int
	iceConnected       bool
}

// reconcileState snapshots the peer for a reconcile tick.
//
// ICEConnectionState is sampled BEFORE p.mu is taken, and never under it:
// pion invokes OnICECandidate (-> sendLocalICESignal -> p.mu) while holding its
// own internal locks, so taking p.mu first and pion's second here would invert
// that order. It is also nil-guarded because reconcile-decision tests construct
// bare &subscriberPeer{} values with no peer connection.
func (p *subscriberPeer) reconcileState() peerReconcileState {
	iceConnected := false
	if p.pc != nil {
		switch p.pc.ICEConnectionState() {
		case webrtc.ICEConnectionStateConnected, webrtc.ICEConnectionStateCompleted:
			iceConnected = true
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	return peerReconcileState{
		createdAt:          p.createdAt,
		rebuildCount:       p.rebuildCount,
		offerReceived:      p.offerReceived,
		answeredAt:         p.answeredAt,
		hasPendingAnswer:   p.pendingAnswer != nil,
		pendingAnswerSince: p.pendingAnswerSince,
		answerRetransmits:  p.answerRetransmits,
		iceConnected:       iceConnected,
	}
}

// reconcileActionFor is the whole reconcile decision, as a pure function of a
// peer snapshot. It is the existing three-guard chain in the same order, plus
// one prelude and one new branch filling the chain's silent fall-through.
func reconcileActionFor(now time.Time, s peerReconcileState, captured int) reconcileAction {
	if reconcileShouldRetryAnswer(now, s) {
		return actionRetryAnswer
	}
	if captured > 0 {
		// GUARD 1 (unchanged for healthy peers): capturing media, incl. a
		// camera-only / muted-with-video participant. Leave the working peer
		// alone. Reaching here means it has no answer outstanding.
		return actionNone
	}
	if reconcileShouldRebuild(now, s.createdAt, s.rebuildCount) {
		// GUARD 2 (unchanged): the 45s createdAt floor. Kept AHEAD of the
		// answered branch below so the new logic can only ever act earlier than
		// today, never later.
		return actionRebuild
	}
	if s.offerReceived {
		// GUARD 3's silent fall-through: the handshake completed, so asking for
		// another offer is both throttled off and pointless. This is where a
		// peer used to sit doing nothing until the 45s floor (D-509 gap 1).
		if reconcileShouldRebuildForFirstMedia(now, s) {
			return actionRebuild
		}
		return actionNone
	}
	return actionRequestOffer // GUARD 3 (unchanged)
}

// reconcileShouldRetryAnswer reports whether a peer's failed answer write
// should be retransmitted. It runs before the captured>0 skip: a peer wedged by
// a failed renegotiation answer keeps capturing negotiation 1's audio forever,
// so the skip would make it unreachable by every lever the loop has, and its
// added video would be lost for the rest of the call (D-509 gap 2).
//
// Scoped to offerReceived, i.e. to RENEGOTIATION wedges only. On a first offer
// the latch is still open, so GUARD 3 already re-requests an offer within
// requestOfferResponseTimeout (D-386) — a recovery at least as good as retrying
// an answer to a negotiation the MCU may have abandoned. Only a renegotiation
// wedge is structurally unreachable, and that is exactly the gap this closes.
func reconcileShouldRetryAnswer(now time.Time, s peerReconcileState) bool {
	if !s.offerReceived || !s.hasPendingAnswer {
		return false
	}
	if s.answerRetransmits >= maxAnswerRetransmits {
		return false
	}
	if s.pendingAnswerSince.IsZero() {
		return false
	}
	return now.Sub(s.pendingAnswerSince) < answerRetransmitBudget
}

// reconcileShouldRebuildForFirstMedia reports whether an answered peer that has
// captured nothing should be rebuilt on the firstMediaGrace anchor. Requires
// ICE to be observably not connected: see firstMediaGrace on why a bare timer
// here would be worse than no recovery at all.
func reconcileShouldRebuildForFirstMedia(now time.Time, s peerReconcileState) bool {
	if s.rebuildCount >= maxCaptureRebuilds {
		return false
	}
	if s.answeredAt.IsZero() || s.iceConnected {
		return false
	}
	return now.Sub(s.answeredAt) >= firstMediaGrace
}

// rebuildReasonFor labels a rebuild for the log, so the two anchors are
// distinguishable in a post-mortem.
func rebuildReasonFor(now time.Time, s peerReconcileState) string {
	if reconcileShouldRebuild(now, s.createdAt, s.rebuildCount) {
		return fmt.Sprintf("no media after %s", captureRebuildGrace)
	}
	return fmt.Sprintf("no media within %s of the answer, ICE not connected", firstMediaGrace)
}

// reconcileShouldRebuild reports whether a subscriber that has captured no
// media should be torn down and rebuilt: only once it has existed longer than
// captureRebuildGrace and only up to maxCaptureRebuilds times. Because a rebuilt
// peer's createdAt resets, the grace also spaces successive rebuilds.
func reconcileShouldRebuild(now, createdAt time.Time, rebuildCount int) bool {
	if rebuildCount >= maxCaptureRebuilds {
		return false
	}
	if createdAt.IsZero() {
		return false
	}
	return now.Sub(createdAt) >= captureRebuildGrace
}

// capturedPacketTotals snapshots audio+video packet counts per remote session
// under sessionMu (taken alone, never nested with mu).
func (r *Recorder) capturedPacketTotals() map[string]int {
	r.sessionMu.Lock()
	defer r.sessionMu.Unlock()
	totals := make(map[string]int, len(r.sessionsByRemote))
	for sid, s := range r.sessionsByRemote {
		totals[sid] = s.AudioPackets + s.VideoPackets
	}
	return totals
}

// rebuildSubscriber replaces a stuck subscriber (no media captured) with a
// fresh peer connection and requests a new offer. Capture counts key on the
// remote session id, so media recovered after the rebuild accrues to the same
// participant. It reuses ensureSubscriber's registration discipline: build the
// peer outside the lock, then swap only if we are not stopping and the old peer
// is still the registered one.
func (r *Recorder) rebuildSubscriber(old *subscriberPeer, now time.Time, reason string) {
	sid := old.remoteSessionID
	newPeer, err := r.newSubscriberPeer(sid)
	if err != nil {
		log.Printf("rebuild subscriber %s: create peer failed: %v", sid, err)
		return
	}
	newPeer.createdAt = now
	newPeer.rebuildCount = old.rebuildCount + 1

	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		_ = newPeer.close()
		return
	}
	if r.subscribers[sid] != old {
		// The participant left or the peer was already replaced since the
		// reconcile snapshot; don't resurrect or double-register.
		r.mu.Unlock()
		_ = newPeer.close()
		return
	}
	r.subscribers[sid] = newPeer
	r.mu.Unlock()

	_ = old.close()
	log.Printf("talk recorder: rebuilding subscriber sid=%s (%s, rebuild %d/%d)", sid, reason, newPeer.rebuildCount, maxCaptureRebuilds)
	if err := newPeer.requestOffer(); err != nil {
		log.Printf("rebuild requestoffer failed for %s: %v", sid, err)
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
			// Room join events indicate signaling-room presence only — a
			// user with the conversation window open, not necessarily in
			// the call. Per the standalone signaling API, WebRTC peer
			// connections are established from participants-update inCall
			// flags (see handleParticipantsEvent), so only remember the
			// identity here. Subscribing on join used to create dead peers
			// whose requestoffers the server rejected ("not in same call")
			// and whose presence blocked the room-empty autostop for as
			// long as anyone kept a chat window open (D-365).
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
			sessionID, identity, callState := parseParticipantUpdate(asMap(raw))
			if sessionID == "" || callState != callStateInCall {
				continue
			}
			active[sessionID] = identity
		}
		return r.syncRemoteParticipants(active)
	}

	for _, raw := range users {
		sessionID, identity, callState := parseParticipantUpdate(asMap(raw))
		if sessionID == "" {
			continue
		}
		switch callState {
		case callStateNotInCall:
			r.removeParticipantSessions([]any{sessionID})
			continue
		case callStateUnknown:
			// The entry omits inCall: it describes room presence, not
			// call membership. Neither create nor remove — a partial
			// update without the field must not tear down the live
			// subscriber of a participant still in the call.
			continue
		}
		r.rememberParticipantIdentity(sessionID, identity.DisplayName, identity.ParticipantID)
		if err := r.ensureInCallSubscriber(sessionID); err != nil {
			return err
		}
	}

	return nil
}

func (r *Recorder) handleSignalingData(ctx context.Context, data map[string]any) error {
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
	if err := peer.handleMessage(ctx, data); err != nil {
		log.Printf("subscriber signaling handling failed sid=%s type=%s: %v", fromSession, msgType, err)
		return nil
	}
	return nil
}

// ensureSubscriber returns an existing peer or registers a new peer and issues
// exactly one initial requestoffer. It deliberately does not bypass an existing
// peer's response throttle: participants updates describe current in-call
// state, not edge-triggered transitions, and another empty-SID request can make
// the MCU replace the Janus subscriber during an active negotiation (D-454).
func (r *Recorder) ensureSubscriber(remoteSessionID string) (*subscriberPeer, error) {
	if remoteSessionID == "" || remoteSessionID == r.signalingSessionID {
		return nil, nil
	}

	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		return nil, nil
	}
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
	if r.stopping {
		// cleanup() drained the map while we were building the peer;
		// registering it now would resurrect a subscriber nobody closes.
		r.mu.Unlock()
		_ = peer.close()
		return nil, nil
	}
	if current := r.subscribers[remoteSessionID]; current != nil {
		r.mu.Unlock()
		_ = peer.close()
		// The concurrent winner owns the one initial requestoffer.
		return current, nil
	}
	r.subscribers[remoteSessionID] = peer
	r.inCallEver[remoteSessionID] = struct{}{}
	r.mu.Unlock()
	r.notifySubscriberChange()

	log.Printf("subscribing to remote session %s", remoteSessionID)
	if err := peer.requestOffer(); err != nil {
		log.Printf("initial requestoffer failed for %s: %v", remoteSessionID, err)
	}

	return peer, nil
}

// ensureInCallSubscriber owns recovery actions driven by participant state.
// Keeping exhausted-peer reset here prevents a late inbound offer/candidate
// from issuing a new empty-SID request before that valid message is processed.
func (r *Recorder) ensureInCallSubscriber(remoteSessionID string) error {
	peer, err := r.ensureSubscriber(remoteSessionID)
	if err != nil || peer == nil {
		return err
	}
	if !peer.resetIfExhausted() {
		return nil
	}
	log.Printf("retrying requestoffer for %s (participant still in call)", remoteSessionID)
	if err := peer.requestOffer(); err != nil {
		log.Printf("requestoffer recovery failed for %s: %v", remoteSessionID, err)
	}
	return nil
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
		if err := r.ensureInCallSubscriber(remoteSessionID); err != nil {
			return err
		}
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

// participantCallState classifies a participants-update entry for the
// subscribe/teardown decision.
type participantCallState int

const (
	// callStateUnknown: the entry omits the inCall field, so it says
	// nothing about call membership (room presence only). Neutral —
	// neither create nor tear down a subscriber.
	callStateUnknown participantCallState = iota
	// callStateNotInCall: the entry explicitly reports the participant
	// out of the call (in-call bit clear), or is an internal session
	// that never gets a subscriber.
	callStateNotInCall
	// callStateInCall: the in-call bit is set; a subscriber should exist.
	callStateInCall
)

func parseParticipantUpdate(user map[string]any) (string, participantIdentity, participantCallState) {
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
		return "", participantIdentity{}, callStateUnknown
	}
	if asBool(user["internal"]) {
		return sessionID, participantIdentity{}, callStateNotInCall
	}
	// Per the standalone signaling API, a participant is in the call iff
	// the in-call bit of the inCall flags is set (bit 1; higher bits only
	// describe published media / SIP). Updates without that bit must not
	// produce subscribers (D-365); entries that omit inCall entirely
	// describe room presence only and are neutral — they must not tear
	// down a live subscriber either.
	flags, ok := asInt(user["inCall"])
	if !ok {
		return sessionID, participantIdentity{}, callStateUnknown
	}
	if flags&inCallFlagInCall == 0 {
		return sessionID, participantIdentity{}, callStateNotInCall
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
	}, callStateInCall
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

// sendPLI asks the sender behind remoteSessionID for a fresh keyframe on the
// given video SSRC. Used when NACK recovery evidently failed (see pli.go) and
// once at capture start so the recording never opens mid-GOP.
func (r *Recorder) sendPLI(remoteSessionID string, ssrc uint32) {
	r.mu.Lock()
	peer := r.subscribers[remoteSessionID]
	r.mu.Unlock()
	if peer == nil || peer.pc == nil {
		return
	}
	if err := peer.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}}); err != nil {
		log.Printf("send PLI failed sid=%s ssrc=%d: %v", remoteSessionID, ssrc, err)
	}
}

func (r *Recorder) onRemoteTrack(ctx context.Context, track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver, remoteSessionID string) {
	kind := strings.ToLower(track.Kind().String())

	session, err := r.ensureSessionCapture(remoteSessionID)
	if err != nil {
		log.Printf("ensure session capture failed sid=%s: %v", remoteSessionID, err)
		return
	}

	var gapTracker *pliGapTracker
	if kind == "video" {
		gapTracker = newPLIGapTracker()
		// Ask for a keyframe up front: the sender is mid-GOP from our
		// point of view, and its next voluntary keyframe can be ~100s out.
		r.sendPLI(remoteSessionID, uint32(track.SSRC()))
	}

	streamCaptureID := ""
	var streamCaptureMu sync.RWMutex
	streamCaptureSSRC := uint32(track.SSRC())
	streamCapturePT := uint8(track.PayloadType())
	trackDesc := descriptorFromTrack(track)
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
	// openCaptureStream (re)opens an rtplog stream for the given SSRC/PT.
	// Open failures used to permanently disable capture for the track;
	// callers now retry on later packets (throttled below) so a transient
	// failure costs a slice of one track, not the rest of the meeting.
	// Returns false without logging once the artifact is closed (shutdown).
	openCaptureStream := func(ssrc uint32, pt uint8, at time.Time) bool {
		participantID, participantName := r.sessionIdentity(remoteSessionID)
		id, openErr := r.sessionArtifact.openStream(
			remoteSessionID,
			participantID,
			participantName,
			trackDesc,
			ssrc,
			pt,
			at,
		)
		if openErr != nil {
			if !errors.Is(openErr, errSessionArtifactClosed) {
				log.Printf("session artifact stream open failed sid=%s kind=%s track=%s: %v", remoteSessionID, kind, track.ID(), openErr)
			}
			return false
		}
		setStreamCaptureID(id)
		streamCaptureSSRC = ssrc
		streamCapturePT = pt
		return true
	}
	if r.sessionArtifact != nil {
		openCaptureStream(streamCaptureSSRC, streamCapturePT, time.Now())
	}

	if r.sessionArtifact != nil && receiver != nil && r.addTrackReader() {
		go func() {
			defer r.trackWG.Done()
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
	var nextStreamOpenAttempt time.Time
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

		if r.sessionArtifact != nil {
			activeStreamID := getStreamCaptureID()
			if activeStreamID != "" && (pkt.SSRC != streamCaptureSSRC || pkt.PayloadType != streamCapturePT) {
				rotateReason := streamSegmentRotationReason(streamCaptureSSRC, pkt.SSRC, streamCapturePT, pkt.PayloadType)
				if err := r.sessionArtifact.closeStream(activeStreamID, rotateReason, recv); err != nil {
					log.Printf("session artifact stream rotate-close failed sid=%s stream=%s track=%s: %v", remoteSessionID, activeStreamID, track.ID(), err)
				}
				setStreamCaptureID("")
				activeStreamID = ""
			}
			if activeStreamID == "" && recv.After(nextStreamOpenAttempt) {
				// (Re)open capture for this packet's SSRC/PT. This covers
				// SSRC/PT rotation as well as recovery from earlier open
				// failures; retries are throttled so a persistently failing
				// disk is not hammered at packet rate.
				if openCaptureStream(pkt.SSRC, pkt.PayloadType, recv) {
					activeStreamID = getStreamCaptureID()
				} else {
					nextStreamOpenAttempt = recv.Add(time.Second)
				}
			}
			if activeStreamID != "" {
				if err := r.sessionArtifact.writeRTP(activeStreamID, pkt, recv); err != nil {
					log.Printf("session artifact packet write failed sid=%s stream=%s track=%s: %v", remoteSessionID, activeStreamID, track.ID(), err)
				}
			}
		}

		if gapTracker != nil && gapTracker.observe(pkt.SSRC, pkt.SequenceNumber, recv) {
			log.Printf("video gap unhealed past NACK grace; sending PLI sid=%s track=%s ssrc=%d", remoteSessionID, track.ID(), pkt.SSRC)
			r.sendPLI(remoteSessionID, pkt.SSRC)
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

// sessionIdentity returns a locked snapshot of the participant identity
// for a capture session. Track readers must not read sessionCapture
// name/ID fields directly: rememberParticipantIdentity rewrites them
// under sessionMu when a display name arrives late.
func (r *Recorder) sessionIdentity(remoteSessionID string) (participantID, participantName string) {
	r.sessionMu.Lock()
	defer r.sessionMu.Unlock()
	session := r.sessionsByRemote[remoteSessionID]
	if session == nil {
		return "", ""
	}
	return session.ParticipantID, session.ParticipantName
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
		"composed final output from session artifact: session=%s output=%s work=%s segments=%d skipped=%d",
		result.SessionJSONPath,
		result.OutputPath,
		result.WorkDir,
		result.Segments,
		len(result.SkippedStreams),
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
		createdAt:       time.Now(),
	}

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("subscriber %s ICE state=%s", remoteSessionID, state.String())
	})
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			peer.sendEndOfCandidates("ice-gathering-complete")
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
		peer.sendLocalICESignal("candidate", map[string]any{"candidate": candidatePayload})
	})
	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if !r.addTrackReader() {
			return
		}
		go func() {
			defer r.trackWG.Done()
			r.onRemoteTrack(context.Background(), track, receiver, remoteSessionID)
		}()
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
	maxAttempts := p.owner.cfg.MaxRequestOfferAttempts
	window := requestOfferRetryWindow(p.requestOfferAttempts, maxAttempts)
	if !p.awaitingOfferSince.IsZero() && now.Sub(p.awaitingOfferSince) < window {
		p.mu.Unlock()
		return nil
	}

	// Past the fast burst we slow down but never stop: this subscriber exists
	// because the participant is in the call (D-365), so we keep asking for an
	// offer rather than abandoning them after MaxRequestOfferAttempts (D-386).
	if maxAttempts > 0 && p.requestOfferAttempts >= maxAttempts && !p.offerExhaustedLogged {
		log.Printf("subscriber %s received no offer after %d requestoffer attempts; slowing retries to %s", p.remoteSessionID, p.requestOfferAttempts, slowRequestOfferInterval)
		p.offerExhaustedLogged = true
	}

	p.requestOfferAttempts++
	p.awaitingOfferSince = now
	p.mu.Unlock()

	return p.owner.sendRequestOffer(p.remoteSessionID)
}

// requestOfferRetryWindow is the wait before the next requestoffer: the fast
// response timeout during the initial burst (first maxAttempts), then the slow
// steady interval, so an in-call participant whose handshake keeps racing is
// retried indefinitely instead of parked (D-386). maxAttempts <= 0 disables the
// burst cap and always uses the fast window.
func requestOfferRetryWindow(attempts, maxAttempts int) time.Duration {
	if maxAttempts > 0 && attempts >= maxAttempts {
		return slowRequestOfferInterval
	}
	return requestOfferResponseTimeout
}

func (p *subscriberPeer) hasOffer() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.offerReceived
}

// resetIfExhausted resets the requestoffer attempt counter when participant
// reconciliation confirms the remote is still in the call. Returns true if a
// reset was performed (attempts were exhausted and no offer was received yet).
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

func (p *subscriberPeer) sendLocalICESignal(messageType string, payload map[string]any) {
	p.mu.Lock()
	startFlush := p.enqueueLocalICESignalLocked(messageType, payload)
	p.mu.Unlock()

	if startFlush {
		p.flushLocalICESignals()
	}
}

// enqueueLocalICESignalLocked appends one signal with the negotiation SID that
// was current at callback time. p.mu must be held by the caller.
func (p *subscriberPeer) enqueueLocalICESignalLocked(messageType string, payload map[string]any) bool {
	p.pendingLocalICESignals = append(p.pendingLocalICESignals, localICESignal{
		messageType: messageType,
		payload:     payload,
		sid:         p.currentSID,
	})
	startFlush := p.answerSent && !p.localICEFlushing
	if startFlush {
		p.localICEFlushing = true
	}
	return startFlush
}

// flushLocalICESignals is the sole sender for this peer's trickled ICE
// messages. Keeping one drainer active preserves candidate order and, in
// particular, prevents endOfCandidates from overtaking a candidate queued by
// an adjacent callback. It deliberately does not hold p.mu during a network
// write, so other callbacks and shutdown can keep using the peer state while a
// signaling write is in progress.
func (p *subscriberPeer) flushLocalICESignals() {
	for {
		p.mu.Lock()
		if !p.answerSent || len(p.pendingLocalICESignals) == 0 {
			if len(p.pendingLocalICESignals) == 0 {
				p.pendingLocalICESignals = nil
			}
			p.localICEFlushing = false
			p.mu.Unlock()
			return
		}
		signal := p.pendingLocalICESignals[0]
		p.pendingLocalICESignals = p.pendingLocalICESignals[1:]
		p.mu.Unlock()

		if err := p.owner.sendPeerMessage(p.remoteSessionID, signal.messageType, signal.payload, signal.sid); err != nil {
			log.Printf("send %s failed remote=%s sid=%s: %v", signal.messageType, p.remoteSessionID, signal.sid, err)
		}
	}
}

func (p *subscriberPeer) sendEndOfCandidates(reason string) {
	p.mu.Lock()
	if p.endOfCandidatesSent {
		p.mu.Unlock()
		return
	}
	p.endOfCandidatesSent = true
	startFlush := p.enqueueLocalICESignalLocked("endOfCandidates", map[string]any{})
	p.mu.Unlock()

	if p.owner.cfg.DebugSignaling {
		log.Printf("subscriber %s -> send endOfCandidates (%s)", p.remoteSessionID, reason)
	}
	if startFlush {
		p.flushLocalICESignals()
	}
}

// beginNegotiation re-arms the per-negotiation signaling state when an offer
// arrives: the response throttle (so the retry loop stays quiet while the
// answer is built and sent), the negotiation SID, and the local ICE gate. It is
// the single point at which state belonging to a superseded negotiation is
// invalidated. Buffered ICE signals are dropped — they were stamped with the
// old SID and must not ride out around the new answer — and so is any pending
// answer: the MCU has moved on and stopped waiting for it, so retransmitting it
// could only put a stale answer on the wire (D-509).
//
// It deliberately does NOT set offerReceived: only a successfully sent answer
// stops offer retries (D-386, see markAnswerSent). Note this is a one-way
// latch, so on a RENEGOTIATION offerReceived is already true on entry and the
// retries it gates were stopped by the previous negotiation, not by this one —
// the pendingAnswer retransmit is what covers a failed answer from here on
// (D-509 gap 2).
//
// It also does NOT clear answeredAt, which means "last successful answer at":
// a peer mid-renegotiation has still been answered since then, and still has
// whatever media that answer produced (or has not).
func (p *subscriberPeer) beginNegotiation(sid string) {
	p.mu.Lock()
	p.awaitingOfferSince = time.Now()
	p.currentSID = sid
	p.endOfCandidatesSent = false
	p.answerSent = false
	p.pendingLocalICESignals = nil
	p.pendingAnswer = nil
	p.pendingAnswerSID = ""
	p.pendingAnswerSince = time.Time{}
	p.answerRetransmits = 0
	p.mu.Unlock()
}

// recordPendingAnswer stores an answer whose signaling write failed, so the
// reconcile loop can finish the send. Storing is conditional on sid still being
// the current negotiation: an offer handler can be superseded by a newer offer
// while its write is in flight, and installing a wedge for a negotiation the
// MCU has already abandoned would leave a retransmit chasing a dead SID.
func (p *subscriberPeer) recordPendingAnswer(payload map[string]any, sid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if sid == "" || sid != p.currentSID {
		return
	}
	p.pendingAnswer = payload
	p.pendingAnswerSID = sid
	p.pendingAnswerSince = time.Now()
	p.answerRetransmits = 0
}

// retryPendingAnswer re-sends an answer whose write failed, completing the
// negotiation the MCU is still waiting on. It is the recovery for D-509 gap 2:
// the answer SDP was already built (SetLocalDescription succeeded) and only the
// write failed, so the peer needs the send finished, not a rebuild (which would
// drop its live audio) and not a fresh offer (an empty-SID requestoffer against
// a peer with a live Janus subscriber is the D-454 ALREADY_JOINED mechanism
// that PR #127 removed).
//
// It is compare-and-act under p.mu, and that is not incidental. reconcile is
// snapshot -> decide -> act, so a new offer can land between the two: it would
// clear this pendingAnswer and rotate currentSID, and a naive retransmit would
// then put the OLD sid's answer on the wire and call markAnswerSent — opening
// the gate and flushing the NEW negotiation's buffered candidates around an
// answer that was never sent for it. That is exactly the D-454 harm, re-entered
// through a new door. So the SID is re-read under the lock before sending, and
// re-checked after the write with the gate-open in the same critical section;
// if it moved either time, this retransmit belongs to a dead negotiation and
// touches nothing.
func (p *subscriberPeer) retryPendingAnswer() {
	payload, sid, ok := p.takePendingAnswerForRetry()
	if !ok {
		return
	}
	p.completeAnswerRetransmit(sid, p.owner.sendPeerMessage(p.remoteSessionID, "answer", payload, sid))
}

// takePendingAnswerForRetry returns the pending answer to retransmit, but only
// while it still belongs to the CURRENT negotiation: reconcile decided to retry
// from a snapshot, and a new offer may have superseded this answer since.
func (p *subscriberPeer) takePendingAnswerForRetry() (map[string]any, string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pendingAnswer == nil || p.pendingAnswerSID == "" || p.pendingAnswerSID != p.currentSID {
		return nil, "", false
	}
	return p.pendingAnswer, p.pendingAnswerSID, true
}

// completeAnswerRetransmit applies the outcome of a retransmit write. It is
// split from the write itself because the write happens with p.mu released (a
// network write must not hold the peer lock), which is exactly the window in
// which a new offer can supersede this negotiation: beginNegotiation would drop
// the pending answer, rotate currentSID and shut the gate again. If this then
// called markAnswerSent unconditionally it would re-open the gate and flush the
// NEW negotiation's buffered candidates around an answer that was never sent
// for it — the D-454 failure (#127) from a new site. So the SID is re-checked
// under the lock, and the check and markAnswerSentLocked share one critical
// section: the gate can only ever open for the negotiation we just answered.
func (p *subscriberPeer) completeAnswerRetransmit(sid string, err error) {
	p.mu.Lock()
	if p.pendingAnswer == nil || p.pendingAnswerSID != sid || p.currentSID != sid {
		// Superseded while the write was in flight. Whatever we just wrote is
		// stale; the new negotiation owns the gate and must not be opened by us.
		p.mu.Unlock()
		return
	}
	if err != nil {
		// A resume window is the expected cause of the wedge, not evidence the
		// peer is broken, so it must not burn the write budget: a resume that
		// SUCCEEDS can take ~15-25s (see answerRetransmitBudget). The wall-clock
		// budget still bounds this.
		if !errors.Is(err, signaling.ErrNotConnected) {
			p.answerRetransmits++
		}
		retransmits := p.answerRetransmits
		p.mu.Unlock()
		log.Printf("retry answer failed remote=%s sid=%s (attempt %d/%d): %v", p.remoteSessionID, sid, retransmits, maxAnswerRetransmits, err)
		return
	}
	startFlush := p.markAnswerSentLocked()
	p.mu.Unlock()

	log.Printf("talk recorder: retransmitted wedged answer remote=%s sid=%s; ICE gate reopened", p.remoteSessionID, sid)
	if startFlush {
		p.flushLocalICESignals()
	}
}

// markAnswerSent opens the local ICE gate only after sendPeerMessage(answer)
// returned successfully. It also completes the requestoffer state transition:
// a failed answer write must leave retries enabled and candidates buffered.
func (p *subscriberPeer) markAnswerSent() {
	p.mu.Lock()
	startFlush := p.markAnswerSentLocked()
	p.mu.Unlock()

	if startFlush {
		p.flushLocalICESignals()
	}
}

// markAnswerSentLocked is markAnswerSent's body; p.mu must be held. It exists
// so retryPendingAnswer can compare the negotiation SID and open the gate in
// ONE critical section: opening the gate for a negotiation whose answer we did
// not just send is precisely the D-454 failure (buffered candidates flushing
// around an answer the remote never received), and a retransmit races every
// inbound offer by construction.
//
// It clears the pending answer because the answer has now landed, and stamps
// answeredAt as the anchor for firstMediaGrace. Returns whether the caller must
// drive flushLocalICESignals (which must not run under p.mu).
func (p *subscriberPeer) markAnswerSentLocked() bool {
	p.answerSent = true
	p.offerReceived = true
	p.answeredAt = time.Now()
	p.awaitingOfferSince = time.Time{}
	p.pendingAnswer = nil
	p.pendingAnswerSID = ""
	p.pendingAnswerSince = time.Time{}
	p.answerRetransmits = 0
	startFlush := len(p.pendingLocalICESignals) != 0 && !p.localICEFlushing
	if startFlush {
		p.localICEFlushing = true
	}
	return startFlush
}

func (p *subscriberPeer) handleMessage(ctx context.Context, data map[string]any) error {
	msgType := asString(data["type"])
	payload := asMap(data["payload"])

	switch msgType {
	case "offer":
		sdp := asString(payload["sdp"])
		if sdp == "" {
			return nil
		}

		sid := asString(data["sid"])
		p.beginNegotiation(sid)

		remoteDesc := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}
		if err := p.pc.SetRemoteDescription(remoteDesc); err != nil {
			return fmt.Errorf("set remote offer for %s: %w", p.remoteSessionID, err)
		}

		answer, err := p.pc.CreateAnswer(nil)
		if err != nil {
			return fmt.Errorf("create answer for %s: %w", p.remoteSessionID, err)
		}
		if err := p.pc.SetLocalDescription(answer); err != nil {
			return fmt.Errorf("set local answer for %s: %w", p.remoteSessionID, err)
		}

		// Send the answer immediately (trickle ICE); never park it behind ICE
		// gathering. The initial local description already carries the
		// ufrag/pwd/fingerprint the remote needs to start connectivity checks;
		// candidates follow on their own via OnICECandidate ("candidate"
		// messages), and the truthful endOfCandidates is emitted only when
		// gathering actually finishes (OnICECandidate(nil)). Those callbacks
		// are buffered until the answer send below succeeds: Pion may invoke
		// them concurrently with this handler, and candidate/EOC messages must
		// not overtake the matching answer on the MCU subscriber queue.
		//
		// #106 (waiting on GatheringCompletePromise) fixed a premature
		// endOfCandidates but introduced a worse head-of-line stall: that
		// promise resolves only after EVERY configured STUN/TURN server has
		// been gathered or timed out, so a single slow/unreachable server (the
		// marginal TURN/ICE path in D-454) delayed the answer by the whole
		// gather timeout (pion's stunGatherTimeout, 5s per server). The remote
		// then reached ICE `connected` ~24s late — or never, torn down
		// `checking -> closed` — missed the call window, and finalize hit "no
		// remuxable streams" / an empty transcript. Sending the answer up front
		// removes the stall while keeping endOfCandidates honest, so it can no
		// longer poison the remote's candidate set (the #106 concern).
		local := p.pc.LocalDescription()
		if local == nil {
			return fmt.Errorf("local description missing for %s", p.remoteSessionID)
		}

		answerPayload := map[string]any{"type": local.Type.String(), "sdp": local.SDP}
		if err := p.owner.sendPeerMessage(p.remoteSessionID, "answer", answerPayload, sid); err != nil {
			// The SDP is built and the MCU is waiting for exactly this answer;
			// only the write failed (typically the signaling client's resume
			// window, during which Send returns ErrNotConnected while the same
			// session is redialed and resumed). Record it so reconcile can
			// finish the send. Without this, a renegotiation wedge is
			// unreachable forever once the peer is capturing anything, because
			// the captured>0 skip runs before every other guard (D-509 gap 2).
			p.recordPendingAnswer(answerPayload, sid)
			return fmt.Errorf("send answer for %s: %w", p.remoteSessionID, err)
		}

		// Only now, with the answer actually sent, stop re-requesting offers
		// for this peer. Setting offerReceived on offer arrival let a failed
		// answer path permanently suppress retries (silent loss, D-386).
		p.markAnswerSent()
		return nil

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

// lowAudioPacketFloor is the captured-audio packet count below which an
// in-call participant that captured no video is flagged with a soft
// "very low audio" advisory. ~50 packets is ~1s at the default 20ms Opus
// ptime: high enough that anyone who said more than a word clears it, low
// enough to catch a participant we captured only a sliver of. Advisory
// only — it never fails the run.
const lowAudioPacketFloor = 50

// captureGap describes an in-call participant the recorder captured nothing
// (or almost nothing) for. Reason is "no-media" (no audio and no video, or
// no captured track at all) or "low-audio" (a sub-floor sliver of audio and
// no video). See detectCaptureGaps and D-386.
type captureGap struct {
	RemoteSessionID string
	ParticipantName string
	ParticipantID   string
	AudioPackets    int
	VideoPackets    int
	CapturedSession bool
	Reason          string
}

// warning renders the human-readable line used both for the run report's
// warnings array and the stderr marker. The "in-call participant ..."
// prefixes are stable so log scraping can key on them.
func (g captureGap) warning() string {
	switch g.Reason {
	case "no-media":
		if !g.CapturedSession {
			return fmt.Sprintf("in-call participant captured no media at all: name=%s sid=%s", g.ParticipantName, g.RemoteSessionID)
		}
		return fmt.Sprintf("in-call participant captured no audio or video: name=%s sid=%s", g.ParticipantName, g.RemoteSessionID)
	case "low-audio":
		return fmt.Sprintf("in-call participant captured very low audio: name=%s sid=%s (audio_packets=%d)", g.ParticipantName, g.RemoteSessionID, g.AudioPackets)
	default:
		return fmt.Sprintf("in-call participant capture gap (%s): name=%s sid=%s", g.Reason, g.ParticipantName, g.RemoteSessionID)
	}
}

func captureGapOutputs(gaps []captureGap) []map[string]any {
	out := make([]map[string]any, 0, len(gaps))
	for _, g := range gaps {
		out = append(out, map[string]any{
			"remote_session_id": g.RemoteSessionID,
			"participant_name":  g.ParticipantName,
			"participant_id":    g.ParticipantID,
			"audio_packets":     g.AudioPackets,
			"video_packets":     g.VideoPackets,
			"captured_session":  g.CapturedSession,
			"reason":            g.Reason,
		})
	}
	return out
}

// detectCaptureGaps reconciles every remote session we ever stood a
// subscriber up for (inCallEver) against the per-session captured packet
// counts, returning the in-call participants we recorded nothing — or
// almost nothing — for, and logging a loud, greppable stderr marker per
// gap. It runs regardless of the WriteReport gate: in production the
// external report is disabled, so the marker is the only surface that makes
// a silently-missed participant visible (D-386).
//
// sessions is the value-copy snapshot cleanup() already took under
// sessionMu, so this never races a straggling track reader bumping counts.
//
// Flagged:
//   - in-call but never captured a track (no session row): "no-media",
//     captured=false — the severe case (requestoffer exhaustion, or an
//     offer whose media never flowed).
//   - captured but 0 audio AND 0 video: "no-media", captured=true.
//   - captured a sub-floor sliver of audio with 0 video: "low-audio".
//
// Deliberately NOT flagged: 0 audio with >0 video — a muted / camera-only
// participant is legitimately in the recording, just silent. Only a total
// media gap (or a video-less sliver) is a gap, so the signal does not cry
// wolf over people who were recorded but did not speak.
func (r *Recorder) detectCaptureGaps(sessions []sessionCapture) []captureGap {
	r.mu.Lock()
	inCall := make([]string, 0, len(r.inCallEver))
	for sid := range r.inCallEver {
		inCall = append(inCall, sid)
	}
	r.mu.Unlock()
	if len(inCall) == 0 {
		return nil
	}
	sort.Strings(inCall)

	byRemote := make(map[string]sessionCapture, len(sessions))
	for _, s := range sessions {
		byRemote[s.RemoteSessionID] = s
	}

	gaps := make([]captureGap, 0)
	for _, sid := range inCall {
		s, captured := byRemote[sid]
		switch {
		case !captured:
			gaps = append(gaps, captureGap{
				RemoteSessionID: sid,
				ParticipantName: r.captureGapName(sid),
				Reason:          "no-media",
			})
		case s.AudioPackets == 0 && s.VideoPackets == 0:
			gaps = append(gaps, captureGap{
				RemoteSessionID: sid,
				ParticipantName: s.ParticipantName,
				ParticipantID:   s.ParticipantID,
				CapturedSession: true,
				Reason:          "no-media",
			})
		case s.AudioPackets == 0:
			// audio==0 with video>0: muted / camera-only, legitimately recorded.
			continue
		case s.AudioPackets < lowAudioPacketFloor && s.VideoPackets == 0:
			gaps = append(gaps, captureGap{
				RemoteSessionID: sid,
				ParticipantName: s.ParticipantName,
				ParticipantID:   s.ParticipantID,
				AudioPackets:    s.AudioPackets,
				VideoPackets:    s.VideoPackets,
				CapturedSession: true,
				Reason:          "low-audio",
			})
		}
	}

	for _, g := range gaps {
		log.Printf("talk recorder: %s", g.warning())
	}
	return gaps
}

// captureGapName resolves a display name for an in-call session that never
// reached ensureSessionCapture (so there is no sessionCapture to read the
// name from), falling back to the synthetic name the capture path uses.
func (r *Recorder) captureGapName(remoteSessionID string) string {
	r.sessionMu.Lock()
	identity := r.identityByRemote[remoteSessionID]
	r.sessionMu.Unlock()
	if name := strings.TrimSpace(identity.DisplayName); name != "" {
		return name
	}
	return fmt.Sprintf("participant-%s", shortID(remoteSessionID))
}

func (r *Recorder) writeReport(
	reportPath string,
	sessions []sessionCapture,
	composeOK bool,
	composeErr string,
	intermediateCleaned bool,
	artifactSummary *sessionCaptureSummary,
	gaps []captureGap,
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
			"capture_error_count": artifactSummary.CaptureErrorCount,
			"first_capture_error": artifactSummary.FirstCaptureError,
		}
	}

	finalExists, finalSize := fileState(r.finalOutputPath)
	segmentsExists := dirExists(r.segmentsDir)

	warnings := make([]string, 0, 2)
	if composeErr != "" {
		warnings = append(warnings, "final compose failed: "+composeErr)
	}
	if artifactSummary != nil && artifactSummary.CaptureErrorCount > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"capture lost media: %d error(s); first: %s",
			artifactSummary.CaptureErrorCount,
			artifactSummary.FirstCaptureError,
		))
	}
	if r.artifactRemux != nil {
		for _, skipped := range r.artifactRemux.SkippedStreams {
			warnings = append(warnings, fmt.Sprintf("stream %s skipped during final compose: %s", skipped.StreamID, skipped.Reason))
		}
	}
	for _, g := range gaps {
		warnings = append(warnings, g.warning())
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
		"talk_auth_mode":   r.talkAuthMode(),
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
		"session_artifact":  reportSessionArtifact,
		"artifact_remux":    buildArtifactRemuxReport(r.artifactRemux),
		"session_outputs":   sessionOutputs,
		"capture_gaps":      captureGapOutputs(gaps),
		"capture_gap_count": len(gaps),
		"warnings":          warnings,
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
		"skipped_streams":   result.SkippedStreams,
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
