package signaling

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// defaultWriteTimeout bounds every websocket write so a peer that
	// stops reading cannot block senders (and cleanup behind them) forever.
	defaultWriteTimeout = 10 * time.Second
	// defaultPingInterval/defaultPongWait implement client-side keepalive:
	// without it a half-open connection (e.g. after a NAT idle timeout)
	// would silently stop delivering membership and offer events forever.
	defaultPingInterval = 20 * time.Second
	defaultPongWait     = 45 * time.Second
	// The standalone signaling server keeps a disconnected session alive
	// for ~30s; the resume retry schedule is sized to fit that window.
	defaultResumeAttempts     = 4
	defaultResumeBackoff      = time.Second
	defaultResumeDialTimeout  = 5 * time.Second
	defaultResumeHelloTimeout = 5 * time.Second
)

// errSessionExpired marks a resume the server rejected with code
// "no_such_session": the session is gone and retrying cannot help.
var errSessionExpired = errors.New("signaling session expired (no_such_session)")

// ErrNotConnected marks a send attempted while no connection is installed —
// most often the resume window, where clearConn has dropped the old conn and
// the redial is still in flight. It is exported so callers can tell "the socket
// is momentarily away, and the very same session is likely to come back" from a
// real send failure, and hold their retry budget accordingly (D-509).
var ErrNotConnected = errors.New("signaling websocket not connected")

type response struct {
	msg map[string]any
	err error
}

type Client struct {
	wsURL    string
	insecure bool

	// Keepalive and resume tuning; fixed after NewClient (tests shrink them).
	writeTimeout       time.Duration
	pingInterval       time.Duration
	pongWait           time.Duration
	resumeAttempts     int
	resumeBackoff      time.Duration
	resumeDialTimeout  time.Duration
	resumeHelloTimeout time.Duration

	// connMu guards conn, connReady and dialed. conn is nil while a
	// session resume is in flight; connReady is then an open channel
	// that is closed once a usable connection is installed again.
	connMu    sync.Mutex
	conn      *websocket.Conn
	connReady chan struct{}
	dialed    bool

	// writeMu serializes data writes; gorilla/websocket allows at most
	// one concurrent writer. Close and WriteControl intentionally do not
	// take it: both are documented as safe alongside writers, and taking
	// it would queue shutdown behind a write stuck on a dead peer.
	writeMu sync.Mutex

	events chan map[string]any
	done   chan struct{}

	nextID int64

	mu      sync.Mutex
	pending map[string]chan response
	closed  bool
	// Session resume state sniffed from hello responses; the server hello
	// response carries both the granted protocol version and the resumeid.
	helloVersion string
	resumeID     string
}

func NewClient(wsURL string, insecure bool) *Client {
	return &Client{
		wsURL:              wsURL,
		insecure:           insecure,
		writeTimeout:       defaultWriteTimeout,
		pingInterval:       defaultPingInterval,
		pongWait:           defaultPongWait,
		resumeAttempts:     defaultResumeAttempts,
		resumeBackoff:      defaultResumeBackoff,
		resumeDialTimeout:  defaultResumeDialTimeout,
		resumeHelloTimeout: defaultResumeHelloTimeout,
		connReady:          make(chan struct{}),
		events:             make(chan map[string]any, 128),
		done:               make(chan struct{}),
		pending:            make(map[string]chan response),
	}
}

func (c *Client) Connect(ctx context.Context) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return fmt.Errorf("connect signaling websocket: %w", err)
	}

	c.connMu.Lock()
	c.conn = conn
	c.dialed = true
	close(c.connReady)
	c.connMu.Unlock()

	go c.receiver(conn)
	go c.pinger()
	return nil
}

func (c *Client) dial(ctx context.Context) (*websocket.Conn, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		Proxy:            http.ProxyFromEnvironment,
	}
	if c.insecure {
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	conn, _, err := dialer.DialContext(ctx, c.wsURL, nil)
	return conn, err
}

func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	for _, ch := range c.pending {
		ch <- response{err: errors.New("signaling client closed")}
		close(ch)
	}
	c.pending = map[string]chan response{}
	c.mu.Unlock()

	close(c.done)

	c.connMu.Lock()
	conn := c.conn
	c.conn = nil
	c.connMu.Unlock()
	if conn == nil {
		return nil
	}
	// Close without writeMu: gorilla/websocket documents Close as safe
	// concurrently with writers, and it unblocks a writer stuck on a
	// peer that stopped reading instead of queueing behind it.
	return conn.Close()
}

