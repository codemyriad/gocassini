package signaling

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newTestServer starts a websocket server whose handler is invoked with a
// 1-based connection index, so tests can script reconnect scenarios.
func newTestServer(t *testing.T, handle func(connIndex int64, conn *websocket.Conn)) string {
	t.Helper()

	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}
	var connIndex int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		handle(atomic.AddInt64(&connIndex, 1), conn)
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

// serveHello answers a single client hello request with a server hello
// response carrying the given session and resume ids.
func serveHello(conn *websocket.Conn, sessionID, resumeID string) error {
	var msg map[string]any
	if err := conn.ReadJSON(&msg); err != nil {
		return err
	}
	hello := map[string]any{
		"version":   "2.0",
		"sessionid": sessionID,
	}
	if resumeID != "" {
		hello["resumeid"] = resumeID
	}
	return conn.WriteJSON(map[string]any{
		"id":    msg["id"],
		"type":  "hello",
		"hello": hello,
	})
}

// clientHello drives the hello handshake from the client side the same way
// the recorder does (a plain Request that the server answers by id).
func clientHello(t *testing.T, ctx context.Context, client *Client) {
	t.Helper()

	resp, err := client.Request(ctx, map[string]any{
		"type": "hello",
		"hello": map[string]any{
			"version": "2.0",
			"auth":    map[string]any{},
		},
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("hello request: %v", err)
	}
	if got := asString(resp["type"]); got != "hello" {
		t.Fatalf("hello response type = %q, want hello", got)
	}
}

func TestClientConcurrentSend(t *testing.T) {
	t.Parallel()

	received := make(chan struct{}, 2048)
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			var payload map[string]any
			if err := conn.ReadJSON(&payload); err != nil {
				return
			}
			received <- struct{}{}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client := NewClient(wsURL, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	const workers = 16
	const perWorker = 40
	want := workers * perWorker

	var wg sync.WaitGroup
	errCh := make(chan error, want)
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if err := client.Send(map[string]any{
					"type":   "test",
					"worker": worker,
					"idx":    i,
				}); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("send failed: %v", err)
		}
	}

	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()

	got := 0
	for got < want {
		select {
		case <-received:
			got++
		case <-timeout.C:
			t.Fatalf("received %d/%d websocket messages", got, want)
		}
	}
}

func TestClientResumesSessionAfterDrop(t *testing.T) {
	t.Parallel()

	resumeReq := make(chan map[string]any, 1)
	wsURL := newTestServer(t, func(connIndex int64, conn *websocket.Conn) {
		switch connIndex {
		case 1:
			if err := serveHello(conn, "sess-1", "resume-1"); err != nil {
				return
			}
			// Drop the TCP connection without a close frame to simulate
			// a network failure.
			conn.Close()
		case 2:
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			resumeReq <- msg
			_ = conn.WriteJSON(map[string]any{
				"id":   msg["id"],
				"type": "hello",
				"hello": map[string]any{
					"version":   "2.0",
					"sessionid": "sess-1",
				},
			})
			_ = conn.WriteJSON(map[string]any{
				"type":  "event",
				"event": map[string]any{"marker": "after-resume"},
			})
			// Keep the connection open until the client goes away.
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}
	})

	client := NewClient(wsURL, false)
	client.resumeBackoff = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	clientHello(t, ctx, client)

	ev, err := client.NextEvent(ctx, 10*time.Second)
	if err != nil {
		t.Fatalf("next event: %v", err)
	}
	if got := asString(ev["type"]); got != "event" {
		t.Fatalf("event type = %q (%v), want event", got, ev)
	}
	if got := asString(asMap(ev["event"])["marker"]); got != "after-resume" {
		t.Fatalf("event marker = %q, want after-resume", got)
	}

	select {
	case msg := <-resumeReq:
		hello := asMap(msg["hello"])
		if got := asString(hello["resumeid"]); got != "resume-1" {
			t.Fatalf("resume hello resumeid = %q, want resume-1", got)
		}
		if got := asString(hello["version"]); got != "2.0" {
			t.Fatalf("resume hello version = %q, want 2.0", got)
		}
	default:
		t.Fatal("server never received a resume hello")
	}
}

