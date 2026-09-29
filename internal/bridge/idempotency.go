package bridge

import (
	"container/list"
	"sync"
	"time"
)

// idempotencyCache is a small, bounded, TTL'd cache mapping (route, key) to
// a previously-produced HTTP response body/status. It protects
// externally-triggered mutation endpoints (message/task/artifact/finding
// creation) from producing duplicate logical records when a client retries
// after a network failure.
type idempotencyCache struct {
	mu       sync.Mutex
	ttl      time.Duration
	maxItems int
	order    *list.List
	items    map[string]*list.Element
}

type idempotencyEntry struct {
	key       string
	status    int
	body      []byte
	expiresAt time.Time
}

func newIdempotencyCache(ttl time.Duration, maxItems int) *idempotencyCache {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if maxItems <= 0 {
		maxItems = 10000
	}
	return &idempotencyCache{
		ttl:      ttl,
		maxItems: maxItems,
		order:    list.New(),
		items:    make(map[string]*list.Element),
	}
}

func (c *idempotencyCache) get(route, key string) (int, []byte, bool) {
	if key == "" {
		return 0, nil, false
	}
	full := route + "\x00" + key
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[full]
	if !ok {
		return 0, nil, false
	}
	entry := el.Value.(*idempotencyEntry)
	if time.Now().After(entry.expiresAt) {
		c.order.Remove(el)
		delete(c.items, full)
		return 0, nil, false
	}
	return entry.status, entry.body, true
}

func (c *idempotencyCache) put(route, key string, status int, body []byte) {
	if key == "" {
		return
	}
	full := route + "\x00" + key
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[full]; ok {
		c.order.Remove(el)
		delete(c.items, full)
	}
	entry := &idempotencyEntry{key: full, status: status, body: body, expiresAt: time.Now().Add(c.ttl)}
	el := c.order.PushFront(entry)
	c.items[full] = el

	for c.order.Len() > c.maxItems {
		back := c.order.Back()
		if back == nil {
			break
		}
		c.order.Remove(back)
		delete(c.items, back.Value.(*idempotencyEntry).key)
	}
}
