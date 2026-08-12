package storage

import "testing"

func TestBoundedDirCacheKeepsExistingKeyAtCapacity(t *testing.T) {
	cache := newBoundedDirCache(2)
	cache.Add("first")
	cache.Add("second")
	cache.Add("first")
	if !cache.Contains("first") || !cache.Contains("second") {
		t.Fatal("adding an existing key cleared a full cache")
	}

	cache.Add("third")
	if cache.Contains("first") || cache.Contains("second") || !cache.Contains("third") {
		t.Fatal("cache did not clear only when a new key exceeded capacity")
	}
}
