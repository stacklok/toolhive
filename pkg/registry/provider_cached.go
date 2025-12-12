package registry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/adrg/xdg"
	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"

	types "github.com/stacklok/toolhive/pkg/registry/registry"
)

const (
	// Cache configuration (hardcoded to avoid config pollution)
	defaultCacheTTL       = 1 * time.Hour
	maxCacheFileSize      = 10 * 1024 * 1024   // 10MB per cache file
	maxCacheAge           = 7 * 24 * time.Hour // Delete caches older than 7 days
	maxTotalCacheSize     = 50 * 1024 * 1024   // 50MB total cache directory
	persistentCacheSubdir = "cache"
)

// CachedAPIRegistryProvider wraps APIRegistryProvider with caching support.
// Provides both in-memory and optional persistent file caching.
// Works for both CLI (with persistent cache) and API server (memory only).
type CachedAPIRegistryProvider struct {
	*APIRegistryProvider

	// In-memory cache
	cacheMu    sync.RWMutex
	cachedData *types.Registry
	cacheTime  time.Time

	// Cache configuration
	cacheTTL      time.Duration
	usePersistent bool
	cacheFile     string
}

// NewCachedAPIRegistryProvider creates a new cached API registry provider.
// If usePersistent is true, it will use a file cache in ~/.toolhive/cache/
// The validation happens in NewAPIRegistryProvider by actually trying to use the API.
func NewCachedAPIRegistryProvider(apiURL string, allowPrivateIp bool, usePersistent bool) (*CachedAPIRegistryProvider, error) {
	base, err := NewAPIRegistryProvider(apiURL, allowPrivateIp)
	if err != nil {
		return nil, err
	}

	cached := &CachedAPIRegistryProvider{
		APIRegistryProvider: base,
		cacheTTL:            defaultCacheTTL,
		usePersistent:       usePersistent,
	}

	// CRITICAL: Override the BaseProvider's GetRegistryFunc to use our cached version
	// Without this, BaseProvider.ListServers() will call the uncached APIRegistryProvider.GetRegistry()
	// which hits the API and does expensive conversion on every call
	cached.GetRegistryFunc = cached.GetRegistry

	if usePersistent {
		// Generate cache file path based on API URL hash
		hash := sha256.Sum256([]byte(apiURL))
		cacheFile, err := xdg.CacheFile(fmt.Sprintf("toolhive/%s/registry-%x.json", persistentCacheSubdir, hash[:4]))
		if err != nil {
			return nil, fmt.Errorf("failed to get cache file path: %w", err)
		}
		cached.cacheFile = cacheFile

		// Clean up old caches
		cached.cleanupOldCaches()

		// Try to load from disk
		if err := cached.loadFromDisk(); err != nil {
			// Not a fatal error, just means we'll fetch from API
			_ = err
		}
	}

	return cached, nil
}

// GetRegistry returns the registry data, using cache if valid.
// Falls back to stale cache if API is unavailable.
func (p *CachedAPIRegistryProvider) GetRegistry() (*types.Registry, error) {
	p.cacheMu.RLock()

	// Check if cache is valid (not expired)
	if p.cachedData != nil && time.Since(p.cacheTime) < p.cacheTTL {
		defer p.cacheMu.RUnlock()
		return p.cachedData, nil
	}
	p.cacheMu.RUnlock()

	// Cache expired or missing, fetch fresh data
	return p.refreshCache()
}

// refreshCache fetches fresh data from the API and updates the cache.
// If the API fetch fails, returns stale cache if available.
func (p *CachedAPIRegistryProvider) refreshCache() (*types.Registry, error) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()

	// Fetch from API
	registry, err := p.APIRegistryProvider.GetRegistry()
	if err != nil {
		// If fetch fails and we have stale cache, return it
		if p.cachedData != nil {
			return p.cachedData, nil
		}
		return nil, err
	}

	// Update in-memory cache
	p.cachedData = registry
	p.cacheTime = time.Now()

	// Persist to disk if enabled
	if p.usePersistent {
		if err := p.saveToDisk(registry); err != nil {
			// Log error but don't fail - cache save is non-critical
			_ = err
		}
	}

	return registry, nil
}

// ForceRefresh forces a cache refresh, ignoring TTL.
func (p *CachedAPIRegistryProvider) ForceRefresh() error {
	_, err := p.refreshCache()
	return err
}

// GetServer returns a specific server by name (overrides base to use cache).
func (p *CachedAPIRegistryProvider) GetServer(name string) (types.ServerMetadata, error) {
	// For individual server lookups, we could query the API directly for freshness,
	// or use the cached registry. Let's use cached registry for consistency.
	registry, err := p.GetRegistry()
	if err != nil {
		return nil, err
	}

	// Try to find in cached registry first
	if server, ok := registry.Servers[name]; ok {
		return server, nil
	}
	if server, ok := registry.RemoteServers[name]; ok {
		return server, nil
	}

	// Fall back to API lookup (might be a newly added server)
	return p.APIRegistryProvider.GetServer(name)
}

// SearchServers searches for servers, using cached data.
func (p *CachedAPIRegistryProvider) SearchServers(query string) ([]types.ServerMetadata, error) {
	// Ensure cache is loaded first
	_, err := p.GetRegistry()
	if err != nil {
		return nil, err
	}

	// Use base provider's SearchServers which will use our GetRegistry
	return p.BaseProvider.SearchServers(query)
}

// ListServers returns all servers from cache.
func (p *CachedAPIRegistryProvider) ListServers() ([]types.ServerMetadata, error) {
	// Ensure cache is loaded first
	_, err := p.GetRegistry()
	if err != nil {
		return nil, err
	}

	// Use base provider's ListServers which will use our GetRegistry
	return p.BaseProvider.ListServers()
}

