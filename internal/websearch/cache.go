package websearch

import (
	"sync"
	"time"
)

// maxCacheEntries bounds the in-process result cache. Search results are cheap
// to recompute, so the cache only needs to absorb bursts.
const maxCacheEntries = 512

// cache is a TTL cache of normalized responses. A non-positive TTL disables it.
type cache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]cacheEntry
}

type cacheEntry struct {
	stored   time.Time
	response Response
}

func newCache(ttl time.Duration) *cache {
	if ttl < 0 {
		ttl = 0
	}
	return &cache{ttl: ttl, entries: make(map[string]cacheEntry)}
}

func (c *cache) get(key string) (Response, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return Response{}, false
	}
	if c.expired(entry, time.Now()) {
		delete(c.entries, key)
		return Response{}, false
	}
	return entry.response, true
}

func (c *cache) set(key string, response Response) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxCacheEntries {
		c.evictLocked()
	}
	c.entries[key] = cacheEntry{stored: time.Now(), response: response}
}

// evictLocked drops expired entries, then the oldest entry if the cache is
// still at capacity. It runs under the cache lock.
func (c *cache) evictLocked() {
	now := time.Now()
	oldestKey := ""
	var oldestAt time.Time
	for key, entry := range c.entries {
		if c.expired(entry, now) {
			delete(c.entries, key)
			continue
		}
		if oldestKey == "" || entry.stored.Before(oldestAt) {
			oldestKey, oldestAt = key, entry.stored
		}
	}
	if len(c.entries) >= maxCacheEntries && oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

func (c *cache) expired(entry cacheEntry, now time.Time) bool {
	return c.ttl <= 0 || now.Sub(entry.stored) > c.ttl
}
