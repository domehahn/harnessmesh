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
// after a network failure - including when the retry races the original,
// still-in-flight request: beginOrWait reserves the key for exactly one
// caller and makes every concurrent caller with the same key wait for that
// one execution's result, rather than letting both execute the handler.
type idempotencyCache struct {
	mu       sync.Mutex
	ttl      time.Duration
	maxItems int
	order    *list.List
	items    map[string]*list.Element
	inflight map[string]chan struct{}
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
		inflight: make(map[string]chan struct{}),
	}
}

// beginOrWait resolves a request for (route, key). If a completed result
// already exists, it is returned immediately (done=true). If another
// goroutine is currently producing a result for the same key, this blocks
// until that goroutine finishes and then returns its result. Otherwise, the
// caller becomes the sole owner for this key (done=false) and must call
// finish or abort exactly once.
func (c *idempotencyCache) beginOrWait(route, key string) (status int, body []byte, done bool) {
	full := route + "\x00" + key
	for {
		c.mu.Lock()
		if el, exists := c.items[full]; exists {
			entry := el.Value.(*idempotencyEntry)
			if time.Now().Before(entry.expiresAt) {
				c.mu.Unlock()
				return entry.status, entry.body, true
			}
			c.order.Remove(el)
			delete(c.items, full)
		}
		if ch, pending := c.inflight[full]; pending {
			c.mu.Unlock()
			<-ch
			continue
		}
		c.inflight[full] = make(chan struct{})
		c.mu.Unlock()
		return 0, nil, false
	}
}

// finish records the result for (route, key) and releases any waiters.
func (c *idempotencyCache) finish(route, key string, status int, body []byte) {
	full := route + "\x00" + key
	c.mu.Lock()
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
	ch := c.inflight[full]
	delete(c.inflight, full)
	c.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// abort releases the reservation for (route, key) without caching a result,
// so a retry (or a waiter) re-executes the handler instead of replaying a
// failed attempt or blocking forever.
func (c *idempotencyCache) abort(route, key string) {
	full := route + "\x00" + key
	c.mu.Lock()
	ch := c.inflight[full]
	delete(c.inflight, full)
	c.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}
