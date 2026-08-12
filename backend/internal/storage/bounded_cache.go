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

// boundedStringCache is a thread-safe string cache with a fixed upper bound.
// It is deliberately small and clears as a unit once full: these caches are
// performance hints, so retaining unbounded user-controlled paths is worse
// than occasionally repeating a provider lookup.
type boundedStringCache struct {
	mu     sync.RWMutex
	m      map[string]string
	maxCap int
}

func newBoundedStringCache(maxCap int) *boundedStringCache {
	if maxCap <= 0 {
		maxCap = 5000
	}
	return &boundedStringCache{m: make(map[string]string, 1024), maxCap: maxCap}
}

func (c *boundedStringCache) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	value, ok := c.m[key]
	return value, ok
}

func (c *boundedStringCache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.m[key]; !exists && len(c.m) >= c.maxCap {
		c.m = make(map[string]string, 1024)
	}
	c.m[key] = value
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
	if c.m[key] {
		return
	}
	if len(c.m) >= c.maxCap {
		c.m = make(map[string]bool, 1024)
	}
	c.m[key] = true
}
