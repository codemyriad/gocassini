package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/ivfreader"
)

const (
	defaultRotateSeconds  = 5.0
	defaultRoomEmptyGrace = 8 * time.Second
	joinFlagsAudioVideo   = 1 | 2 | 4
	joinFlagsVideoOnly    = 1 | 4
	videoFrameDuration    = time.Second / 30
	audioFrameDuration    = 20 * time.Millisecond
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type floatList []float64

func (f *floatList) String() string {
	parts := make([]string, len(*f))
	for i, v := range *f {
		parts[i] = fmt.Sprintf("%.3f", v)
	}
	return strings.Join(parts, ",")
}

func (f *floatList) Set(value string) error {
	v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return fmt.Errorf("parse float %q: %w", value, err)
	}
	*f = append(*f, v)
	return nil
}

type ocsEnvelope struct {
	OCS struct {
		Meta struct {
			Status     string `json:"status"`
			StatusCode int    `json:"statuscode"`
			Message    string `json:"message"`
		} `json:"meta"`
		Data json.RawMessage `json:"data"`
	} `json:"ocs"`
}

type settingICEServer struct {
	URLs       any    `json:"urls"`
	Username   string `json:"username"`
	Credential string `json:"credential"`
}

type signalingSettings struct {
	Server          string                     `json:"server"`
	Signaling       []map[string]any           `json:"signaling"`
	HelloAuthParams map[string]json.RawMessage `json:"helloAuthParams"`
	StunServers     []settingICEServer         `json:"stunservers"`
	TurnServers     []settingICEServer         `json:"turnservers"`
}

func (s *signalingSettings) primaryServer() string {
	if s.Server != "" {
		return s.Server
	}
	for _, entry := range s.Signaling {
		if server := asString(entry["server"]); server != "" {
			return server
		}
	}
	return ""
}

type ocsClient struct {
	baseURL  string
	username string
	password string
	http     *http.Client
}

func newOCSClient(baseURL string, insecure bool, username string, password string) *ocsClient {
	jar, _ := cookiejar.New(nil)

	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		baseTransport = &http.Transport{}
	}
	transport := baseTransport.Clone()
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	return &ocsClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: strings.TrimSpace(username),
		password: password,
		http: &http.Client{
			Timeout:   20 * time.Second,
			Transport: transport,
			Jar:       jar,
		},
	}
}

func (c *ocsClient) Close() {
	if c.http != nil {
		c.http.CloseIdleConnections()
	}
}

