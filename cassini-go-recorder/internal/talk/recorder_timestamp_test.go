package talk

import (
	"testing"
	"time"
)

func TestReadWithArrivalTimeTimestampsAfterBlockingRead(t *testing.T) {
	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	type result struct {
		value      int
		receivedAt time.Time
		err        error
	}
	resultCh := make(chan result, 1)

	go func() {
		value, _, receivedAt, err := readWithArrivalTime(func() (int, struct{}, error) {
			close(readStarted)
			<-releaseRead
			return 42, struct{}{}, nil
		})
		resultCh <- result{value: value, receivedAt: receivedAt, err: err}
	}()

	<-readStarted
	notBefore := time.Now()
	close(releaseRead)

	got := <-resultCh
	if got.err != nil {
		t.Fatalf("read: %v", got.err)
	}
	if got.value != 42 {
		t.Fatalf("value = %d, want 42", got.value)
	}
	if got.receivedAt.Before(notBefore) {
		t.Fatalf("receive timestamp %v was captured before the blocked read completed at %v", got.receivedAt, notBefore)
	}
}