func (c *Client) Send(payload map[string]any) error {
	if !c.isDialed() {
		return ErrNotConnected
	}
	return c.writeJSON(payload)
}

func (c *Client) Request(ctx context.Context, payload map[string]any, timeout time.Duration) (map[string]any, error) {
	if !c.isDialed() {
		return nil, ErrNotConnected
	}

	id := fmt.Sprintf("%d", atomic.AddInt64(&c.nextID, 1))
	req := cloneMap(payload)
	req["id"] = id

	respCh := make(chan response, 1)
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

func (c *Client) NextEvent(ctx context.Context, timeout time.Duration) (map[string]any, error) {
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

// receiver owns the read side of the connection. Network read errors are
// recovered via the protocol's session resume flow, so events keep flowing
// transparently across reconnects; only resume failures (or errors before
// the first hello completed) surface as a _connection_error event.
func (c *Client) receiver(conn *websocket.Conn) {
	for {
		recoverable, err := c.readLoop(conn)
		if c.isClosed() {
			return
		}
		if !recoverable {
			c.emitConnectionError(err)
			return
		}

		next, rerr := c.resumeSession(conn, err)
		if rerr != nil {
			c.emitConnectionError(rerr)
			return
		}
		if next == nil {
			// Client closed while the resume was in flight.
			return
		}
		conn = next
	}
}

func (c *Client) readLoop(conn *websocket.Conn) (recoverable bool, err error) {
	_ = conn.SetReadDeadline(time.Now().Add(c.pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(c.pongWait))
	})

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return true, fmt.Errorf("read signaling message: %w", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(c.pongWait))

		var msg map[string]any
		if err := json.Unmarshal(payload, &msg); err != nil {
			return false, fmt.Errorf("decode signaling message: %w", err)
		}

		c.captureResumeState(msg)

		if !c.dispatch(msg) {
			return false, errors.New("signaling client closed")
		}
	}
}

// dispatch routes a message to the matching pending request, falling back
// to the events channel. It returns false once the client is closed.
func (c *Client) dispatch(msg map[string]any) bool {
	id := asString(msg["id"])
	if id != "" {
		c.mu.Lock()
		ch, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.mu.Unlock()
		if ok {
			ch <- response{msg: msg}
			close(ch)
			return true
		}
	}

	select {
	case <-c.done:
		return false
	case c.events <- msg:
		return true
	}
}

// captureResumeState records the protocol version and resume id from hello
// responses flowing through the receiver, so a dropped connection can be
// resumed (the server keeps disconnected sessions alive for ~30s).
func (c *Client) captureResumeState(msg map[string]any) {
	if asString(msg["type"]) != "hello" {
		return
	}
	hello := asMap(msg["hello"])
	c.mu.Lock()
	if v := asString(hello["version"]); v != "" {
		c.helloVersion = v
	}
	if rid := asString(hello["resumeid"]); rid != "" {
		c.resumeID = rid
	}
	c.mu.Unlock()
}

// resumeSession redials the signaling server and resumes the existing
// session via {type:hello, hello:{version, resumeid}}. It returns the new
// connection on success, (nil, nil) if the client closed meanwhile, and an
// error once retries are exhausted or the server reports the session gone.
func (c *Client) resumeSession(old *websocket.Conn, cause error) (*websocket.Conn, error) {
	c.clearConn(old)

	c.mu.Lock()
	version := c.helloVersion
	resumeID := c.resumeID
	c.mu.Unlock()
	if resumeID == "" {
		// No hello completed yet, so there is no session to resume.
		return nil, cause
	}

	log.Printf("signaling connection lost (%v); resuming session", cause)
	lastErr := cause
	for attempt := 1; attempt <= c.resumeAttempts; attempt++ {
		if attempt > 1 {
			backoff := time.NewTimer(time.Duration(attempt-1) * c.resumeBackoff)
			select {
			case <-c.done:
				backoff.Stop()
				return nil, nil
			case <-backoff.C:
			}
		}

		conn, err := c.redial()
		if err != nil {
			lastErr = err
			log.Printf("signaling resume dial failed (attempt %d/%d): %v", attempt, c.resumeAttempts, err)
			continue
		}

		if err := c.resumeHello(conn, version, resumeID); err != nil {
			_ = conn.Close()
			if errors.Is(err, errSessionExpired) {
				return nil, err
			}
			lastErr = err
			log.Printf("signaling resume hello failed (attempt %d/%d): %v", attempt, c.resumeAttempts, err)
			continue
		}

		if !c.installConn(conn) {
			_ = conn.Close()
			return nil, nil
		}
		log.Printf("signaling session resumed (attempt %d/%d)", attempt, c.resumeAttempts)
		return conn, nil
	}

	return nil, fmt.Errorf("signaling session resume failed after %d attempts: %w", c.resumeAttempts, lastErr)
}

