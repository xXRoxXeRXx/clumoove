package storage

import (
	"sync"
)

// boundedDirCache is a thread-safe LRU cache with a maximum capacity. It evicts
// only the least recently used entry to retain hot directory creation hints.
type boundedDirCache struct {
	mu     sync.RWMutex
	m      map[string]bool
	order  []string
	index  map[string]int
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
		order:  make([]string, 0, 1024),
		index:  make(map[string]int, 1024),
		maxCap: maxCap,
	}
}

func (c *boundedDirCache) Contains(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.m[key] {
		return false
	}
	c.touch(key)
	return true
}

func (c *boundedDirCache) Add(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m[key] {
		c.touch(key)
		return
	}
	if len(c.m) >= c.maxCap {
		oldest := c.order[0]
		delete(c.m, oldest)
		delete(c.index, oldest)
		c.order = c.order[1:]
		for i, entry := range c.order {
			c.index[entry] = i
		}
	}
	c.m[key] = true
	c.index[key] = len(c.order)
	c.order = append(c.order, key)
}

func (c *boundedDirCache) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	position, exists := c.index[key]
	if !exists {
		return
	}
	delete(c.m, key)
	delete(c.index, key)
	copy(c.order[position:], c.order[position+1:])
	c.order = c.order[:len(c.order)-1]
	for i := position; i < len(c.order); i++ {
		c.index[c.order[i]] = i
	}
}

func (c *boundedDirCache) touch(key string) {
	position := c.index[key]
	if position == len(c.order)-1 {
		return
	}
	copy(c.order[position:], c.order[position+1:])
	c.order[len(c.order)-1] = key
	for i := position; i < len(c.order); i++ {
		c.index[c.order[i]] = i
	}
}
