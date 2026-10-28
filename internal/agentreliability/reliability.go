// Package agentreliability provides deterministic runtime protection for
// agent, provider and plugin calls: circuit breaking, quarantine and bounded
// recovery state. It contains no provider-specific execution code.
package agentreliability

import (
	"errors"
	"sync"
	"time"
)

var ErrCircuitOpen = errors.New("agent circuit breaker is open")

type State string

const (
	Closed      State = "closed"
	Open        State = "open"
	HalfOpen    State = "half_open"
	Quarantined State = "quarantined"
)

type Config struct {
	FailureThreshold   int
	RecoveryTimeout    time.Duration
	QuarantineAfter    int
	QuarantineDuration time.Duration
}
type Snapshot struct {
	Name             string    `json:"name"`
	State            State     `json:"state"`
	Failures         int       `json:"failures"`
	Successes        int       `json:"successes"`
	QuarantinedUntil time.Time `json:"quarantined_until,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
}

type Controller struct {
	mu    sync.Mutex
	cfg   Config
	items map[string]*Snapshot
}

func New(cfg Config) *Controller {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 3
	}
	if cfg.RecoveryTimeout <= 0 {
		cfg.RecoveryTimeout = 30 * time.Second
	}
	if cfg.QuarantineAfter <= 0 {
		cfg.QuarantineAfter = cfg.FailureThreshold * 2
	}
	if cfg.QuarantineDuration <= 0 {
		cfg.QuarantineDuration = 5 * time.Minute
	}
	return &Controller{cfg: cfg, items: map[string]*Snapshot{}}
}

func (c *Controller) get(name string) *Snapshot {
	item := c.items[name]
	if item == nil {
		item = &Snapshot{Name: name, State: Closed}
		c.items[name] = item
	}
	return item
}
func (c *Controller) Before(name string, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	item := c.get(name)
	if item.State == Quarantined && now.Before(item.QuarantinedUntil) {
		return ErrCircuitOpen
	}
	if item.State == Quarantined {
		item.State = Open
	}
	if item.State == Open {
		if item.QuarantinedUntil.IsZero() || now.Sub(item.QuarantinedUntil) < c.cfg.RecoveryTimeout {
			return ErrCircuitOpen
		}
		item.State = HalfOpen
	}
	return nil
}
func (c *Controller) Success(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	item := c.get(name)
	item.Successes++
	item.Failures = 0
	item.LastError = ""
	item.State = Closed
	item.QuarantinedUntil = time.Time{}
}
func (c *Controller) Failure(name string, err error, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	item := c.get(name)
	item.Failures++
	if err != nil {
		item.LastError = err.Error()
	}
	if item.Failures >= c.cfg.QuarantineAfter {
		item.State = Quarantined
		item.QuarantinedUntil = now.Add(c.cfg.QuarantineDuration)
	} else if item.Failures >= c.cfg.FailureThreshold {
		item.State = Open
		item.QuarantinedUntil = now
	}
}
func (c *Controller) Snapshot(name string) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return *c.get(name)
}
func (c *Controller) All() []Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]Snapshot, 0, len(c.items))
	for _, item := range c.items {
		result = append(result, *item)
	}
	return result
}
