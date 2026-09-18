package collaboration

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

// EventHandler is a callback invoked when a collaboration event matches.
type EventHandler func(ctx context.Context, event *protocol.CollaborationEvent)

// EventBus coordinates publishing, coalescing, and dispatching events across spaces.
type EventBus struct {
	store     store.Store
	mu        sync.RWMutex
	listeners map[string][]EventHandler // spaceID -> handlers

	// Sliding window coalescer
	coalesceMu      sync.Mutex
	coalesceWindows map[string]*coalesceBucket // key -> bucket
	coalescePeriod  time.Duration
}

type coalesceBucket struct {
	timer     *time.Timer
	spaceID   string
	source    string
	files     map[string]struct{}
	payload   map[string]any
	onTrigger func(*protocol.CollaborationEvent)
}

func NewEventBus(st store.Store, coalescePeriod time.Duration) *EventBus {
	if coalescePeriod <= 0 {
		coalescePeriod = 150 * time.Millisecond
	}
	return &EventBus{
		store:           st,
		listeners:       make(map[string][]EventHandler),
		coalesceWindows: make(map[string]*coalesceBucket),
		coalescePeriod:  coalescePeriod,
	}
}

// AddListener registers an in-memory event handler for a space.
func (eb *EventBus) AddListener(spaceID string, h EventHandler) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.listeners[spaceID] = append(eb.listeners[spaceID], h)
}

// Publish broadcasts an event to subscribers and persists to store.
func (eb *EventBus) Publish(ctx context.Context, event *protocol.CollaborationEvent) error {
	if event.ID == "" {
		event.ID = fmt.Sprintf("evt_%d", time.Now().UnixNano())
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	// Persist to store if store is present
	if eb.store != nil && event.SpaceID != "" {
		_ = eb.store.EmitEvent(ctx, event.SpaceID, event.Type, event.Payload)
	}

	eb.mu.RLock()
	handlers := append([]EventHandler(nil), eb.listeners[event.SpaceID]...)
	globalHandlers := append([]EventHandler(nil), eb.listeners["*"]...)
	eb.mu.RUnlock()

	for _, h := range handlers {
		h(ctx, event)
	}
	for _, h := range globalHandlers {
		h(ctx, event)
	}

	return nil
}

// PublishRepoChange submits a repository file change with sliding-window coalescing.
// Rapid bursts of file changes (e.g. 20 edits) are coalesced into a single
// repository.changed event containing the deduplicated set of modified files.
func (eb *EventBus) PublishRepoChange(ctx context.Context, spaceID, source, file string, payload map[string]any) {
	eb.coalesceMu.Lock()
	defer eb.coalesceMu.Unlock()

	bucketKey := fmt.Sprintf("%s:%s", spaceID, source)
	bucket, exists := eb.coalesceWindows[bucketKey]
	if !exists {
		bucket = &coalesceBucket{
			spaceID: spaceID,
			source:  source,
			files:   make(map[string]struct{}),
			payload: make(map[string]any),
		}
		bucket.files[file] = struct{}{}
		for k, v := range payload {
			bucket.payload[k] = v
		}

		bucket.timer = time.AfterFunc(eb.coalescePeriod, func() {
			eb.flushCoalesceBucket(bucketKey)
		})
		eb.coalesceWindows[bucketKey] = bucket
		return
	}

	// Add file to existing window and reset timer (sliding window)
	bucket.files[file] = struct{}{}
	for k, v := range payload {
		bucket.payload[k] = v
	}
	bucket.timer.Reset(eb.coalescePeriod)
}

// FlushCoalesce manually flushes any pending coalescing window for a space.
func (eb *EventBus) FlushCoalesce(spaceID, source string) {
	eb.coalesceMu.Lock()
	defer eb.coalesceMu.Unlock()
	bucketKey := fmt.Sprintf("%s:%s", spaceID, source)
	eb.flushCoalesceBucket(bucketKey)
}

func (eb *EventBus) flushCoalesceBucket(bucketKey string) {
	bucket, exists := eb.coalesceWindows[bucketKey]
	if !exists {
		return
	}
	if bucket.timer != nil {
		bucket.timer.Stop()
	}
	delete(eb.coalesceWindows, bucketKey)

	var files []string
	for f := range bucket.files {
		files = append(files, f)
	}
	sort.Strings(files)

	evt := &protocol.CollaborationEvent{
		ID:        fmt.Sprintf("evt_coalesced_%d", time.Now().UnixNano()),
		SpaceID:   bucket.spaceID,
		Type:      protocol.EventRepoChanged,
		Source:    bucket.source,
		Timestamp: time.Now().UTC(),
		Scope:     files,
		Payload:   bucket.payload,
	}

	_ = eb.Publish(context.Background(), evt)
}

// MatchesSubscription determines if an event matches a participant subscription.
func MatchesSubscription(sub *protocol.Subscription, event *protocol.CollaborationEvent, channelID string) bool {
	if sub.SpaceID != event.SpaceID && sub.SpaceID != "*" {
		return false
	}

	// Channel filter
	if len(sub.Channels) > 0 && channelID != "" {
		channelMatched := false
		for _, ch := range sub.Channels {
			if ch == "*" || ch == channelID {
				channelMatched = true
				break
			}
		}
		if !channelMatched {
			return false
		}
	}

	// Event type filter
	if len(sub.EventTypes) > 0 {
		typeMatched := false
		for _, pattern := range sub.EventTypes {
			if pattern == "*" || pattern == event.Type {
				typeMatched = true
				break
			}
			if strings.HasSuffix(pattern, ".*") {
				prefix := strings.TrimSuffix(pattern, ".*")
				if strings.HasPrefix(event.Type, prefix+".") {
					typeMatched = true
					break
				}
			}
		}
		if !typeMatched {
			return false
		}
	}

	// Scope pattern filter
	if len(sub.ScopePatterns) > 0 && len(event.Scope) > 0 {
		scopeMatched := false
		for _, file := range event.Scope {
			for _, pat := range sub.ScopePatterns {
				if pat == "*" || pat == "**" {
					scopeMatched = true
					break
				}
				if matched, _ := filepath.Match(pat, file); matched {
					scopeMatched = true
					break
				}
				// Also handle standard prefix matching like internal/**
				if strings.HasSuffix(pat, "/**") {
					prefix := strings.TrimSuffix(pat, "/**")
					if strings.HasPrefix(file, prefix+"/") || file == prefix {
						scopeMatched = true
						break
					}
				}
			}
			if scopeMatched {
				break
			}
		}
		if !scopeMatched {
			return false
		}
	}

	return true
}
