package storage

import "testing"

func TestBoundedDirCacheKeepsExistingKeyAtCapacity(t *testing.T) {
	cache := newBoundedDirCache(2)
	cache.Add("first")
	cache.Add("second")
	cache.Add("first")
	cache.Add("third")
	if !cache.Contains("first") || cache.Contains("second") || !cache.Contains("third") {
		t.Fatal("cache did not evict the least recently used key")
	}
}
