package tv

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type thumbnail struct {
	id   string
	data []byte
	etag string
}

// thumbCache is an LRU of thumbnails by content id. Concurrent misses for
// one id share a fetch, and sem bounds how many D2D transfers run at once.
type thumbCache struct {
	mu    sync.Mutex
	max   int
	ll    *list.List
	items map[string]*list.Element
	// misses holds ids the TV has no thumbnail for, until the time given.
	misses map[string]time.Time

	group singleflight.Group
	sem   chan struct{}
}

func newThumbCache(size, concurrency int) *thumbCache {
	return &thumbCache{
		max:    size,
		ll:     list.New(),
		items:  map[string]*list.Element{},
		misses: map[string]time.Time{},
		sem:    make(chan struct{}, concurrency),
	}
}

func (c *thumbCache) get(id string) (thumbnail, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[id]
	if !ok {
		return thumbnail{}, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(thumbnail), true
}

func (c *thumbCache) put(id string, data []byte) thumbnail {
	sum := sha256.Sum256(data)
	t := thumbnail{id: id, data: data, etag: `"` + hex.EncodeToString(sum[:12]) + `"`}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[id]; ok {
		el.Value = t
		c.ll.MoveToFront(el)
		return t
	}
	c.items[id] = c.ll.PushFront(t)
	for c.ll.Len() > c.max {
		last := c.ll.Back()
		c.ll.Remove(last)
		delete(c.items, last.Value.(thumbnail).id)
	}
	return t
}

func (c *thumbCache) missed(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.misses[id]
	if ok && time.Now().After(until) {
		delete(c.misses, id)
		return false
	}
	return ok
}

func (c *thumbCache) miss(id string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.misses[id] = time.Now().Add(ttl)
}

func (c *thumbCache) remove(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.misses, id)
	if el, ok := c.items[id]; ok {
		c.ll.Remove(el)
		delete(c.items, id)
	}
}
