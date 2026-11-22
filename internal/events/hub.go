// Package events provides a thread-safe, non-blocking pub/sub hub implementing models.EventHub
// for real-time WebSocket telemetry, terminal chunks, and router trace broadcasts.
package events

import (
	"context"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

const defaultSubscriberBuffer = 512

type subscriber struct {
	id   uint64
	ch   chan models.WSEvent
	done chan struct{}
}

// Hub implements models.EventHub with non-blocking fan-out and leak-free cancellation.
type Hub struct {
	mu     sync.RWMutex
	nextID uint64
	subs   map[uint64]*subscriber
}

var _ models.EventHub = (*Hub)(nil)

// NewHub creates a new thread-safe non-blocking models.EventHub.
func NewHub() models.EventHub {
	return &Hub{
		subs: make(map[uint64]*subscriber),
	}
}

// Publish broadcasts a WSEvent to all active subscribers without blocking slow consumers.
func (h *Hub) Publish(event models.WSEvent) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, sub := range h.subs {
		select {
		case sub.ch <- event:
		default:
			// Slow consumer buffer is full; drop oldest unread frame and enqueue fresh telemetry.
			select {
			case <-sub.ch:
			default:
			}
			select {
			case sub.ch <- event:
			default:
			}
		}
	}
}

// Subscribe registers a new event listener bound to ctx and returns a read-only channel
// alongside an idempotent unsubscribe function.
func (h *Hub) Subscribe(ctx context.Context) (<-chan models.WSEvent, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		closed := make(chan models.WSEvent)
		close(closed)
		return closed, func() {}
	}

	sub := &subscriber{
		ch:   make(chan models.WSEvent, defaultSubscriberBuffer),
		done: make(chan struct{}),
	}

	h.mu.Lock()
	h.nextID++
	sub.id = h.nextID
	h.subs[sub.id] = sub
	h.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			close(sub.done)
			h.mu.Lock()
			if _, ok := h.subs[sub.id]; ok {
				delete(h.subs, sub.id)
				close(sub.ch)
			}
			h.mu.Unlock()
		})
	}

	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				unsubscribe()
			case <-sub.done:
			}
		}()
	}

	return sub.ch, unsubscribe
}

// ClientCount returns the number of currently active subscribers.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
