package s3

import (
	"strings"
	"testing"
)

// entryWeight mirrors cacheEntry.weight so tests can size budgets exactly.
func entryWeight(key, value string) int64 { return int64(len(key) + len(value) + entryOverhead) }

func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := newReadCache(2 * entryWeight("k1", "value"))
	c.put("k1", []byte("value"), "e1")
	c.put("k2", []byte("value"), "e2")
	// Reading k1 makes k2 the least recently used of the two.
	if _, _, ok := c.get("k1"); !ok {
		t.Fatal("k1 gone before the budget was reached")
	}
	c.put("k3", []byte("value"), "e3")

	if _, _, ok := c.get("k2"); ok {
		t.Error("k2 was least recently used and should have been evicted")
	}
	for _, key := range []string{"k1", "k3"} {
		if _, _, ok := c.get(key); !ok {
			t.Errorf("%s missing", key)
		}
	}
}

func TestCacheKeepsEntryUnderBudget(t *testing.T) {
	c := newReadCache(entryWeight("k1", "small"))
	c.put("k1", []byte(strings.Repeat("x", 1000)), "big")
	if _, _, ok := c.get("k1"); ok {
		t.Error("a value larger than the whole budget must not be cached")
	}
	// The rejected value must not have been charged against the budget.
	c.put("k1", []byte("small"), "e1")
	if _, etag, ok := c.get("k1"); !ok || etag != "e1" {
		t.Errorf("cache unusable after an oversized put: ok=%v etag=%q", ok, etag)
	}
}

func TestCacheRecountsReplacedEntry(t *testing.T) {
	c := newReadCache(400)
	c.put("k", []byte(strings.Repeat("a", 200)), "e1")
	c.put("k", []byte("b"), "e2")

	// k now costs 100 bytes, so three more 98-byte entries still fit. A budget
	// still charged for the 200-byte value would have evicted them.
	for _, key := range []string{"a", "b", "c"} {
		c.put(key, []byte("x"), "e")
	}
	for _, key := range []string{"k", "a", "b", "c"} {
		if _, _, ok := c.get(key); !ok {
			t.Errorf("%s missing after a shrink-and-refill", key)
		}
	}
}

func TestCacheInvalidate(t *testing.T) {
	c := newReadCache(1 << 20)
	c.put("k", []byte("value"), "e1")
	c.invalidate("k")
	if _, _, ok := c.get("k"); ok {
		t.Error("entry survived invalidate")
	}
	c.invalidate("never-stored") // must not panic
}

func TestDisabledCacheIsInert(t *testing.T) {
	for name, c := range map[string]*readCache{"nil": nil, "zero budget": newReadCache(0)} {
		c.put("k", []byte("value"), "e1")
		if _, _, ok := c.get("k"); ok {
			t.Errorf("%s: stored into a disabled cache", name)
		}
		c.invalidate("k")
	}
}
