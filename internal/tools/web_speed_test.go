package tools

import (
	"testing"
	"time"
)

// The web caches are pure accelerators: same key within TTL is a hit, expired
// entries fall out, and the map never grows past its cap.

func TestWebFetchCacheRoundTripAndExpiry(t *testing.T) {
	key := "https://example.com/doc|65536"
	if _, ok := webFetchCacheGet(key); ok {
		t.Fatalf("unexpected pre-existing cache entry")
	}
	webFetchCachePut(key, Result{Status: StatusOK, Output: "cached body", Meta: map[string]string{"url": "https://example.com/doc"}})
	got, ok := webFetchCacheGet(key)
	if !ok || got.Output != "cached body" {
		t.Fatalf("expected cache hit, got ok=%v output=%q", ok, got.Output)
	}

	// Expired entries must miss and be evicted.
	webFetchCache.Lock()
	entry := webFetchCache.entries[key]
	entry.expires = time.Now().Add(-time.Second)
	webFetchCache.entries[key] = entry
	webFetchCache.Unlock()
	if _, ok := webFetchCacheGet(key); ok {
		t.Fatalf("expired entry should miss")
	}
}

func TestWebFetchCacheHonorsCap(t *testing.T) {
	webFetchCache.Lock()
	webFetchCache.entries = map[string]webFetchCacheEntry{}
	webFetchCache.Unlock()
	for i := 0; i < webFetchCacheMax*2; i++ {
		webFetchCachePut(string(rune('a'+i%26))+string(rune('0'+i/26)), Result{Status: StatusOK})
	}
	webFetchCache.Lock()
	n := len(webFetchCache.entries)
	webFetchCache.Unlock()
	if n > webFetchCacheMax {
		t.Fatalf("cache grew past cap: %d > %d", n, webFetchCacheMax)
	}
}

func TestWebFetchCacheHitDoesNotMutateStoredMeta(t *testing.T) {
	key := "https://example.com/meta|65536"
	webFetchCachePut(key, Result{Status: StatusOK, Meta: map[string]string{"url": "u"}})
	got, _ := webFetchCacheGet(key)
	clone := cloneWebFetchMeta(got.Meta)
	clone["cache"] = "hit"
	stored, _ := webFetchCacheGet(key)
	if _, tainted := stored.Meta["cache"]; tainted {
		t.Fatalf("stored entry meta was mutated by a hit")
	}
}

func TestWebSearchCacheServesRepeatQueries(t *testing.T) {
	webSearchCache.Lock()
	webSearchCache.entries = map[string]webSearchCacheEntry{}
	webSearchCache.Unlock()

	key := "golang generics|5"
	if _, ok := webSearchCacheGet(key); ok {
		t.Fatalf("unexpected pre-existing entry")
	}
	webSearchCachePut(key, []searchResult{{Title: "Go", URL: "https://go.dev"}})
	got, ok := webSearchCacheGet(key)
	if !ok || len(got) != 1 || got[0].URL != "https://go.dev" {
		t.Fatalf("expected hit with stored result, got ok=%v results=%v", ok, got)
	}
	// The returned slice is a copy: mutating it must not corrupt the cache.
	got[0].URL = "https://evil.example"
	again, _ := webSearchCacheGet(key)
	if again[0].URL != "https://go.dev" {
		t.Fatalf("cache entry mutated through returned slice")
	}
}