func (c *Client) redial() (*websocket.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.resumeDialTimeout)
	defer cancel()
	go func() {
		select {
		case <-c.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return c.dial(ctx)
}

// resumeHello performs the session resume handshake on a fresh connection.
// The connection is not installed yet, so reading and writing here cannot
// race with other goroutines.
func (c *Client) resumeHello(conn *websocket.Conn, version, resumeID string) error {
	id := fmt.Sprintf("%d", atomic.AddInt64(&c.nextID, 1))
	req := map[string]any{
		"id":   id,
		"type": "hello",
		"hello": map[string]any{
			"version":  version,
			"resumeid": resumeID,
		},
	}

	deadline := time.Now().Add(c.resumeHelloTimeout)
	_ = conn.SetWriteDeadline(deadline)
	if err := conn.WriteJSON(req); err != nil {
		return fmt.Errorf("send resume hello: %w", err)
	}

	_ = conn.SetReadDeadline(deadline)
	for {
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			return fmt.Errorf("read resume hello response: %w", err)
		}
		if asString(msg["id"]) != id {
			// Not the resume response (e.g. a welcome message); keep waiting.
			continue
		}
		switch asString(msg["type"]) {
		case "hello":
			// Refresh resume state; the server may rotate the resume id.
			c.captureResumeState(msg)
			return nil
		case "error":
			if asString(asMap(msg["error"])["code"]) == "no_such_session" {
				return errSessionExpired
			}
			return fmt.Errorf("resume hello rejected: %v", msg)
		default:
			return fmt.Errorf("resume hello returned type=%s", asString(msg["type"]))
		}
	}
}

// pinger sends websocket pings so a half-open connection (e.g. a dropped
// NAT mapping) surfaces as a read deadline error on the receiver instead
// of hanging the event stream forever.
func (c *Client) pinger() {
	ticker := time.NewTicker(c.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
		}

		c.connMu.Lock()
		conn := c.conn
		c.connMu.Unlock()
		if conn == nil {
			continue
		}
		// WriteControl is safe concurrently with other writers; a lost
		// ping shows up as a read deadline error on the receiver side.
		_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(c.writeTimeout))
	}
}

func (c *Client) emitConnectionError(err error) {
	select {
	case <-c.done:
		return
	case c.events <- map[string]any{
		"type":  "_connection_error",
		"error": err.Error(),
	}:
	}
}

func (c *Client) writeJSON(payload any) error {
	deadline := time.Now().Add(c.writeTimeout)
	for {
		c.connMu.Lock()
		conn := c.conn
		ready := c.connReady
		c.connMu.Unlock()

		if conn != nil {
			c.writeMu.Lock()
			_ = conn.SetWriteDeadline(deadline)
			err := conn.WriteJSON(payload)
			c.writeMu.Unlock()
			if err == nil {
				return nil
			}
			if c.currentConn() == conn {
				return fmt.Errorf("write signaling message: %w", err)
			}
			// The connection was replaced by a session resume mid-write;
			// retry on the new one below.
			continue
		}

		// No usable connection (session resume in flight): wait for it
		// within the write budget instead of failing senders immediately.
		wait := time.Until(deadline)
		if wait <= 0 {
			return errors.New("signaling write timed out waiting for session resume")
		}
		t := time.NewTimer(wait)
		select {
		case <-c.done:
			t.Stop()
			return errors.New("signaling client closed")
		case <-t.C:
			return errors.New("signaling write timed out waiting for session resume")
		case <-ready:
			t.Stop()
		}
	}
}

// clearConn drops the given connection if it is still current, leaving an
// open connReady channel for writers to wait on during the resume.
func (c *Client) clearConn(old *websocket.Conn) {
	c.connMu.Lock()
	if c.conn == old {
		c.conn = nil
		c.connReady = make(chan struct{})
	}
	c.connMu.Unlock()
	_ = old.Close()
}

// installConn publishes a resumed connection and releases waiting writers.
// It refuses the swap when the client closed during the resume.
func (c *Client) installConn(conn *websocket.Conn) bool {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	select {
	case <-c.done:
		return false
	default:
	}
	c.conn = conn
	close(c.connReady)
	return true
}

func (c *Client) currentConn() *websocket.Conn {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.conn
}

func (c *Client) isDialed() bool {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.dialed
}

func (c *Client) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
