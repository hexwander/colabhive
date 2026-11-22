package events

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

func TestHubPubSubAndNonBlockingSlowSubscriber(t *testing.T) {
	hub := NewHub()
	if hub.ClientCount() != 0 {
		t.Fatalf("expected 0 clients initially, got %d", hub.ClientCount())
	}

	ctxFast, cancelFast := context.WithCancel(context.Background())
	defer cancelFast()
	fastCh, unsubFast := hub.Subscribe(ctxFast)
	defer unsubFast()

	// Slow subscriber that never reads during burst
	slowCh, unsubSlow := hub.Subscribe(context.Background())
	defer unsubSlow()

	if hub.ClientCount() != 2 {
		t.Fatalf("expected 2 clients, got %d", hub.ClientCount())
	}

	// Publish more events than buffer capacity to verify non-blocking behavior
	total := defaultSubscriberBuffer + 128
	done := make(chan struct{})
	go func() {
		for i := 0; i < total; i++ {
			hub.Publish(models.WSEvent{
				Type:    models.WSEventHeartbeat,
				Payload: i,
			})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on slow subscriber")
	}

	ev := <-fastCh
	if ev.Type != models.WSEventHeartbeat {
		t.Fatalf("unexpected event type %q", ev.Type)
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("expected non-zero auto-populated timestamp")
	}

	// Slow subscriber should still have buffered events and close cleanly on unsub
	if len(slowCh) == 0 {
		t.Fatal("expected buffered events on slow subscriber")
	}
	unsubSlow()
	unsubSlow() // Idempotency check
	if hub.ClientCount() != 1 {
		t.Fatalf("expected 1 client after unsubSlow, got %d", hub.ClientCount())
	}

	cancelFast()
	deadline := time.Now().Add(time.Second)
	for hub.ClientCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if hub.ClientCount() != 0 {
		t.Fatalf("expected 0 clients after context cancel, got %d", hub.ClientCount())
	}
}

func TestHubConcurrentRaceSafety(t *testing.T) {
	hub := NewHub()
	var wg sync.WaitGroup

	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				ch, unsub := hub.Subscribe(ctx)
				hub.Publish(models.WSEvent{Type: models.WSEventQuotaUpdated, Payload: worker})
				select {
				case <-ch:
				default:
				}
				if i%2 == 0 {
					unsub()
				}
				cancel()
			}
		}(w)
	}

	wg.Wait()
}
