package storage

import (
	"sync"
)

// boundedDirCache is a thread-safe cache with a maximum capacity.
// When capacity is exceeded, it clears entries to prevent unbounded memory growth
// during long worker runs while deduplicating directory creation calls.
type boundedDirCache struct {
	mu     sync.RWMutex
	m      map[string]bool
	maxCap int
}

func newBoundedDirCache(maxCap int) *boundedDirCache {
	if maxCap <= 0 {
		maxCap = 5000
	}
	return &boundedDirCache{
		m:      make(map[string]bool, 1024),
		maxCap: maxCap,
	}
}

func (c *boundedDirCache) Contains(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.m[key]
}

func (c *boundedDirCache) Add(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.maxCap {
		c.m = make(map[string]bool, 1024)
	}
	c.m[key] = true
}