// loadFromDisk loads cached data from disk if available and valid.
func (p *CachedAPIRegistryProvider) loadFromDisk() error {
	if p.cacheFile == "" {
		return fmt.Errorf("no cache file configured")
	}

	// Check if file exists
	info, err := os.Stat(p.cacheFile)
	if err != nil {
		return err
	}

	// Check cache age
	if time.Since(info.ModTime()) > maxCacheAge {
		// Cache too old, delete it
		_ = os.Remove(p.cacheFile)
		return fmt.Errorf("cache too old, deleted")
	}

	// Check file size
	if info.Size() > maxCacheFileSize {
		// Cache file too large, delete it
		_ = os.Remove(p.cacheFile)
		return fmt.Errorf("cache file too large, deleted")
	}

	// Read file
	data, err := os.ReadFile(p.cacheFile)
	if err != nil {
		return err
	}

	// Parse JSON
	var registry types.Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		// Corrupted cache, delete it
		_ = os.Remove(p.cacheFile)
		return fmt.Errorf("corrupted cache, deleted: %w", err)
	}

	// Load into memory
	p.cacheMu.Lock()
	p.cachedData = &registry
	p.cacheTime = info.ModTime()
	p.cacheMu.Unlock()

	return nil
}

// saveToDisk saves the current cache to disk.
func (p *CachedAPIRegistryProvider) saveToDisk(registry *types.Registry) error {
	if p.cacheFile == "" {
		return fmt.Errorf("no cache file configured")
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal cache: %w", err)
	}

	// Check size before writing
	if len(data) > maxCacheFileSize {
		return fmt.Errorf("cache data too large: %d bytes", len(data))
	}

	// Write atomically using temp file + rename
	tmpFile := p.cacheFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		return fmt.Errorf("failed to write cache: %w", err)
	}

	if err := os.Rename(tmpFile, p.cacheFile); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to rename cache: %w", err)
	}

	return nil
}

// cleanupOldCaches removes old cache files to prevent unbounded growth.
//
//nolint:gocyclo // Cache cleanup logic naturally has complexity due to multiple passes
func (p *CachedAPIRegistryProvider) cleanupOldCaches() {
	if p.cacheFile == "" {
		return
	}

	cacheDir := filepath.Dir(p.cacheFile)

	// Get all cache files
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return
	}

	now := time.Now()
	var totalSize int64

	// First pass: delete old files and calculate total size
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		path := filepath.Join(cacheDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Delete files older than maxCacheAge
		if now.Sub(info.ModTime()) > maxCacheAge {
			_ = os.Remove(path)
			continue
		}

		totalSize += info.Size()
	}

	// If total size exceeds limit, delete oldest files
	if totalSize > maxTotalCacheSize {
		// Re-read directory after deletions
		entries, err := os.ReadDir(cacheDir)
		if err != nil {
			return
		}

		// Sort by modification time (oldest first)
		type fileInfo struct {
			path    string
			modTime time.Time
			size    int64
		}

		var files []fileInfo
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			path := filepath.Join(cacheDir, entry.Name())
			info, err := entry.Info()
			if err != nil {
				continue
			}

			files = append(files, fileInfo{
				path:    path,
				modTime: info.ModTime(),
				size:    info.Size(),
			})
		}

		// Sort by modification time
		for i := 0; i < len(files); i++ {
			for j := i + 1; j < len(files); j++ {
				if files[i].modTime.After(files[j].modTime) {
					files[i], files[j] = files[j], files[i]
				}
			}
		}

		// Delete oldest files until under limit
		for _, f := range files {
			if totalSize <= maxTotalCacheSize {
				break
			}

			if err := os.Remove(f.path); err == nil {
				totalSize -= f.size
			}
		}
	}
}

// Ensure CachedAPIRegistryProvider implements Provider interface
var _ Provider = (*CachedAPIRegistryProvider)(nil)

// Override methods that query individual servers to ensure they use cache

// GetImageServer returns a specific container server by name (uses cache).
func (p *CachedAPIRegistryProvider) GetImageServer(name string) (*types.ImageMetadata, error) {
	server, err := p.GetServer(name)
	if err != nil {
		return nil, err
	}

	if img, ok := server.(*types.ImageMetadata); ok {
		return img, nil
	}

	return nil, fmt.Errorf("server %s is not a container server", name)
}

// GetRemoteServer returns a specific remote server by name (uses cache).
func (p *CachedAPIRegistryProvider) GetRemoteServer(name string) (*types.RemoteServerMetadata, error) {
	server, err := p.GetServer(name)
	if err != nil {
		return nil, err
	}

	if remote, ok := server.(*types.RemoteServerMetadata); ok {
		return remote, nil
	}

	return nil, fmt.Errorf("server %s is not a remote server", name)
}

// ConvertServerJSON wraps ConvertServerJSON for cached provider
func (*CachedAPIRegistryProvider) ConvertServerJSON(serverJSON *v0.ServerJSON) (types.ServerMetadata, error) {
	return ConvertServerJSON(serverJSON)
}

// ConvertServersToMetadataWithCache wraps ConvertServersToMetadata for cached provider
func (*CachedAPIRegistryProvider) ConvertServersToMetadataWithCache(servers []*v0.ServerJSON) ([]types.ServerMetadata, error) {
	return ConvertServersToMetadata(servers)
}

// GetServerWithContext returns a specific server by name with context support
func (p *CachedAPIRegistryProvider) GetServerWithContext(ctx context.Context, name string) (types.ServerMetadata, error) {
	// Check if context is already cancelled
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	return p.GetServer(name)
}
