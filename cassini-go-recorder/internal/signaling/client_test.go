package signaling

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

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
