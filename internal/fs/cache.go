package fs

import (
	"context"
	"sync"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/meta"
)

// CachingRepository wraps a ResourceRepository and caches results in
// memory with a fixed TTL. ResourceDefinitions and ListResources hits
// are served from cache on re-access; GetResource and UpdateResource
// always pass through (file content is already cached per inode).
// UpdateResource invalidates the list-cache entry for the updated
// (node, namespace, type) tuple so the next read reflects the change.
type CachingRepository struct {
	inner ResourceRepository
	ttl   time.Duration

	mu      sync.Mutex
	rdCache map[string]rdEntry    // node → defs entry
	lrCache map[lrKey]lrEntry     // (node,ns,type) → items entry
}

type rdEntry struct {
	defs   []*meta.ResourceDefinition
	expiry time.Time
}

type lrKey struct{ node, namespace, resourceType string }

type lrEntry struct {
	items  []resource.Resource
	expiry time.Time
}

// NewCachingRepository wraps inner with a cache whose entries expire
// after ttl. A ttl of zero disables caching (every call passes through).
func NewCachingRepository(inner ResourceRepository, ttl time.Duration) *CachingRepository {
	return &CachingRepository{
		inner:   inner,
		ttl:     ttl,
		rdCache: make(map[string]rdEntry),
		lrCache: make(map[lrKey]lrEntry),
	}
}

func (c *CachingRepository) ResourceDefinitions(ctx context.Context, node string) ([]*meta.ResourceDefinition, error) {
	if c.ttl > 0 {
		c.mu.Lock()
		if e, ok := c.rdCache[node]; ok && time.Now().Before(e.expiry) {
			out := make([]*meta.ResourceDefinition, len(e.defs))
			copy(out, e.defs)
			c.mu.Unlock()
			return out, nil
		}
		c.mu.Unlock()
	}

	defs, err := c.inner.ResourceDefinitions(ctx, node)
	if err != nil {
		return nil, err
	}

	if c.ttl > 0 {
		c.mu.Lock()
		c.rdCache[node] = rdEntry{defs: defs, expiry: time.Now().Add(c.ttl)}
		c.mu.Unlock()
	}

	return defs, nil
}

func (c *CachingRepository) ListResources(ctx context.Context, node, namespace, resourceType string) ([]resource.Resource, error) {
	k := lrKey{node, namespace, resourceType}

	if c.ttl > 0 {
		c.mu.Lock()
		if e, ok := c.lrCache[k]; ok && time.Now().Before(e.expiry) {
			out := make([]resource.Resource, len(e.items))
			copy(out, e.items)
			c.mu.Unlock()
			return out, nil
		}
		c.mu.Unlock()
	}

	items, err := c.inner.ListResources(ctx, node, namespace, resourceType)
	if err != nil {
		return nil, err
	}

	if c.ttl > 0 {
		c.mu.Lock()
		c.lrCache[k] = lrEntry{items: items, expiry: time.Now().Add(c.ttl)}
		c.mu.Unlock()
	}

	return items, nil
}

func (c *CachingRepository) GetResource(ctx context.Context, node, namespace, resourceType, id string) (resource.Resource, error) {
	return c.inner.GetResource(ctx, node, namespace, resourceType, id)
}

func (c *CachingRepository) UpdateResource(ctx context.Context, node string, rsrc resource.Resource) error {
	if err := c.inner.UpdateResource(ctx, node, rsrc); err != nil {
		return err
	}
	// Invalidate so the next list reflects the write.
	k := lrKey{node, string(rsrc.Metadata().Namespace()), string(rsrc.Metadata().Type())}
	c.mu.Lock()
	delete(c.lrCache, k)
	c.mu.Unlock()
	return nil
}

// Prefetch pre-warms the ResourceDefinitions cache for all given nodes.
// Calls run in parallel; individual errors are silently dropped (the
// cache simply misses on first FUSE access for that node).
func (c *CachingRepository) Prefetch(ctx context.Context, nodes []string) {
	var wg sync.WaitGroup
	for _, node := range nodes {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			_, _ = c.ResourceDefinitions(ctx, n)
		}(node)
	}
	wg.Wait()
}
