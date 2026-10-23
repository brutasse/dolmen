package s3

import (
	"container/list"
	"strings"
	"sync"
)

// readCache bounds cached object bodies by bytes, not by entry count: gist
// objects range from a few hundred bytes to the 16 MiB save limit, so a
// count limit would let the budget swing by four orders of magnitude.
//
// Entries are never expired. A cached body is only ever served after the
// backend confirms its ETag, so entry age says nothing about correctness and
// a clock here would only add a window of silence about changed objects.
// Entries leave the cache by eviction, by failing that check, or by a write
// or delete through this process.
type readCache struct {
	mu      sync.Mutex
	maxByte int64
	used    int64
	order   *list.List // front = most recently used
	entries map[string]*list.Element
}

// cacheEntry is one object body. value is shared with every caller that reads
// the same key concurrently, so it must never be modified in place.
type cacheEntry struct {
	key   string
	value []byte
	etag  string
}

// entryOverhead approximates what an entry costs beyond its bytes: map slot,
// list node, the entry itself, and the string and slice headers. The cache
// bounds its own bookkeeping, not the allocator's, so an estimate is enough.
const entryOverhead = 96

func newReadCache(maxBytes int64) *readCache {
	return &readCache{maxByte: maxBytes, order: list.New(), entries: map[string]*list.Element{}}
}

// enabled reports whether anything can be stored. A nil cache is valid and
// inert, which keeps a Client built as a literal from needing a constructor.
func (c *readCache) enabled() bool { return c != nil && c.maxByte > 0 }

func (e *cacheEntry) weight() int64 {
	return int64(len(e.value)) + int64(len(e.key)) + entryOverhead
}

// get returns a cached body and the ETag it was stored with.
func (c *readCache) get(key string) (value []byte, etag string, ok bool) {
	if !c.enabled() {
		return nil, "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, "", false
	}
	c.order.MoveToFront(el)
	e := el.Value.(*cacheEntry)
	return e.value, e.etag, true
}

// put stores a body, evicting least-recently-used entries to stay inside the
// budget. A body too large for the whole budget is not worth caching and is
// dropped rather than allowed to evict everything else.
func (c *readCache) put(key string, value []byte, etag string) {
	if !c.enabled() {
		return
	}
	e := &cacheEntry{key: key, value: value, etag: etag}
	if e.weight() > c.maxByte {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.used -= el.Value.(*cacheEntry).weight()
		el.Value = e
		c.order.MoveToFront(el)
	} else {
		c.entries[key] = c.order.PushFront(e)
	}
	c.used += e.weight()
	for c.used > c.maxByte {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		c.removeLocked(oldest)
	}
}

func (c *readCache) invalidate(key string) {
	if !c.enabled() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.removeLocked(el)
	}
}

func (c *readCache) removeLocked(el *list.Element) {
	delete(c.entries, el.Value.(*cacheEntry).key)
	c.used -= el.Value.(*cacheEntry).weight()
	c.order.Remove(el)
}

// Cache keys are namespaced so a version's entry can never be read as the
// current object's, and so a listing's singleflight key cannot collide with
// an object key that happens to share its spelling.
func objectCacheKey(key string) string       { return "o:" + key }
func versionCacheKey(key, vid string) string { return "v:" + key + "|" + vid }
func listingCacheKey(key string) string      { return "s:" + key }

// unquoteETag strips the quotes both GetObject and ListObjectsV2 wrap around
// ETags. Comparison is always against the stripped form.
func unquoteETag(etag string) string { return strings.Trim(etag, `"`) }
