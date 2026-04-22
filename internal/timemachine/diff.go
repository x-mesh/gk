package timemachine

import (
	"container/list"
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/x-mesh/gk/internal/git"
)

// DiffStat returns the `git show --stat` summary for the given OID. The
// output is trimmed — leading/trailing blank lines are stripped so callers
// can embed it directly in TUI panels without extra gaps.
func DiffStat(ctx context.Context, r git.Runner, oid string) (string, error) {
	if oid == "" {
		return "", fmt.Errorf("DiffStat: empty oid")
	}
	out, stderr, err := r.Run(ctx, "show", "--stat", "--format=", oid)
	if err != nil {
		return "", fmt.Errorf("git show --stat %s: %s: %w",
			oid, strings.TrimSpace(string(stderr)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// DiffPatch returns the full patch (`git show --patch --stat` output).
func DiffPatch(ctx context.Context, r git.Runner, oid string) (string, error) {
	if oid == "" {
		return "", fmt.Errorf("DiffPatch: empty oid")
	}
	out, stderr, err := r.Run(ctx, "show", "--patch", "--stat", "--format=", oid)
	if err != nil {
		return "", fmt.Errorf("git show --patch %s: %s: %w",
			oid, strings.TrimSpace(string(stderr)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// DiffCache wraps DiffStat / DiffPatch with an LRU cache of recent results.
// Used by the TUI diff pane to keep keystroke latency low while the user
// arrows through the list.
//
// Keyed by (mode, oid) so stat and patch caches never collide. The default
// capacity is 64; Set a larger value at construction if the TUI list is
// expected to exceed that viewport in active use.
type DiffCache struct {
	mu       sync.Mutex
	capacity int
	order    *list.List               // front = most recently used
	entries  map[string]*list.Element // key -> list node
	runner   git.Runner
}

type diffCacheEntry struct {
	key   string
	value string
}

// NewDiffCache returns a DiffCache bound to the given runner. capacity <= 0
// falls back to 64.
func NewDiffCache(r git.Runner, capacity int) *DiffCache {
	if capacity <= 0 {
		capacity = 64
	}
	return &DiffCache{
		capacity: capacity,
		order:    list.New(),
		entries:  make(map[string]*list.Element, capacity),
		runner:   r,
	}
}

// Stat returns the cached --stat output or fetches + caches it.
func (c *DiffCache) Stat(ctx context.Context, oid string) (string, error) {
	return c.fetch(ctx, "stat", oid, DiffStat)
}

// Patch returns the cached --patch output or fetches + caches it.
func (c *DiffCache) Patch(ctx context.Context, oid string) (string, error) {
	return c.fetch(ctx, "patch", oid, DiffPatch)
}

// Len returns the current number of cached entries. Intended for tests.
func (c *DiffCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

func (c *DiffCache) fetch(
	ctx context.Context,
	mode, oid string,
	fetch func(context.Context, git.Runner, string) (string, error),
) (string, error) {
	key := mode + ":" + oid

	c.mu.Lock()
	if elem, ok := c.entries[key]; ok {
		c.order.MoveToFront(elem)
		v := elem.Value.(*diffCacheEntry).value
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	// Fetch outside the lock — git subprocess may take tens of ms.
	v, err := fetch(ctx, c.runner, oid)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// Re-check: another goroutine may have populated it while we fetched.
	if elem, ok := c.entries[key]; ok {
		c.order.MoveToFront(elem)
		return elem.Value.(*diffCacheEntry).value, nil
	}
	elem := c.order.PushFront(&diffCacheEntry{key: key, value: v})
	c.entries[key] = elem
	for c.order.Len() > c.capacity {
		tail := c.order.Back()
		if tail == nil {
			break
		}
		c.order.Remove(tail)
		delete(c.entries, tail.Value.(*diffCacheEntry).key)
	}
	return v, nil
}
