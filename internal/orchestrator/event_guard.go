package orchestrator

import (
	"sync"
	"time"
)

// EventDeduper provides in-memory idempotency guards for spot events.
// It is safe for concurrent use.
type EventDeduper struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]time.Time
}

// NewEventDeduper creates a new deduper with the given TTL.
func NewEventDeduper(ttl time.Duration) *EventDeduper {
	return &EventDeduper{
		ttl:     ttl,
		entries: make(map[string]time.Time),
	}
}

// CheckAndMark returns true if the key was seen recently (duplicate).
// Otherwise, it records the key and returns false.
func (d *EventDeduper) CheckAndMark(key string, now time.Time) bool {
	if d == nil {
		return false
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// Cleanup expired entries on each call to keep memory bounded.
	for k, expiresAt := range d.entries {
		if !now.Before(expiresAt) {
			delete(d.entries, k)
		}
	}

	if expiresAt, ok := d.entries[key]; ok && now.Before(expiresAt) {
		return true
	}

	d.entries[key] = now.Add(d.ttl)
	return false
}