func (c *ocsClient) getRoom(ctx context.Context, roomToken string) error {
	_, err := c.request(ctx, http.MethodGet, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/room/%s", roomToken), nil, nil)
	return err
}

func (c *ocsClient) markParticipantActive(ctx context.Context, roomToken, displayName string, force bool) (string, error) {
	payload := url.Values{}
	payload.Set("force", strconv.FormatBool(force))
	if displayName != "" {
		payload.Set("displayName", displayName)
	}

	raw, err := c.request(ctx, http.MethodPost, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/room/%s/participants/active", roomToken), nil, payload)
	if err != nil {
		return "", err
	}

	var data struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", fmt.Errorf("decode participants/active response: %w", err)
	}
	if data.SessionID == "" {
		return "", errors.New("participants/active response missing sessionId")
	}
	return data.SessionID, nil
}

func (c *ocsClient) setGuestName(ctx context.Context, roomToken, guestName string) error {
	payload := url.Values{}
	payload.Set("displayName", guestName)
	_, err := c.request(ctx, http.MethodPost, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v1/guest/%s/name", roomToken), nil, payload)
	return err
}

func (c *ocsClient) fetchSignalingSettings(ctx context.Context, roomToken string) (*signalingSettings, error) {
	query := url.Values{}
	query.Set("token", roomToken)
	raw, err := c.request(ctx, http.MethodGet, "/ocs/v2.php/apps/spreed/api/v3/signaling/settings", query, nil)
	if err != nil {
		return nil, err
	}

	var out signalingSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode signaling settings: %w", err)
	}
	return &out, nil
}

func (c *ocsClient) joinCall(ctx context.Context, roomToken string, flags int) error {
	payload := map[string]any{
		"flags":            flags,
		"silent":           false,
		"recordingConsent": false,
		"silentFor":        []any{},
	}
	_, err := c.requestJSON(ctx, http.MethodPost, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/call/%s", roomToken), nil, payload)
	return err
}

func (c *ocsClient) updateCallFlags(ctx context.Context, roomToken string, flags int) error {
	payload := map[string]any{
		"flags": flags,
	}
	_, err := c.requestJSON(ctx, http.MethodPut, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/call/%s", roomToken), nil, payload)
	return err
}

func (c *ocsClient) leaveCall(ctx context.Context, roomToken string) error {
	_, err := c.requestJSON(ctx, http.MethodDelete, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/call/%s", roomToken), nil, map[string]any{"all": false})
	return err
}

func (c *ocsClient) leaveParticipantActive(ctx context.Context, roomToken string) error {
	_, err := c.request(ctx, http.MethodDelete, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/room/%s/participants/active", roomToken), nil, nil)
	return err
}

func (c *ocsClient) startRecording(ctx context.Context, roomToken string, status string) error {
	payload := url.Values{}
	payload.Set("status", status)
	_, err := c.request(ctx, http.MethodPost, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v1/recording/%s", roomToken), nil, payload)
	return err
}

func (c *ocsClient) roomRecordingState(ctx context.Context, roomToken string) (int, error) {
	raw, err := c.request(ctx, http.MethodGet, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/room/%s", roomToken), nil, nil)
	if err != nil {
		return 0, err
	}
	var data struct {
		CallRecording any `json:"callRecording"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return 0, fmt.Errorf("decode room recording state: %w", err)
	}
	state, ok := asInt(data.CallRecording)
	if !ok {
		return 0, fmt.Errorf("room response missing numeric callRecording: %v", data.CallRecording)
	}
	return state, nil
}

func (c *ocsClient) request(ctx context.Context, method, path string, query url.Values, form url.Values) (json.RawMessage, error) {
	var body io.Reader
	var contentType string
	if form != nil {
		body = bytes.NewBufferString(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
	return c.doRequest(ctx, method, path, query, body, contentType)
}

func (c *ocsClient) requestJSON(ctx context.Context, method, path string, query url.Values, payload any) (json.RawMessage, error) {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal JSON payload for %s %s: %w", method, path, err)
	}
	return c.doRequest(ctx, method, path, query, bytes.NewReader(bodyBytes), "application/json")
}

func (c *ocsClient) doRequest(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string) (json.RawMessage, error) {
	reqURL := c.baseURL + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, fmt.Errorf("build request %s %s: %w", method, path, err)
	}
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OCS-APIRequest", "true")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response %s %s: %w", method, path, err)
	}

	var env ocsEnvelope
	if err := json.Unmarshal(respBytes, &env); err != nil {
		return nil, fmt.Errorf("decode OCS envelope %s %s: %w", method, path, err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s %s failed: HTTP %d status=%s code=%d message=%q", method, path, resp.StatusCode, env.OCS.Meta.Status, env.OCS.Meta.StatusCode, env.OCS.Meta.Message)
	}
	if env.OCS.Meta.Status != "ok" {
		return nil, fmt.Errorf("%s %s failed: status=%s code=%d message=%q", method, path, env.OCS.Meta.Status, env.OCS.Meta.StatusCode, env.OCS.Meta.Message)
	}

	return env.OCS.Data, nil
}

type signalingResponse struct {
	msg map[string]any
	err error
}

type signalingClient struct {
	wsURL    string
	insecure bool

	conn *websocket.Conn

	events chan map[string]any
	done   chan struct{}

	nextID int64

	mu      sync.Mutex
	writeMu sync.Mutex
	pending map[string]chan signalingResponse
	closed  bool
}

func newSignalingClient(wsURL string, insecure bool) *signalingClient {
	return &signalingClient{
		wsURL:    wsURL,
		insecure: insecure,
		events:   make(chan map[string]any, 128),
		done:     make(chan struct{}),
		pending:  make(map[string]chan signalingResponse),
	}
}

func (c *signalingClient) Connect(ctx context.Context) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		Proxy:            http.ProxyFromEnvironment,
	}
	if c.insecure {
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	conn, _, err := dialer.DialContext(ctx, c.wsURL, nil)
	if err != nil {
		return fmt.Errorf("connect signaling websocket: %w", err)
	}
	c.conn = conn
	go c.receiver()
	return nil
}

func (c *signalingClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	for _, ch := range c.pending {
		ch <- signalingResponse{err: errors.New("signaling client closed")}
		close(ch)
	}
	c.pending = map[string]chan signalingResponse{}
	c.mu.Unlock()

	close(c.done)
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *signalingClient) Send(payload map[string]any) error {
	if c.conn == nil {
		return errors.New("signaling websocket not connected")
	}
	return c.writeJSON(payload)
}

func (c *signalingClient) Request(ctx context.Context, payload map[string]any, timeout time.Duration) (map[string]any, error) {
	if c.conn == nil {
		return nil, errors.New("signaling websocket not connected")
	}

	id := fmt.Sprintf("%d", atomic.AddInt64(&c.nextID, 1))
	req := cloneMap(payload)
	req["id"] = id

	respCh := make(chan signalingResponse, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("signaling client closed")
	}
	c.pending[id] = respCh
	c.mu.Unlock()

	if err := c.writeJSON(req); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("send signaling request: %w", err)
	}

	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}

	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-c.done:
		return nil, errors.New("signaling client closed")
	case <-timer:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("signaling request %s timed out", id)
	case res := <-respCh:
		if res.err != nil {
			return nil, res.err
		}
		return res.msg, nil
	}
}

func (c *signalingClient) NextEvent(ctx context.Context, timeout time.Duration) (map[string]any, error) {
	if timeout <= 0 {
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-c.done:
			return nil, errors.New("signaling client closed")
		case ev := <-c.events:
			return ev, nil
		}
	}

	t := time.NewTimer(timeout)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-c.done:
		return nil, errors.New("signaling client closed")
	case <-t.C:
		return nil, context.DeadlineExceeded
	case ev := <-c.events:
		return ev, nil
	}
}

func (c *signalingClient) receiver() {
	for {
		_, payload, err := c.conn.ReadMessage()
		if err != nil {
			c.emitConnectionError(fmt.Errorf("read signaling message: %w", err))
			return
		}

		var msg map[string]any
		if err := json.Unmarshal(payload, &msg); err != nil {
			c.emitConnectionError(fmt.Errorf("decode signaling message: %w", err))
			return
		}

		id := asString(msg["id"])
		if id != "" {
			c.mu.Lock()
			ch, ok := c.pending[id]
			if ok {
				delete(c.pending, id)
			}
			c.mu.Unlock()
			if ok {
				ch <- signalingResponse{msg: msg}
				close(ch)
				continue
			}
		}

		select {
		case <-c.done:
			return
		case c.events <- msg:
		}
	}
}

func (c *signalingClient) writeJSON(payload any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteJSON(payload)
}

func (c *signalingClient) emitConnectionError(err error) {
	select {
	case <-c.done:
		return
	case c.events <- map[string]any{"type": "_connection_error", "error": err.Error()}:
	}
}

type mediaStartGate struct {
	ready chan struct{}
	once  sync.Once
	err   error
}

func newMediaStartGate() *mediaStartGate {
	return &mediaStartGate{ready: make(chan struct{})}
}

func (g *mediaStartGate) open(err error) {
	g.once.Do(func() {
		g.err = err
		close(g.ready)
	})
}

func (g *mediaStartGate) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-g.ready:
		return g.err
	}
}

type botConfig struct {
	Index                int
	BaseURL              string
	RoomToken            string
	GuestName            string
	VideoPath            string
	AudioPath            string
	JoinDelay            time.Duration
	AudioReady           time.Duration
	SyncShift            time.Duration
	Duration             time.Duration
	Insecure             bool
	AuthUser             string
	AuthPass             string
	RecordingStarter     bool
	RecordingStatus      string
	RecordingPollTimeout time.Duration
	MediaStartGate       *mediaStartGate
	CallURLRaw           string
}

type muteRotationGate struct {
	File string
}

type recordingOnlyStarterConfig struct {
	BaseURL              string
	RoomToken            string
	DisplayName          string
	AuthUser             string
	AuthPass             string
	Insecure             bool
	RecordingStatus      string
	RecordingPollTimeout time.Duration
	MediaStartGate       *mediaStartGate
}

type bot struct {
	cfg *botConfig

	http      *ocsClient
	settings  *signalingSettings
	signaling *signalingClient

	nextcloudSessionID string
	stateMu            sync.RWMutex
	signalingSessionID string

	pc          *webrtc.PeerConnection
	videoTrack  *webrtc.TrackLocalStaticSample
	audioTrack  *webrtc.TrackLocalStaticSample
	videoSender *webrtc.RTPSender
	audioSender *webrtc.RTPSender
	callSID     string

	answerMu       sync.Mutex
	answerReceived bool
	answerCh       chan struct{}

	connectedCh chan struct{}
	doneCh      chan struct{}
	connected   sync.Once
	done        sync.Once

	muteMu     sync.Mutex
	audioMuted bool

	audienceMu sync.Mutex
	audience   map[string]struct{}

	eventCancel context.CancelFunc
	mediaCancel context.CancelFunc

	streamStartedAt atomic.Int64
	videoSamples    atomic.Uint64
	audioSamples    atomic.Uint64
	videoBytes      atomic.Uint64
	audioBytes      atomic.Uint64
	lastVideoWrite  atomic.Int64
	lastAudioWrite  atomic.Int64
}

func newBot(cfg *botConfig) *bot {
	return &bot{
		cfg:         cfg,
		http:        newOCSClient(cfg.BaseURL, cfg.Insecure, cfg.AuthUser, cfg.AuthPass),
		callSID:     randomHex(16),
		answerCh:    make(chan struct{}),
		connectedCh: make(chan struct{}),
		doneCh:      make(chan struct{}),
		audience:    make(map[string]struct{}),
	}
}

func (b *bot) logf(format string, args ...any) {
	prefix := fmt.Sprintf("[bot %02d %s] ", b.cfg.Index, b.cfg.GuestName)
	log.Printf(prefix+format, args...)
}

func runRecordingOnlyStarter(parent context.Context, cfg recordingOnlyStarterConfig) (runErr error) {
	name := strings.TrimSpace(cfg.DisplayName)
	if name == "" {
		name = cfg.AuthUser
	}
	b := newBot(&botConfig{
		Index:                0,
		BaseURL:              cfg.BaseURL,
		RoomToken:            cfg.RoomToken,
		GuestName:            name,
		Insecure:             cfg.Insecure,
		AuthUser:             cfg.AuthUser,
		AuthPass:             cfg.AuthPass,
		RecordingStatus:      cfg.RecordingStatus,
		RecordingPollTimeout: cfg.RecordingPollTimeout,
	})
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := b.cleanup(cleanupCtx); err != nil && runErr == nil {
			runErr = err
		}
	}()

	fail := func(err error) error {
		if cfg.MediaStartGate != nil {
			cfg.MediaStartGate.open(err)
		}
		return err
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if err := b.joinConversation(ctx); err != nil {
		return fail(fmt.Errorf("recording starter join conversation: %w", err))
	}
	if err := b.fetchSignalingSettings(ctx); err != nil {
		return fail(fmt.Errorf("recording starter fetch signaling settings: %w", err))
	}
	if err := b.connectSignaling(ctx); err != nil {
		return fail(fmt.Errorf("recording starter connect signaling: %w", err))
	}
	if err := b.hello(ctx); err != nil {
		return fail(fmt.Errorf("recording starter hello: %w", err))
	}
	if err := b.joinSignalingRoom(ctx); err != nil {
		return fail(fmt.Errorf("recording starter join signaling room: %w", err))
	}
	if err := b.joinCall(ctx); err != nil {
		return fail(fmt.Errorf("recording starter join call: %w", err))
	}

	err := b.startRecordingAndWait(ctx)
	if cfg.MediaStartGate != nil {
		cfg.MediaStartGate.open(err)
	}
	if err != nil {
		return err
	}
	b.logf("recording-only starter waiting while media publishers run")
	<-ctx.Done()
	return nil
}

func (b *bot) run(parent context.Context) (runErr error) {
	defer b.markDone()
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := b.cleanup(cleanupCtx); err != nil {
			if runErr == nil {
				runErr = err
			}
		}
	}()

	if b.cfg.JoinDelay > 0 {
		b.logf("waiting join delay %s", b.cfg.JoinDelay)
		if !sleepContext(parent, b.cfg.JoinDelay) {
			return nil
		}
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	if err := b.joinConversation(ctx); err != nil {
		return err
	}
	if err := b.fetchSignalingSettings(ctx); err != nil {
		return err
	}
	if err := b.connectSignaling(ctx); err != nil {
		return err
	}
	if err := b.hello(ctx); err != nil {
		return err
	}
	if err := b.joinSignalingRoom(ctx); err != nil {
		return err
	}
	if err := b.joinCall(ctx); err != nil {
		return err
	}

	eventCtx, eventCancel := context.WithCancel(ctx)
	b.eventCancel = eventCancel
	eventErrCh := make(chan error, 1)
	go func() {
		eventErrCh <- b.eventLoop(eventCtx)
	}()

	if err := b.startWebRTC(ctx); err != nil {
		return err
	}
	if err := b.setAudioMuted(true); err != nil {
		return err
	}
	b.markConnected()
	b.logf("connected")

	if b.cfg.RecordingStarter {
		err := b.startRecordingAndWait(ctx)
		if b.cfg.MediaStartGate != nil {
			b.cfg.MediaStartGate.open(err)
		}
		if err != nil {
			return err
		}
	}
	if b.cfg.MediaStartGate != nil {
		if err := b.cfg.MediaStartGate.wait(ctx); err != nil {
			return err
		}
	}
	b.logf("connected and streaming")

	mediaCtx, mediaCancel := context.WithCancel(ctx)
	b.mediaCancel = mediaCancel
	b.streamStartedAt.Store(time.Now().UnixNano())
	mediaErrCh := make(chan error, 1)
	go func() {
		mediaErrCh <- b.runMedia(mediaCtx)
	}()
	statsCtx, statsCancel := context.WithCancel(ctx)
	defer statsCancel()
	go b.mediaStatsLoop(statsCtx, 5*time.Second)

	if b.cfg.Duration > 0 {
		b.logf("running for %s", b.cfg.Duration)
		timer := time.NewTimer(b.cfg.Duration)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil
		case err := <-eventErrCh:
			if err != nil {
				return err
			}
			return nil
		case err := <-mediaErrCh:
			if err != nil {
				return err
			}
			return nil
		case <-timer.C:
			b.logf("duration reached")
			return nil
		}
	}

	b.logf("running until media EOF")
	select {
	case <-ctx.Done():
		return nil
	case err := <-eventErrCh:
		if err != nil {
			return err
		}
		return nil
	case err := <-mediaErrCh:
		if err != nil {
			return err
		}
		b.logf("media completed")
		return nil
	}
}

func (b *bot) markConnected() {
	b.connected.Do(func() { close(b.connectedCh) })
}

func (b *bot) markDone() {
	b.done.Do(func() { close(b.doneCh) })
}

func (b *bot) isConnected() bool {
	select {
	case <-b.connectedCh:
		return true
	default:
		return false
	}
}

func (b *bot) isDone() bool {
	select {
	case <-b.doneCh:
		return true
	default:
		return false
	}
}

func (b *bot) setSignalingSessionID(sessionID string) {
	b.stateMu.Lock()
	b.signalingSessionID = strings.TrimSpace(sessionID)
	b.stateMu.Unlock()
}

func (b *bot) getSignalingSessionID() string {
	b.stateMu.RLock()
	defer b.stateMu.RUnlock()
	return b.signalingSessionID
}

func (b *bot) isAudioReady() bool {
	if b.cfg.AudioReady <= 0 {
		return true
	}
	startNanos := b.streamStartedAt.Load()
	if startNanos == 0 {
		return false
	}
	started := time.Unix(0, startNanos)
	return time.Since(started) >= b.cfg.AudioReady
}

func (b *bot) joinConversation(ctx context.Context) error {
	if err := b.http.getRoom(ctx, b.cfg.RoomToken); err != nil {
		return fmt.Errorf("room check failed: %w", err)
	}
	authenticated := strings.TrimSpace(b.cfg.AuthUser) != ""
	displayName := b.cfg.GuestName
	if authenticated {
		displayName = ""
	}
	sessionID, err := b.http.markParticipantActive(ctx, b.cfg.RoomToken, displayName, authenticated)
	if err != nil {
		return fmt.Errorf("participants/active failed: %w", err)
	}
	b.nextcloudSessionID = sessionID
	if !authenticated {
		if err := b.http.setGuestName(ctx, b.cfg.RoomToken, b.cfg.GuestName); err != nil {
			b.logf("guest display name not set (%v); continuing", err)
		}
	}
	return nil
}

func (b *bot) fetchSignalingSettings(ctx context.Context) error {
	settings, err := b.http.fetchSignalingSettings(ctx, b.cfg.RoomToken)
	if err != nil {
		return fmt.Errorf("fetch signaling settings: %w", err)
	}
	b.settings = settings
	return nil
}

func (b *bot) backendURL() string {
	return strings.TrimRight(b.cfg.BaseURL, "/") + "/ocs/v2.php/apps/spreed/api/v3/signaling/backend"
}

func (b *bot) connectSignaling(ctx context.Context) error {
	if b.settings == nil {
		return errors.New("signaling settings not loaded")
	}
	wsServer := b.settings.primaryServer()
	if wsServer == "" {
		return errors.New("signaling settings missing signaling server")
	}

	b.signaling = newSignalingClient(toWSURL(wsServer), b.cfg.Insecure)
	if err := b.signaling.Connect(ctx); err != nil {
		return err
	}
	return nil
}

func (b *bot) hello(ctx context.Context) error {
	if b.settings == nil {
		return errors.New("signaling settings not loaded")
	}
	if b.signaling == nil {
		return errors.New("signaling websocket not connected")
	}

	versions := make([]string, 0, 2)
	if _, ok := b.settings.HelloAuthParams["2.0"]; ok {
		versions = append(versions, "2.0")
	}
	if _, ok := b.settings.HelloAuthParams["1.0"]; ok {
		versions = append(versions, "1.0")
	}
	if len(versions) == 0 {
		return errors.New("signaling settings missing helloAuthParams")
	}

	var lastErr error
	for _, version := range versions {
		var params any
		if err := json.Unmarshal(b.settings.HelloAuthParams[version], &params); err != nil {
			lastErr = fmt.Errorf("decode helloAuthParams[%s]: %w", version, err)
			continue
		}

		request := map[string]any{
			"type": "hello",
			"hello": map[string]any{
				"version": version,
				"auth": map[string]any{
					"url":    b.backendURL(),
					"params": params,
				},
				"features": []string{"chat-relay"},
			},
		}

		response, err := b.signaling.Request(ctx, request, 15*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		if asString(response["type"]) != "hello" {
			lastErr = fmt.Errorf("hello version %s returned type=%s", version, asString(response["type"]))
			continue
		}
		hello := asMap(response["hello"])
		sessionID := asString(hello["sessionid"])
		if sessionID == "" {
			return errors.New("hello response missing signaling sessionid")
		}
		b.setSignalingSessionID(sessionID)
		b.logf("hello ok (version %s, session=%s)", version, sessionID)
		return nil
	}

	if lastErr == nil {
		lastErr = errors.New("all signaling hello attempts failed")
	}
	return lastErr
}

func (b *bot) joinSignalingRoom(ctx context.Context) error {
	if b.signaling == nil {
		return errors.New("signaling websocket not connected")
	}
	response, err := b.signaling.Request(ctx, map[string]any{
		"type": "room",
		"room": map[string]any{
			"roomid":    b.cfg.RoomToken,
			"sessionid": b.nextcloudSessionID,
		},
	}, 15*time.Second)
	if err != nil {
		return err
	}
	if asString(response["type"]) == "error" {
		return fmt.Errorf("room join failed: %v", response)
	}
	b.logf("joined signaling room")
	return nil
}

func (b *bot) startRecordingAndWait(ctx context.Context) error {
	if strings.TrimSpace(b.cfg.AuthUser) == "" {
		return errors.New("recording starter requires authenticated user credentials")
	}
	status := strings.TrimSpace(b.cfg.RecordingStatus)
	if status == "" {
		status = "1"
	}
	b.logf("starting Nextcloud Talk recording before media")
	if err := b.http.startRecording(ctx, b.cfg.RoomToken, status); err != nil {
		return fmt.Errorf("start recording: %w", err)
	}
	timeout := b.cfg.RecordingPollTimeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lastState := -1
	for {
		state, err := b.http.roomRecordingState(pollCtx, b.cfg.RoomToken)
		if err != nil {
			return fmt.Errorf("poll recording state: %w", err)
		}
		lastState = state
		if state == 1 {
			b.logf("recording active")
			return nil
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-pollCtx.Done():
			timer.Stop()
			return fmt.Errorf("recording did not become active before timeout; last callRecording=%d", lastState)
		case <-timer.C:
		}
	}
}

func (b *bot) joinCall(ctx context.Context) error {
	if err := b.http.joinCall(ctx, b.cfg.RoomToken, joinFlagsAudioVideo); err != nil {
		if strings.Contains(err.Error(), "code=404") {
			b.logf("join call returned 404; continuing")
			return nil
		}
		return fmt.Errorf("join call failed: %w", err)
	}
	return nil
}

func (b *bot) updateCallFlags(ctx context.Context, flags int) error {
	if err := b.http.updateCallFlags(ctx, b.cfg.RoomToken, flags); err != nil {
		if strings.Contains(err.Error(), "code=404") {
			b.logf("update call flags returned 404; continuing")
			return nil
		}
		if strings.Contains(err.Error(), "code=405") {
			b.logf("update call flags returned 405; continuing")
			return nil
		}
		return fmt.Errorf("update call flags=%d failed: %w", flags, err)
	}
	return nil
}

func (b *bot) startWebRTC(ctx context.Context) error {
	iceServers := buildICEServers(b.settings)
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers})
	if err != nil {
		return fmt.Errorf("create peer connection: %w", err)
	}
	b.pc = pc

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		b.logf("peer state: %s", state.String())
	})

	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000},
		"video",
		fmt.Sprintf("video-%d", b.cfg.Index),
	)
	if err != nil {
		return fmt.Errorf("create video track: %w", err)
	}
	b.videoTrack = videoTrack

	audioTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio",
		fmt.Sprintf("audio-%d", b.cfg.Index),
	)
	if err != nil {
		return fmt.Errorf("create audio track: %w", err)
	}
	b.audioTrack = audioTrack

	videoSender, err := pc.AddTrack(videoTrack)
	if err != nil {
		return fmt.Errorf("add video track: %w", err)
	}
	b.videoSender = videoSender
	go drainRTCP(videoSender)

	audioSender, err := pc.AddTrack(audioTrack)
	if err != nil {
		return fmt.Errorf("add audio track: %w", err)
	}
	b.audioSender = audioSender
	go drainRTCP(audioSender)

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("create offer: %w", err)
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("set local description (offer): %w", err)
	}
	select {
	case <-gather:
	case <-time.After(8 * time.Second):
		b.logf("ICE gathering timeout reached; continuing")
	}

	local := pc.LocalDescription()
	if local == nil {
		return errors.New("local description is nil after offer")
	}
	if err := b.sendCallMessage(map[string]any{
		"to":       b.getSignalingSessionID(),
		"sid":      b.callSID,
		"roomType": "video",
		"type":     "offer",
		"payload": map[string]any{
			"type": local.Type.String(),
			"sdp":  local.SDP,
		},
	}); err != nil {
		return err
	}
	b.logf("offer sent")

	answerTimeout := time.NewTimer(25 * time.Second)
	defer answerTimeout.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-answerTimeout.C:
		return errors.New("timeout waiting for answer")
	case <-b.answerCh:
		return nil
	}
}

func (b *bot) sendCallMessage(data map[string]any) error {
	if b.signaling == nil {
		return errors.New("signaling websocket not connected")
	}
	to := asString(data["to"])
	if to == "" {
		return errors.New("missing recipient session id")
	}
	payload := map[string]any{
		"type": "message",
		"message": map[string]any{
			"recipient": map[string]any{
				"type":      "session",
				"sessionid": to,
			},
			"data": data,
		},
	}
	if err := b.signaling.Send(payload); err != nil {
		return fmt.Errorf("send signaling message: %w", err)
	}
	return nil
}

func (b *bot) eventLoop(ctx context.Context) error {
	if b.signaling == nil {
		return errors.New("signaling websocket not connected")
	}
	for {
		event, err := b.signaling.NextEvent(ctx, time.Second)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			if isContextDone(err) {
				return nil
			}
			return err
		}

		eventType := asString(event["type"])
		if eventType == "_connection_error" {
			return fmt.Errorf("signaling connection error: %s", asString(event["error"]))
		}
		if eventType == "event" {
			b.handleSignalingEvent(event)
			continue
		}
		if eventType != "message" {
			continue
		}

		message := asMap(event["message"])
		data := asMap(message["data"])
		if len(data) == 0 {
			continue
		}
		sender := asString(asMap(message["sender"])["sessionid"])
		if sender != "" {
			if _, ok := data["from"]; !ok {
				data = cloneMap(data)
				data["from"] = sender
			}
		}

		roomType := asString(data["roomType"])
		if roomType != "" && roomType != "video" {
			continue
		}

		if err := b.handleCallData(ctx, data); err != nil {
			return err
		}
	}
}

func (b *bot) handleSignalingEvent(event map[string]any) {
	payload := asMap(event["event"])
	if len(payload) == 0 {
		return
	}

	switch asString(payload["target"]) {
	case "room":
		for _, raw := range asSlice(payload["join"]) {
			entry := asMap(raw)
			b.addAudienceSession(valueOr(asString(entry["sessionid"]), asString(entry["sessionId"])))
		}
		for _, raw := range asSlice(payload["change"]) {
			entry := asMap(raw)
			b.addAudienceSession(valueOr(asString(entry["sessionid"]), asString(entry["sessionId"])))
		}
		for _, raw := range asSlice(payload["leave"]) {
			b.removeAudienceSession(asString(raw))
		}
	case "participants":
		update := asMap(payload["update"])
		if len(update) == 0 {
			return
		}

		if asBool(update["all"]) {
			if flags, ok := asInt(update["incall"]); ok && flags == 0 {
				b.clearAudienceSessions()
				return
			}
		}

		users := asSlice(update["users"])
		if len(users) == 0 {
			users = asSlice(update["changed"])
		}
		for _, raw := range users {
			user := asMap(raw)
			if len(user) == 0 {
				continue
			}
			sessionID := valueOr(asString(user["sessionId"]), asString(user["sessionid"]))
			if sessionID == "" {
				continue
			}
			if asBool(user["internal"]) {
				b.removeAudienceSession(sessionID)
				continue
			}
			if flags, ok := asInt(user["inCall"]); ok && flags == 0 {
				b.removeAudienceSession(sessionID)
				continue
			}
			b.addAudienceSession(sessionID)
		}
	}
}

func (b *bot) handleCallData(ctx context.Context, data map[string]any) error {
	if b.pc == nil {
		return nil
	}
	sid := asString(data["sid"])
	if sid != "" && sid != b.callSID {
		return nil
	}

	msgType := asString(data["type"])
	payload := asMap(data["payload"])

	switch msgType {
	case "answer":
		sdp := asString(payload["sdp"])
		if sdp == "" {
			return nil
		}
		if err := b.pc.SetRemoteDescription(webrtc.SessionDescription{Type: parseSDPType(asString(payload["type"]), webrtc.SDPTypeAnswer), SDP: sdp}); err != nil {
			return fmt.Errorf("set remote answer: %w", err)
		}
		upper := strings.ToUpper(sdp)
		b.logf(
			"answer codecs: opus=%t vp8=%t h264=%t",
			strings.Contains(upper, "OPUS"),
			strings.Contains(upper, "VP8"),
			strings.Contains(upper, "H264"),
		)
		b.answerMu.Lock()
		if !b.answerReceived {
			b.answerReceived = true
			close(b.answerCh)
		}
		b.answerMu.Unlock()
		b.logf("received answer")
		return nil
	case "offer":
		sdp := asString(payload["sdp"])
		if sdp == "" {
			return nil
		}
		if err := b.pc.SetRemoteDescription(webrtc.SessionDescription{Type: parseSDPType(asString(payload["type"]), webrtc.SDPTypeOffer), SDP: sdp}); err != nil {
			return fmt.Errorf("set remote offer: %w", err)
		}
		answer, err := b.pc.CreateAnswer(nil)
		if err != nil {
			return fmt.Errorf("create renegotiation answer: %w", err)
		}
		gather := webrtc.GatheringCompletePromise(b.pc)
		if err := b.pc.SetLocalDescription(answer); err != nil {
			return fmt.Errorf("set local description (answer): %w", err)
		}
		select {
		case <-gather:
		case <-time.After(4 * time.Second):
			b.logf("ICE gathering timeout on renegotiation")
		}
		local := b.pc.LocalDescription()
		if local == nil {
			return errors.New("local description nil on renegotiation answer")
		}
		to := asString(data["from"])
		if to == "" {
			to = b.getSignalingSessionID()
		}
		answerSID := sid
		if answerSID == "" {
			answerSID = b.callSID
		}
		if err := b.sendCallMessage(map[string]any{
			"to":       to,
			"sid":      answerSID,
			"roomType": valueOr(asString(data["roomType"]), "video"),
			"type":     "answer",
			"payload": map[string]any{
				"type": local.Type.String(),
				"sdp":  local.SDP,
			},
		}); err != nil {
			return err
		}
		b.logf("handled renegotiation offer")
		return nil
	case "candidate":
		candidateInfo := asMap(payload["candidate"])
		if len(candidateInfo) == 0 {
			candidateInfo = asMap(data["candidate"])
		}
		candidate := asString(candidateInfo["candidate"])
		if candidate == "" {
			return nil
		}
		if !strings.HasPrefix(candidate, "candidate:") {
			candidate = "candidate:" + candidate
		}

		var mid *string
		if value := asString(candidateInfo["sdpMid"]); value != "" {
			mid = &value
		}

		var mLine *uint16
		if value, ok := asUint16(candidateInfo["sdpMLineIndex"]); ok {
			mLine = &value
		}

		if err := b.pc.AddICECandidate(webrtc.ICECandidateInit{
			Candidate:     candidate,
			SDPMid:        mid,
			SDPMLineIndex: mLine,
		}); err != nil {
			return fmt.Errorf("add ICE candidate: %w", err)
		}
		return nil
	case "endOfCandidates":
		return nil
	default:
		return nil
	}
}

func (b *bot) runMedia(ctx context.Context) error {
	errCh := make(chan error, 2)
	go func() { errCh <- b.streamVideo(ctx) }()
	go func() { errCh <- b.streamAudio(ctx) }()

	completed := 0
	for completed < 2 {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			if err != nil {
				return err
			}
			completed++
		}
	}
	return nil
}

func (b *bot) mediaStatsLoop(ctx context.Context, every time.Duration) {
	if every <= 0 {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			videoSamples := b.videoSamples.Load()
			audioSamples := b.audioSamples.Load()
			videoBytes := b.videoBytes.Load()
			audioBytes := b.audioBytes.Load()
			lastVideo := b.lastVideoWrite.Load()
			lastAudio := b.lastAudioWrite.Load()
			b.logf(
				"media stats: video_samples=%d video_bytes=%d last_video=%s audio_samples=%d audio_bytes=%d last_audio=%s muted=%t",
				videoSamples,
				videoBytes,
				sinceLabel(lastVideo),
				audioSamples,
				audioBytes,
				sinceLabel(lastAudio),
				b.isAudioMutedFlag(),
			)
		}
	}
}

func sinceLabel(lastUnixNano int64) string {
	if lastUnixNano <= 0 {
		return "never"
	}
	elapsed := time.Since(time.Unix(0, lastUnixNano)).Round(100 * time.Millisecond)
	if elapsed < 0 {
		return "0s"
	}
	return elapsed.String() + " ago"
}

type opusPacket struct {
	data     []byte
	duration time.Duration
}

type opusOggPacketReader struct {
	reader      *bufio.Reader
	queue       []opusPacket
	carry       []byte
	lastGranule uint64
}

func newOpusOggPacketReader(in io.Reader) *opusOggPacketReader {
	return &opusOggPacketReader{
		reader: bufio.NewReader(in),
		queue:  make([]opusPacket, 0, 16),
	}
}

func (r *opusOggPacketReader) NextPacket() ([]byte, time.Duration, error) {
	for {
		if len(r.queue) > 0 {
			pkt := r.queue[0]
			r.queue = r.queue[1:]
			return pkt.data, pkt.duration, nil
		}

		header := make([]byte, 27)
		if _, err := io.ReadFull(r.reader, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, 0, io.EOF
			}
			return nil, 0, err
		}
		if string(header[0:4]) != "OggS" {
			return nil, 0, fmt.Errorf("invalid ogg capture pattern")
		}

		granule := binary.LittleEndian.Uint64(header[6:14])
		pageSegments := int(header[26])
		segmentTable := make([]byte, pageSegments)
		if _, err := io.ReadFull(r.reader, segmentTable); err != nil {
			return nil, 0, err
		}

		bodyLen := 0
		for _, seg := range segmentTable {
			bodyLen += int(seg)
		}
		body := make([]byte, bodyLen)
		if _, err := io.ReadFull(r.reader, body); err != nil {
			return nil, 0, err
		}

		var completed [][]byte
		cursor := 0
		packet := r.carry
		for _, seg := range segmentTable {
			next := cursor + int(seg)
			if next > len(body) {
				return nil, 0, fmt.Errorf("invalid ogg segment length")
			}
			packet = append(packet, body[cursor:next]...)
			cursor = next
			if seg < 255 {
				completed = append(completed, packet)
				packet = nil
			}
		}
		r.carry = packet

		perPacketDuration := audioFrameDuration
		if len(completed) > 0 && granule > r.lastGranule {
			sampleCount := granule - r.lastGranule
			pageDuration := time.Duration((float64(sampleCount) / 48000.0) * float64(time.Second))
			if pageDuration > 0 {
				perPacketDuration = pageDuration / time.Duration(len(completed))
				if perPacketDuration <= 0 {
					perPacketDuration = audioFrameDuration
				}
			}
			r.lastGranule = granule
		}

		for _, raw := range completed {
			if len(raw) == 0 {
				continue
			}
			if bytes.HasPrefix(raw, []byte("OpusHead")) || bytes.HasPrefix(raw, []byte("OpusTags")) {
				continue
			}
			pkt := make([]byte, len(raw))
			copy(pkt, raw)
			r.queue = append(r.queue, opusPacket{data: pkt, duration: perPacketDuration})
		}
	}
}

func (b *bot) streamVideo(ctx context.Context) error {
	if b.cfg.SyncShift < 0 {
		if !sleepContext(ctx, -b.cfg.SyncShift) {
			return nil
		}
	}

	file, err := os.Open(b.cfg.VideoPath)
	if err != nil {
		return fmt.Errorf("open video file %s: %w", b.cfg.VideoPath, err)
	}
	defer file.Close()

	reader, _, err := ivfreader.NewWith(file)
	if err != nil {
		return fmt.Errorf("open IVF reader %s: %w", b.cfg.VideoPath, err)
	}

	videoSkip := b.cfg.SyncShift
	if videoSkip < 0 {
		videoSkip = 0
	}
	closedPipeLogged := false

	for {
		frame, _, err := reader.ParseNextFrame()
		if errors.Is(err, io.EOF) {
			b.logf("video EOF: samples=%d bytes=%d", b.videoSamples.Load(), b.videoBytes.Load())
			return nil
		}
		if err != nil {
			return fmt.Errorf("read IVF frame %s: %w", b.cfg.VideoPath, err)
		}
		if videoSkip > 0 {
			videoSkip -= videoFrameDuration
			continue
		}
		if err := b.videoTrack.WriteSample(media.Sample{Data: frame, Duration: videoFrameDuration}); err != nil {
			if errors.Is(err, io.ErrClosedPipe) {
				if !closedPipeLogged {
					b.logf("video write hit closed pipe; waiting for binding")
					closedPipeLogged = true
				}
				if !sleepContext(ctx, videoFrameDuration) {
					return nil
				}
				continue
			}
			return fmt.Errorf("write video sample: %w", err)
		}
		b.videoSamples.Add(1)
		b.videoBytes.Add(uint64(len(frame)))
		b.lastVideoWrite.Store(time.Now().UnixNano())
		if !sleepContext(ctx, videoFrameDuration) {
			return nil
		}
	}
}

func (b *bot) streamAudio(ctx context.Context) error {
	if b.cfg.SyncShift < 0 {
		if !sleepContext(ctx, -b.cfg.SyncShift) {
			return nil
		}
	}

	file, err := os.Open(b.cfg.AudioPath)
	if err != nil {
		return fmt.Errorf("open audio file %s: %w", b.cfg.AudioPath, err)
	}
	defer file.Close()

	reader := newOpusOggPacketReader(file)

	audioSkip := b.cfg.SyncShift
	if audioSkip < 0 {
		audioSkip = 0
	}
	closedPipeLogged := false

	for {
		packetData, packetDuration, err := reader.NextPacket()
		if errors.Is(err, io.EOF) {
			b.logf("audio EOF: samples=%d bytes=%d", b.audioSamples.Load(), b.audioBytes.Load())
			return nil
		}
		if err != nil {
			return fmt.Errorf("read OGG page %s: %w", b.cfg.AudioPath, err)
		}
		if packetDuration <= 0 {
			packetDuration = audioFrameDuration
		}

		if audioSkip > 0 {
			audioSkip -= packetDuration
			continue
		}
		if b.isAudioMutedFlag() {
			if !sleepContext(ctx, packetDuration) {
				return nil
			}
			continue
		}
		if err := b.audioTrack.WriteSample(media.Sample{Data: packetData, Duration: packetDuration}); err != nil {
			if errors.Is(err, io.ErrClosedPipe) {
				if !closedPipeLogged {
					b.logf("audio write hit closed pipe; waiting for binding")
					closedPipeLogged = true
				}
				if !sleepContext(ctx, packetDuration) {
					return nil
				}
				continue
			}
			return fmt.Errorf("write audio sample: %w", err)
		}
		b.audioSamples.Add(1)
		b.audioBytes.Add(uint64(len(packetData)))
		b.lastAudioWrite.Store(time.Now().UnixNano())
		if !sleepContext(ctx, packetDuration) {
			return nil
		}
	}
}

func (b *bot) setAudioMuted(muted bool) error {
	b.muteMu.Lock()
	if muted == b.audioMuted {
		b.muteMu.Unlock()
		return nil
	}
	b.audioMuted = muted
	b.muteMu.Unlock()

	if muted {
		b.logf("audio muted")
	} else {
		b.logf("audio unmuted")
	}
	return nil
}

func (b *bot) isAudioMutedFlag() bool {
	b.muteMu.Lock()
	defer b.muteMu.Unlock()
	return b.audioMuted
}

func (b *bot) addAudienceSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || sessionID == b.getSignalingSessionID() {
		return
	}
	b.audienceMu.Lock()
	b.audience[sessionID] = struct{}{}
	b.audienceMu.Unlock()
}

func (b *bot) removeAudienceSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	b.audienceMu.Lock()
	delete(b.audience, sessionID)
	b.audienceMu.Unlock()
}

func (b *bot) clearAudienceSessions() {
	b.audienceMu.Lock()
	b.audience = make(map[string]struct{})
	b.audienceMu.Unlock()
}

func (b *bot) audienceSnapshot() []string {
	b.audienceMu.Lock()
	defer b.audienceMu.Unlock()
	out := make([]string, 0, len(b.audience))
	for sessionID := range b.audience {
		if sessionID != "" && sessionID != b.getSignalingSessionID() {
			out = append(out, sessionID)
		}
	}
	return out
}

func (b *bot) externalAudienceSessions(exclude map[string]struct{}) []string {
	sessions := b.audienceSnapshot()
	if len(sessions) == 0 || len(exclude) == 0 {
		return sessions
	}
	out := sessions[:0]
	for _, sessionID := range sessions {
		if _, knownBot := exclude[sessionID]; knownBot {
			continue
		}
		out = append(out, sessionID)
	}
	return out
}

func (b *bot) broadcastMuteState(muted bool) {
	if b.signaling == nil {
		return
	}
	targets := b.audienceSnapshot()
	if len(targets) == 0 {
		return
	}

	msgType := "unmute"
	if muted {
		msgType = "mute"
	}
	payload := map[string]any{"name": "audio"}

	sent := 0
	for _, to := range targets {
		if err := b.sendCallMessage(map[string]any{
			"to":       to,
			"sid":      b.callSID,
			"roomType": "video",
			"type":     msgType,
			"payload":  payload,
		}); err != nil {
			b.logf("broadcast %s to %s failed: %v", msgType, to, err)
			continue
		}
		sent++
	}
	b.logf("broadcast %s to %d/%d sessions", msgType, sent, len(targets))
}

func (b *bot) cleanup(ctx context.Context) error {
	var firstErr error

	if b.mediaCancel != nil {
		b.mediaCancel()
	}
	if b.eventCancel != nil {
		b.eventCancel()
	}

	if b.pc != nil {
		if err := b.pc.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		b.pc = nil
	}

	if b.signaling != nil {
		_ = b.signaling.Send(map[string]any{"type": "bye", "bye": map[string]any{}})
		if err := b.signaling.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		b.signaling = nil
	}

	if b.nextcloudSessionID != "" {
		if err := b.http.leaveCall(ctx, b.cfg.RoomToken); err != nil {
			b.logf("leave call failed (continuing): %v", err)
		}
		if err := b.http.leaveParticipantActive(ctx, b.cfg.RoomToken); err != nil {
			b.logf("leave participants/active failed (continuing): %v", err)
		}
	}

	if b.http != nil {
		b.http.Close()
	}
	return firstErr
}

func drainRTCP(sender *webrtc.RTPSender) {
	if sender == nil {
		return
	}
	buf := make([]byte, 1500)
	for {
		if _, _, err := sender.Read(buf); err != nil {
			return
		}
	}
}

func waitForMuteRotationGate(ctx context.Context, gate muteRotationGate) bool {
	if strings.TrimSpace(gate.File) == "" {
		return true
	}
	log.Printf("[manager] waiting for mute rotation gate: %s", gate.File)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(gate.File); err == nil {
			log.Printf("[manager] mute rotation gate opened")
			return true
		} else if !errors.Is(err, os.ErrNotExist) {
			log.Printf("[manager] mute rotation gate stat failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

func rotateAudio(ctx context.Context, bots []*bot, every time.Duration, gate muteRotationGate) {
	if !waitForMuteRotationGate(ctx, gate) {
		return
	}
	current := -1
	for {
		if allBotsDone(bots) {
			return
		}
		active := make([]int, 0, len(bots))
		for i, b := range bots {
			if b.isConnected() && !b.isDone() && b.isAudioReady() {
				active = append(active, i)
			}
		}
		if len(active) == 0 {
			if !sleepContext(ctx, 250*time.Millisecond) {
				return
			}
			continue
		}

		chosen := -1
		for offset := 1; offset <= len(bots); offset++ {
			idx := (current + offset) % len(bots)
			for _, activeIdx := range active {
				if idx == activeIdx {
					chosen = idx
					break
				}
			}
			if chosen >= 0 {
				break
			}
		}
		if chosen < 0 {
			if !sleepContext(ctx, 250*time.Millisecond) {
				return
			}
			continue
		}

		current = chosen
		for _, idx := range active {
			if err := bots[idx].setAudioMuted(idx != chosen); err != nil {
				log.Printf("[manager] mute switch failed for %s: %v", bots[idx].cfg.GuestName, err)
			}
		}
		mediaSec := bots[chosen].cfg.JoinDelay.Seconds() + float64(bots[chosen].videoSamples.Load())/30.0
		log.Printf("[manager] audible=%s active=%d media_t=%.3f", bots[chosen].cfg.GuestName, len(active), mediaSec)

		if !sleepContext(ctx, every) {
			return
		}
	}
}

func allBotsDone(bots []*bot) bool {
	for _, b := range bots {
		if !b.isDone() {
			return false
		}
	}
	return true
}

func knownBotSessions(bots []*bot) map[string]struct{} {
	out := make(map[string]struct{}, len(bots))
	for _, b := range bots {
		if sid := strings.TrimSpace(b.getSignalingSessionID()); sid != "" {
			out[sid] = struct{}{}
		}
	}
	return out
}

func collectExternalAudienceSessions(bots []*bot) (map[string]struct{}, int) {
	botSessions := knownBotSessions(bots)
	external := map[string]struct{}{}
	for _, b := range bots {
		for _, sid := range b.externalAudienceSessions(botSessions) {
			if sid == "" {
				continue
			}
			external[sid] = struct{}{}
		}
	}
	return external, len(botSessions)
}

func stopWhenAudienceGone(ctx context.Context, bots []*bot, grace time.Duration, cancel context.CancelFunc) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	seenExternal := false
	var emptySince time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			external, knownBots := collectExternalAudienceSessions(bots)
			if knownBots < len(bots) {
				continue
			}

			externalCount := len(external)
			if externalCount > 0 {
				seenExternal = true
				if !emptySince.IsZero() {
					log.Printf("[manager] external participant activity resumed; room-empty auto-stop canceled")
				}
				emptySince = time.Time{}
				continue
			}
			if !seenExternal {
				continue
			}
			if emptySince.IsZero() {
				emptySince = time.Now()
				log.Printf("[manager] no external participants; stopping in %s unless someone rejoins", grace)
				continue
			}
			if time.Since(emptySince) >= grace {
				log.Printf("[manager] room stayed empty for %s; stopping all bots", grace)
				cancel()
				return
			}
		}
	}
}

func buildICEServers(settings *signalingSettings) []webrtc.ICEServer {
	if settings == nil {
		return nil
	}
	servers := make([]webrtc.ICEServer, 0, len(settings.StunServers)+len(settings.TurnServers))
	appendServer := func(src settingICEServer) {
		urls := toStringSlice(src.URLs)
		if len(urls) == 0 {
			return
		}
		servers = append(servers, webrtc.ICEServer{
			URLs:       urls,
			Username:   src.Username,
			Credential: src.Credential,
		})
	}
	for _, src := range settings.StunServers {
		appendServer(src)
	}
	for _, src := range settings.TurnServers {
		appendServer(src)
	}
	return servers
}

func toStringSlice(v any) []string {
	switch raw := v.(type) {
	case string:
		if raw == "" {
			return nil
		}
		return []string{raw}
	case []any:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			if s := asString(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	default:
		return nil
	}
}

func parseCallURL(raw string) (baseURL string, roomToken string, err error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse call URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", "", fmt.Errorf("invalid call URL: %s", raw)
	}
	parts := splitPath(parsed.Path)
	if len(parts) >= 2 && parts[0] == "call" {
		roomToken = parts[1]
	}
	if roomToken == "" {
		return "", "", fmt.Errorf("could not extract room token from %s (expected /call/<token>)", raw)
	}
	return fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host), roomToken, nil
}

func splitPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func toWSURL(serverURL string) string {
	url := strings.TrimRight(serverURL, "/")
	if strings.HasPrefix(url, "https://") {
		url = "wss://" + strings.TrimPrefix(url, "https://")
	} else if strings.HasPrefix(url, "http://") {
		url = "ws://" + strings.TrimPrefix(url, "http://")
	}
	return url + "/spreed"
}

func randomHex(bytesLen int) string {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		fallback := fmt.Sprintf("%d", time.Now().UnixNano())
		return hex.EncodeToString([]byte(fallback))
	}
	return hex.EncodeToString(buf)
}

func parseSDPType(raw string, fallback webrtc.SDPType) webrtc.SDPType {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "offer":
		return webrtc.SDPTypeOffer
	case "answer":
		return webrtc.SDPTypeAnswer
	case "pranswer":
		return webrtc.SDPTypePranswer
	case "rollback":
		return webrtc.SDPTypeRollback
	default:
		return fallback
	}
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func asMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if out, ok := v.(map[string]any); ok {
		return out
	}
	if out, ok := v.(map[string]interface{}); ok {
		res := make(map[string]any, len(out))
		for k, value := range out {
			res[k] = value
		}
		return res
	}
	return nil
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	default:
		return ""
	}
}

func asSlice(v any) []any {
	switch raw := v.(type) {
	case []any:
		return raw
	default:
		return nil
	}
}

func asBool(v any) bool {
	switch raw := v.(type) {
	case bool:
		return raw
	case int:
		return raw != 0
	case int64:
		return raw != 0
	case uint64:
		return raw != 0
	case float64:
		return raw != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func asInt(v any) (int, bool) {
	switch raw := v.(type) {
	case int:
		return raw, true
	case int32:
		return int(raw), true
	case int64:
		return int(raw), true
	case uint:
		return int(raw), true
	case uint32:
		return int(raw), true
	case uint64:
		if raw > uint64(^uint(0)>>1) {
			return 0, false
		}
		return int(raw), true
	case float64:
		return int(raw), true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return 0, false
		}
		return i, true
	default:
		return 0, false
	}
}

func asUint16(v any) (uint16, bool) {
	switch x := v.(type) {
	case uint16:
		return x, true
	case uint32:
		if x > 65535 {
			return 0, false
		}
		return uint16(x), true
	case uint64:
		if x > 65535 {
			return 0, false
		}
		return uint16(x), true
	case int:
		if x < 0 || x > 65535 {
			return 0, false
		}
		return uint16(x), true
	case int64:
		if x < 0 || x > 65535 {
			return 0, false
		}
		return uint16(x), true
	case float64:
		if x < 0 || x > 65535 {
			return 0, false
		}
		return uint16(x), true
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(x), 10, 16)
		if err != nil || i < 0 {
			return 0, false
		}
		return uint16(i), true
	default:
		return 0, false
	}
}

func valueOr(current, fallback string) string {
	if strings.TrimSpace(current) == "" {
		return fallback
	}
	return current
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func isContextDone(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func main() {
	if err := run(); err != nil {
		log.Printf("error: %v", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		callURL                      string
		insecure                     bool
		videoFiles                   stringList
		audioFiles                   stringList
		names                        stringList
		joinDelaySecs                floatList
		audioReadySec                floatList
		syncShiftSec                 floatList
		botDurationSec               floatList
		authUsers                    stringList
		authPasswords                stringList
		durationSec                  float64
		rotateSeconds                float64
		stopWhenRoomEmpty            bool
		recordBeforeMedia            bool
		recordingStarterIndex        int
		recordingStarterAuthUser     string
		recordingStarterAuthPassword string
		recordingStarterName         string
		recordingStatus              string
		recordingTimeoutSec          float64
		roomEmptyGraceSec            float64
		muteStartFile                string
	)

	flag.StringVar(&callURL, "call-url", "", "Talk call URL")
	flag.Var(&videoFiles, "video", "Path to IVF video file (repeat once per participant)")
	flag.Var(&audioFiles, "audio", "Path to OGG Opus audio file (repeat once per participant)")
	flag.Var(&names, "name", "Bot name (repeat up to video count)")
	flag.Var(&joinDelaySecs, "join-delay", "Join delay in seconds (repeat up to video count)")
	flag.Var(&audioReadySec, "audio-ready-after", "Audio can become audible after N seconds from stream start (repeat up to video count)")
	flag.Var(&syncShiftSec, "sync-shift", "Per-bot media time shift in seconds (positive=forward, negative=backward, repeat up to video count)")
	flag.Var(&botDurationSec, "bot-duration", "Per-bot duration in seconds (0 means until EOF, repeat up to video count)")
	flag.Var(&authUsers, "auth-user", "Authenticated Nextcloud user id (repeat exactly once per participant when used)")
	flag.Var(&authPasswords, "auth-password", "Authenticated Nextcloud password (repeat exactly once per participant when used)")
	flag.Float64Var(&durationSec, "duration", 0, "Optional duration override in seconds for all bots")
	flag.Float64Var(&rotateSeconds, "rotate-seconds", defaultRotateSeconds, "Audio rotation interval in seconds")
	flag.BoolVar(&stopWhenRoomEmpty, "stop-when-room-empty", true, "Exit once all non-bot participants leave (after grace)")
	flag.BoolVar(&recordBeforeMedia, "record-before-media", false, "Start Nextcloud Talk recording and wait until active before media samples are sent")
	flag.IntVar(&recordingStarterIndex, "recording-starter-index", 1, "1-based participant index that starts recording when --record-before-media is set")
	flag.StringVar(&recordingStarterAuthUser, "recording-starter-auth-user", "", "Optional authenticated recording starter user that joins without publishing media")
	flag.StringVar(&recordingStarterAuthPassword, "recording-starter-auth-password", "", "Password for --recording-starter-auth-user")
	flag.StringVar(&recordingStarterName, "recording-starter-name", "", "Display name for separate recording starter logs")
	flag.StringVar(&recordingStatus, "recording-status", "1", "Nextcloud Talk recording status value")
	flag.Float64Var(&recordingTimeoutSec, "recording-timeout", 90, "Seconds to wait for recording to become active")
	flag.Float64Var(&roomEmptyGraceSec, "room-empty-grace-seconds", defaultRoomEmptyGrace.Seconds(), "Grace period before stopping after room empties")
	flag.StringVar(&muteStartFile, "mute-start-file", "", "Optional file path that must exist before audio mute rotation starts")
	flag.BoolVar(&insecure, "insecure", false, "Disable TLS verification")
	flag.Parse()

	if strings.TrimSpace(callURL) == "" {
		return errors.New("missing --call-url")
	}
	participantCount := len(videoFiles)
	if participantCount < 1 {
		return fmt.Errorf("--video must be passed at least once, got %d", participantCount)
	}
	if len(audioFiles) != participantCount {
		return fmt.Errorf("--audio must be passed exactly as many times as --video (%d), got %d", participantCount, len(audioFiles))
	}
	if rotateSeconds <= 0 {
		return errors.New("--rotate-seconds must be > 0")
	}
	if roomEmptyGraceSec < 0 {
		return errors.New("--room-empty-grace-seconds must be >= 0")
	}
	if durationSec < 0 {
		return errors.New("--duration must be >= 0")
	}
	if recordingTimeoutSec <= 0 {
		return errors.New("--recording-timeout must be > 0")
	}
	externalRecordingStarter := strings.TrimSpace(recordingStarterAuthUser) != "" || recordingStarterAuthPassword != ""
	if externalRecordingStarter && !recordBeforeMedia {
		return errors.New("--recording-starter-auth-user requires --record-before-media")
	}
	if externalRecordingStarter && (strings.TrimSpace(recordingStarterAuthUser) == "" || recordingStarterAuthPassword == "") {
		return errors.New("--recording-starter-auth-user and --recording-starter-auth-password must be provided together")
	}
	if recordBeforeMedia && !externalRecordingStarter {
		if recordingStarterIndex < 1 || recordingStarterIndex > participantCount {
			return fmt.Errorf("--recording-starter-index must be between 1 and participant count (%d)", participantCount)
		}
	}
	if len(names) > participantCount {
		return fmt.Errorf("--name count (%d) exceeds participant count (%d)", len(names), participantCount)
	}
	if len(joinDelaySecs) > participantCount {
		return fmt.Errorf("--join-delay count (%d) exceeds participant count (%d)", len(joinDelaySecs), participantCount)
	}
	if len(audioReadySec) > participantCount {
		return fmt.Errorf("--audio-ready-after count (%d) exceeds participant count (%d)", len(audioReadySec), participantCount)
	}
	if len(syncShiftSec) > participantCount {
		return fmt.Errorf("--sync-shift count (%d) exceeds participant count (%d)", len(syncShiftSec), participantCount)
	}
	if len(botDurationSec) > participantCount {
		return fmt.Errorf("--bot-duration count (%d) exceeds participant count (%d)", len(botDurationSec), participantCount)
	}
	if len(authUsers) > 0 || len(authPasswords) > 0 {
		if len(authUsers) != participantCount {
			return fmt.Errorf("--auth-user count (%d) must match participant count (%d)", len(authUsers), participantCount)
		}
		if len(authPasswords) != participantCount {
			return fmt.Errorf("--auth-password count (%d) must match participant count (%d)", len(authPasswords), participantCount)
		}
	}
	if recordBeforeMedia && !externalRecordingStarter && len(authUsers) == 0 {
		return errors.New("--record-before-media requires authenticated --auth-user/--auth-password credentials")
	}

	for _, path := range append(append([]string{}, videoFiles...), audioFiles...) {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("media file not found: %s", path)
		}
	}

	baseURL, roomToken, err := parseCallURL(callURL)
	if err != nil {
		return err
	}

	resolvedNames := make([]string, participantCount)
	for i := 0; i < participantCount; i++ {
		resolvedNames[i] = fmt.Sprintf("SongBot-%d", i+1)
	}
	for i := 0; i < len(names) && i < participantCount; i++ {
		if trimmed := strings.TrimSpace(names[i]); trimmed != "" {
			resolvedNames[i] = trimmed
		}
	}

	resolvedJoinDelays := make([]float64, participantCount)
	for i := 0; i < participantCount; i++ {
		resolvedJoinDelays[i] = float64(i * 4)
	}
	for i := 0; i < len(joinDelaySecs) && i < participantCount; i++ {
		if joinDelaySecs[i] < 0 {
			return fmt.Errorf("join delay must be >= 0 (idx=%d value=%.3f)", i, joinDelaySecs[i])
		}
		resolvedJoinDelays[i] = joinDelaySecs[i]
	}

	resolvedAudioReady := make([]float64, participantCount)
	for i := 0; i < len(audioReadySec) && i < participantCount; i++ {
		if audioReadySec[i] < 0 {
			return fmt.Errorf("audio-ready-after must be >= 0 (idx=%d value=%.3f)", i, audioReadySec[i])
		}
		resolvedAudioReady[i] = audioReadySec[i]
	}

	resolvedSyncShift := make([]float64, participantCount)
	for i := 0; i < len(syncShiftSec) && i < participantCount; i++ {
		resolvedSyncShift[i] = syncShiftSec[i]
	}

	var overrideDuration time.Duration
	if durationSec > 0 {
		overrideDuration = time.Duration(durationSec * float64(time.Second))
	}
	var mediaGate *mediaStartGate
	if recordBeforeMedia {
		mediaGate = newMediaStartGate()
	}
	resolvedDurations := make([]time.Duration, participantCount)
	for i := 0; i < participantCount; i++ {
		resolvedDurations[i] = overrideDuration
	}
	for i := 0; i < len(botDurationSec) && i < participantCount; i++ {
		if botDurationSec[i] < 0 {
			return fmt.Errorf("bot-duration must be >= 0 (idx=%d value=%.3f)", i, botDurationSec[i])
		}
		if botDurationSec[i] == 0 {
			resolvedDurations[i] = 0
			continue
		}
		resolvedDurations[i] = time.Duration(botDurationSec[i] * float64(time.Second))
	}

	cfgs := make([]*botConfig, 0, participantCount)
	for i := 0; i < participantCount; i++ {
		authUser := ""
		authPass := ""
		if len(authUsers) > 0 {
			authUser = strings.TrimSpace(authUsers[i])
			authPass = authPasswords[i]
		}
		cfgs = append(cfgs, &botConfig{
			Index:                i + 1,
			BaseURL:              baseURL,
			RoomToken:            roomToken,
			GuestName:            resolvedNames[i],
			VideoPath:            videoFiles[i],
			AudioPath:            audioFiles[i],
			JoinDelay:            time.Duration(resolvedJoinDelays[i] * float64(time.Second)),
			AudioReady:           time.Duration(resolvedAudioReady[i] * float64(time.Second)),
			SyncShift:            time.Duration(resolvedSyncShift[i] * float64(time.Second)),
			Duration:             resolvedDurations[i],
			Insecure:             insecure,
			AuthUser:             authUser,
			AuthPass:             authPass,
			RecordingStarter:     recordBeforeMedia && !externalRecordingStarter && i+1 == recordingStarterIndex,
			RecordingStatus:      recordingStatus,
			RecordingPollTimeout: time.Duration(recordingTimeoutSec * float64(time.Second)),
			MediaStartGate:       mediaGate,
			CallURLRaw:           callURL,
		})
	}

	log.Printf("[manager] call=%s", callURL)
	for _, cfg := range cfgs {
		durationLabel := "until EOF"
		if cfg.Duration > 0 {
			durationLabel = cfg.Duration.String()
		}
		authLabel := "guest"
		if cfg.AuthUser != "" {
			authLabel = cfg.AuthUser
		}
		log.Printf(
			"[manager] plan bot=%s auth=%s join=%s audio_ready=%s sync_shift=%s duration=%s video=%s audio=%s",
			cfg.GuestName,
			authLabel,
			cfg.JoinDelay,
			cfg.AudioReady,
			cfg.SyncShift,
			durationLabel,
			cfg.VideoPath,
			cfg.AudioPath,
		)
	}
	if recordBeforeMedia {
		if externalRecordingStarter {
			log.Printf("[manager] recording gate enabled external_starter=%s timeout=%s", recordingStarterAuthUser, time.Duration(recordingTimeoutSec*float64(time.Second)))
		} else {
			log.Printf("[manager] recording gate enabled starter=%d timeout=%s", recordingStarterIndex, time.Duration(recordingTimeoutSec*float64(time.Second)))
		}
	}
	log.Printf("[manager] rotating audible audio every %.2fs", rotateSeconds)
	if strings.TrimSpace(muteStartFile) != "" {
		log.Printf("[manager] mute rotation start file=%s", muteStartFile)
	}
	if stopWhenRoomEmpty {
		log.Printf("[manager] auto-stop when room empty enabled (grace=%s)", time.Duration(roomEmptyGraceSec*float64(time.Second)))
	}

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	var starterDone chan error
	if externalRecordingStarter {
		starterDone = make(chan error, 1)
		go func() {
			err := runRecordingOnlyStarter(ctx, recordingOnlyStarterConfig{
				BaseURL:              baseURL,
				RoomToken:            roomToken,
				DisplayName:          valueOr(recordingStarterName, recordingStarterAuthUser),
				AuthUser:             recordingStarterAuthUser,
				AuthPass:             recordingStarterAuthPassword,
				Insecure:             insecure,
				RecordingStatus:      recordingStatus,
				RecordingPollTimeout: time.Duration(recordingTimeoutSec * float64(time.Second)),
				MediaStartGate:       mediaGate,
			})
			if err != nil && !isContextDone(err) {
				mediaGate.open(err)
				cancel()
			}
			starterDone <- err
		}()
	}

	bots := make([]*bot, 0, len(cfgs))
	for _, cfg := range cfgs {
		bots = append(bots, newBot(cfg))
	}

	if stopWhenRoomEmpty {
		grace := time.Duration(roomEmptyGraceSec * float64(time.Second))
		if grace <= 0 {
			grace = defaultRoomEmptyGrace
		}
		go stopWhenAudienceGone(ctx, bots, grace, cancel)
	}

	rotateCtx, rotateCancel := context.WithCancel(ctx)
	defer rotateCancel()
	go rotateAudio(rotateCtx, bots, time.Duration(rotateSeconds*float64(time.Second)), muteRotationGate{File: strings.TrimSpace(muteStartFile)})

	type botResult struct {
		index int
		err   error
	}
	resultCh := make(chan botResult, len(bots))
	for i, b := range bots {
		go func(idx int, botInstance *bot) {
			resultCh <- botResult{index: idx, err: botInstance.run(ctx)}
		}(i, b)
	}

	failures := 0
	for range bots {
		res := <-resultCh
		if res.err != nil {
			failures++
			log.Printf("[manager] bot %d failed: %v", res.index+1, res.err)
		}
	}
	rotateCancel()
	cancel()
	if starterDone != nil {
		if err := <-starterDone; err != nil && !isContextDone(err) {
			failures++
			log.Printf("[manager] recording starter failed: %v", err)
		}
	}

	if failures > 0 {
		return fmt.Errorf("%d bot(s) failed", failures)
	}
	log.Printf("[manager] all bots completed")
	return nil
}
