package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Event struct {
	Sequence uint64    `json:"sequence"`
	Kind     string    `json:"kind"`
	Subject  string    `json:"subject"`
	Digest   string    `json:"digest"`
	Previous string    `json:"previous,omitempty"`
	At       time.Time `json:"at"`
}
type Chain struct {
	mu     sync.RWMutex
	events []Event
	root   string
}

func New() *Chain { return &Chain{} }
func (c *Chain) Append(kind, subject string, payload any, at time.Time) (Event, error) {
	if kind == "" || subject == "" {
		return Event{}, errors.New("kind and subject are required")
	}
	digestInput := []byte(fmt.Sprintf("%T", payload))
	sum := sha256.Sum256(digestInput)
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := c.root
	event := Event{Sequence: uint64(len(c.events) + 1), Kind: kind, Subject: subject, Digest: hex.EncodeToString(sum[:]), Previous: previous, At: at.UTC()}
	eventRaw, _ := json.Marshal(event)
	eventSum := sha256.Sum256(append([]byte(previous), eventRaw...))
	c.root = hex.EncodeToString(eventSum[:])
	c.events = append(c.events, event)
	return event, nil
}
func (c *Chain) Root() string { c.mu.RLock(); defer c.mu.RUnlock(); return c.root }
func (c *Chain) Events() []Event {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]Event(nil), c.events...)
}
func (c *Chain) Verify() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	previous := ""
	for i, event := range c.events {
		if event.Sequence != uint64(i+1) || event.Previous != previous {
			return fmt.Errorf("provenance chain broken at sequence %d", event.Sequence)
		}
		raw, _ := json.Marshal(event)
		sum := sha256.Sum256(append([]byte(previous), raw...))
		previous = hex.EncodeToString(sum[:])
	}
	if previous != c.root {
		return errors.New("provenance root mismatch")
	}
	return nil
}
