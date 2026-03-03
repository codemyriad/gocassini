package signaling

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type response struct {
	msg map[string]any
	err error
}

type Client struct {
	wsURL    string
	insecure bool

	conn *websocket.Conn

	events chan map[string]any
	done   chan struct{}

	nextID int64

	mu      sync.Mutex
	pending map[string]chan response
	closed  bool
}

func NewClient(wsURL string, insecure bool) *Client {
	return &Client{
		wsURL:    wsURL,
		insecure: insecure,
		events:   make(chan map[string]any, 128),
		done:     make(chan struct{}),
		pending:  make(map[string]chan response),
	}
}

func (c *Client) Connect(ctx context.Context) error {
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
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) Send(payload map[string]any) error {
	if c.conn == nil {
		return errors.New("signaling websocket not connected")
	}
	return c.conn.WriteJSON(payload)
}

func (c *Client) Request(ctx context.Context, payload map[string]any, timeout time.Duration) (map[string]any, error) {
	if c.conn == nil {
		return nil, errors.New("signaling websocket not connected")
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

	if err := c.conn.WriteJSON(req); err != nil {
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

func (c *Client) receiver() {
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
				ch <- response{msg: msg}
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