func TestClientReportsConnectionErrorWhenResumeExpires(t *testing.T) {
	t.Parallel()

	wsURL := newTestServer(t, func(connIndex int64, conn *websocket.Conn) {
		switch connIndex {
		case 1:
			if err := serveHello(conn, "sess-1", "resume-1"); err != nil {
				return
			}
			conn.Close()
		case 2:
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			_ = conn.WriteJSON(map[string]any{
				"id":   msg["id"],
				"type": "error",
				"error": map[string]any{
					"code":    "no_such_session",
					"message": "the session does not exist",
				},
			})
		}
	})

	client := NewClient(wsURL, false)
	client.resumeBackoff = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	clientHello(t, ctx, client)

	ev, err := client.NextEvent(ctx, 10*time.Second)
	if err != nil {
		t.Fatalf("next event: %v", err)
	}
	if got := asString(ev["type"]); got != "_connection_error" {
		t.Fatalf("event type = %q (%v), want _connection_error", got, ev)
	}
	if msg := asString(ev["error"]); !strings.Contains(msg, "no_such_session") {
		t.Fatalf("connection error %q does not mention no_such_session", msg)
	}
}

func TestClientSendsKeepalivePings(t *testing.T) {
	t.Parallel()

	pings := make(chan struct{}, 64)
	wsURL := newTestServer(t, func(_ int64, conn *websocket.Conn) {
		conn.SetPingHandler(func(appData string) error {
			pings <- struct{}{}
			return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	client := NewClient(wsURL, false)
	client.pingInterval = 50 * time.Millisecond
	client.pongWait = 250 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	// Wait for enough pings to prove the connection outlived pongWait,
	// i.e. the pong handler kept refreshing the read deadline.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for i := 0; i < 8; i++ {
		select {
		case <-pings:
		case <-deadline.C:
			t.Fatalf("received %d/8 keepalive pings", i)
		}
	}

	if _, err := client.NextEvent(ctx, 100*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("next event err = %v, want deadline exceeded (healthy idle connection)", err)
	}
}

func TestClientDetectsHalfOpenConnection(t *testing.T) {
	t.Parallel()

	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	wsURL := newTestServer(t, func(_ int64, _ *websocket.Conn) {
		// Never read: pings get no pong, like a dead NAT mapping.
		<-hold
	})

	client := NewClient(wsURL, false)
	client.pingInterval = 50 * time.Millisecond
	client.pongWait = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	// No hello was exchanged, so there is no session to resume and the
	// expired read deadline must surface as a connection error.
	ev, err := client.NextEvent(ctx, 5*time.Second)
	if err != nil {
		t.Fatalf("next event: %v", err)
	}
	if got := asString(ev["type"]); got != "_connection_error" {
		t.Fatalf("event type = %q (%v), want _connection_error", got, ev)
	}
}

func TestClientWriteDeadlineUnblocksStuckWriter(t *testing.T) {
	t.Parallel()

	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	wsURL := newTestServer(t, func(_ int64, _ *websocket.Conn) {
		// Never read so the kernel socket buffers fill up and writes block.
		<-hold
	})

	client := NewClient(wsURL, false)
	client.writeTimeout = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	writeErr := make(chan error, 1)
	go func() {
		blob := strings.Repeat("x", 256*1024)
		for {
			if err := client.Send(map[string]any{"type": "test", "blob": blob}); err != nil {
				writeErr <- err
				return
			}
		}
	}()

	select {
	case err := <-writeErr:
		if err == nil {
			t.Fatal("expected a write error")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("blocked write never hit its deadline")
	}

	// Close must not queue behind writers.
	start := time.Now()
	if err := client.Close(); err != nil && !strings.Contains(err.Error(), "closed") {
		t.Logf("close: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("close took %s, want <1s", elapsed)
	}
}
