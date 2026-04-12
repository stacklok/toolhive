// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package registry provides MCP server registry management functionality.
// It supports multiple registry sources including embedded data, local files,
// remote URLs, and API endpoints, with lazy loading and conversion capabilities.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"

	types "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/registry/auth"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// Store is the central registry data holder. It manages two kinds of
// registries:
//
//   - local registries: backed by a Source that produces raw ServerJSON and
//     Skill slices (embedded, file, or URL). Data is lazy-loaded on first
//     access and cached in memory.
//   - proxied registries: backed by a remote MCP Registry API endpoint.
//     Queries are forwarded over HTTP (not yet implemented).
//
// All public methods are safe for concurrent use.
type Store struct {
	mu          sync.RWMutex
	local       map[string]*localRegistry
	proxied     map[string]*proxiedRegistry
	defaultName string
}

// localRegistry holds the Source and its lazily loaded data.
type localRegistry struct {
	source  Source
	servers []*v0.ServerJSON
	skills  []types.Skill
	loaded  bool
}

// proxiedRegistry holds connection details for a remote MCP Registry API.
// Query methods are not yet implemented and will return an error.
type proxiedRegistry struct {
	baseURL    string
	httpClient *http.Client
}

// NewStore creates an empty Store with the given default registry name.
func NewStore(defaultName string) *Store {
	return &Store{
		local:       make(map[string]*localRegistry),
		proxied:     make(map[string]*proxiedRegistry),
		defaultName: defaultName,
	}
}

// --- Mutation methods ---

// AddLocalRegistry registers a Source-backed registry under the given name.
// If a registry with the same name already exists it is replaced.
func (s *Store) AddLocalRegistry(name string, source Source) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.local[name] = &localRegistry{source: source}
}

// AddProxiedRegistry registers a remote registry API endpoint.
// If a registry with the same name already exists it is replaced.
func (s *Store) AddProxiedRegistry(name string, baseURL string, httpClient *http.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proxied[name] = &proxiedRegistry{
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

// Reset clears all registry data so the Store can be re-populated after a
// configuration change.
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.local = make(map[string]*localRegistry)
	s.proxied = make(map[string]*proxiedRegistry)
}

// --- Metadata queries ---

// DefaultRegistryName returns the name used when callers pass an empty
// registry name.
func (s *Store) DefaultRegistryName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.defaultName
}

// ListRegistries returns the names of all registered registries (local and
// proxied), sorted alphabetically.
func (s *Store) ListRegistries() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]struct{}, len(s.local)+len(s.proxied))
	for name := range s.local {
		seen[name] = struct{}{}
	}
	for name := range s.proxied {
		seen[name] = struct{}{}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// IsProxied reports whether the named registry is a proxied (remote API)
// registry. Returns false for local registries and unknown names.
func (s *Store) IsProxied(registryName string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.proxied[s.resolveName(registryName)]
	return ok
}

// --- Server queries ---

// ListServers returns all servers in the named registry. If registryName is
// empty the default registry is used.
func (s *Store) ListServers(registryName string) ([]*v0.ServerJSON, error) {
	name := s.resolve(registryName)

	if s.isProxiedLocked(name) {
		return nil, fmt.Errorf("proxied registry queries are not yet implemented (registry %q)", name)
	}

	if err := s.ensureLoaded(name); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	lr, ok := s.local[name]
	if !ok {
		return nil, fmt.Errorf("registry %q not found", name)
	}
	return lr.servers, nil
}

// GetServer returns the server matching the given name from the named
// registry. It first attempts an exact match, then falls back to short-name
// matching (the last path component after "/").
func (s *Store) GetServer(registryName, name string) (*v0.ServerJSON, error) {
	servers, err := s.ListServers(registryName)
	if err != nil {
		return nil, err
	}

	// Exact match
	for _, srv := range servers {
		if srv.Name == name {
			return srv, nil
		}
	}

	// Short-name fallback: match on the suffix "/<shortName>"
	if !strings.Contains(name, "/") {
		suffix := "/" + name
		var matches []*v0.ServerJSON
		var matchNames []string
		for _, srv := range servers {
			if strings.HasSuffix(srv.Name, suffix) {
				matches = append(matches, srv)
				matchNames = append(matchNames, srv.Name)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("multiple servers match %q: %s -- use the full name",
				name, strings.Join(matchNames, ", "))
		}
	}

	return nil, fmt.Errorf("server %q not found", name)
}

// SearchServers returns all servers whose name or description contains the
// query string (case-insensitive).
func (s *Store) SearchServers(registryName, query string) ([]*v0.ServerJSON, error) {
	servers, err := s.ListServers(registryName)
	if err != nil {
		return nil, err
	}

	q := strings.ToLower(query)
	var results []*v0.ServerJSON
	for _, srv := range servers {
		if strings.Contains(strings.ToLower(srv.Name), q) ||
			strings.Contains(strings.ToLower(srv.Description), q) {
			results = append(results, srv)
		}
	}
	return results, nil
}

// --- Skill queries ---

// ListSkills returns all skills from the named registry. If registryName is
// empty the default registry is used.
func (s *Store) ListSkills(registryName string) ([]types.Skill, error) {
	name := s.resolve(registryName)

	if s.isProxiedLocked(name) {
		return nil, fmt.Errorf("proxied registry queries are not yet implemented (registry %q)", name)
	}

	if err := s.ensureLoaded(name); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	lr, ok := s.local[name]
	if !ok {
		return nil, fmt.Errorf("registry %q not found", name)
	}
	return lr.skills, nil
}

// GetSkill returns the skill matching the given namespace and name from the
// named registry.
func (s *Store) GetSkill(registryName, namespace, name string) (*types.Skill, error) {
	skills, err := s.ListSkills(registryName)
	if err != nil {
		return nil, err
	}

	for i := range skills {
		if skills[i].Namespace == namespace && skills[i].Name == name {
			return &skills[i], nil
		}
	}
	return nil, nil
}

// SearchSkills returns all skills whose name, description, or namespace
// contains the query string (case-insensitive).
func (s *Store) SearchSkills(registryName, query string) ([]types.Skill, error) {
	skills, err := s.ListSkills(registryName)
	if err != nil {
		return nil, err
	}

	q := strings.ToLower(query)
	var results []types.Skill
	for _, sk := range skills {
		if strings.Contains(strings.ToLower(sk.Name), q) ||
			strings.Contains(strings.ToLower(sk.Description), q) ||
			strings.Contains(strings.ToLower(sk.Namespace), q) {
			results = append(results, sk)
		}
	}
	return results, nil
}

// --- Internal helpers ---

// resolve returns registryName unchanged if non-empty, otherwise the
// store's default name. Does not acquire a lock.
func (s *Store) resolve(registryName string) string {
	if registryName != "" {
		return registryName
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.defaultName
}

// resolveName returns registryName if non-empty, otherwise defaultName.
// Caller must hold at least s.mu.RLock.
func (s *Store) resolveName(registryName string) string {
	if registryName != "" {
		return registryName
	}
	return s.defaultName
}

// isProxiedLocked checks the proxied map under a read lock.
func (s *Store) isProxiedLocked(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.proxied[name]
	return ok
}

// ensureLoaded triggers a lazy load for the named local registry if it has
// not been loaded yet. The load itself runs outside the write lock to avoid
// holding the mutex during I/O; a short write lock is taken afterwards to
// store the results.
func (s *Store) ensureLoaded(name string) error {
	s.mu.RLock()
	lr, ok := s.local[name]
	if ok && lr.loaded {
		s.mu.RUnlock()
		return nil
	}
	s.mu.RUnlock()

	if !ok {
		return fmt.Errorf("registry %q not found", name)
	}

	// Perform the (potentially slow) load outside any lock.
	result, err := lr.source.Load(context.Background())
	if err != nil {
		return fmt.Errorf("failed to load registry %q: %w", name, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check after acquiring the write lock; another goroutine may have
	// loaded between our read and write locks.
	if !lr.loaded {
		lr.servers = result.Servers
		lr.skills = result.Skills
		lr.loaded = true
	}
	return nil
}

// --- Global singleton ---

// storeState groups the sync.Once with the Store it initialises.
// Storing both together behind an atomic pointer allows ResetDefaultStore
// to swap in a fresh struct without writing to one that another goroutine
// is reading — the same pattern used by providerState in factory.go.
type storeState struct {
	once  sync.Once
	store *Store
	err   error
}

// currentStore holds the live singleton state. Replaced atomically by
// ResetDefaultStore; never mutated after creation except inside once.Do.
var currentStore atomic.Pointer[storeState]

func init() {
	currentStore.Store(&storeState{})
}

// DefaultStore returns the global Store singleton, creating it on first call
// from the application config. The Store is populated with registry sources
// matching the config's priority order:
//
//  1. An "embedded" local registry is always added.
//  2. If RegistryApiUrl is set: a proxied registry is added.
//  3. If RegistryUrl is set: a URLSource local registry is added.
//  4. If LocalRegistryPath is set: a FileSource local registry is added.
//
// config.NewProvider() is used (not NewDefaultProvider) so that registered
// ProviderFactory implementations (e.g. Kubernetes) are respected.
func DefaultStore() (*Store, error) {
	ss := currentStore.Load()
	ss.once.Do(func() {
		cfg, err := config.NewProvider().LoadOrCreateConfig()
		if err != nil {
			ss.err = fmt.Errorf("failed to load config for registry store: %w", err)
			return
		}
		ss.store = buildStoreFromConfig(cfg)
	})
	return ss.store, ss.err
}

// ResetDefaultStore atomically replaces the singleton so that the next call
// to DefaultStore creates a fresh Store from the current config. Goroutines
// that already hold a reference to the old Store finish cleanly against it.
func ResetDefaultStore() {
	currentStore.Store(&storeState{})
}

// buildStoreFromConfig creates and populates a Store based on the given
// config. This is separated from DefaultStore for testability.
func buildStoreFromConfig(cfg *config.Config) *Store {
	defaultName := "embedded"
	if cfg != nil && cfg.DefaultRegistry != "" {
		defaultName = cfg.DefaultRegistry
	}

	store := NewStore(defaultName)

	// Always add the embedded catalog.
	store.AddLocalRegistry("embedded", &EmbeddedSource{})

	if cfg == nil {
		return store
	}

	// Iterate over configured registries and add each to the store.
	for _, reg := range cfg.Registries {
		switch reg.Type {
		case config.RegistrySourceTypeAPI:
			tokenSource := resolveTokenSource(reg.Auth, reg.Location)
			httpClient, err := buildProxiedHTTPClient(reg.AllowPrivateIP, tokenSource)
			if err != nil {
				slog.Warn("Failed to build HTTP client for proxied registry, skipping",
					"name", reg.Name, "url", reg.Location, "error", err)
				continue
			}
			store.AddProxiedRegistry(reg.Name, reg.Location, httpClient)
		case config.RegistrySourceTypeURL:
			store.AddLocalRegistry(reg.Name, &URLSource{
				URL:            reg.Location,
				AllowPrivateIP: reg.AllowPrivateIP,
			})
		case config.RegistrySourceTypeFile:
			store.AddLocalRegistry(reg.Name, &FileSource{
				Path: reg.Location,
			})
		case config.RegistrySourceTypeServer:
			tokenSource := resolveTokenSource(reg.Auth, reg.Location)
			httpClient, err := buildProxiedHTTPClient(reg.AllowPrivateIP, tokenSource)
			if err != nil {
				slog.Warn("Failed to build HTTP client for registry server, skipping",
					"name", reg.Name, "url", reg.Location, "error", err)
				continue
			}
			discovered, err := discoverRegistries(httpClient, reg.Location)
			if err != nil {
				slog.Warn("Failed to discover registries from server, skipping",
					"name", reg.Name, "url", reg.Location, "error", err)
				continue
			}
			for _, name := range discovered {
				proxyURL := strings.TrimRight(reg.Location, "/") + "/registry/" + name
				store.AddProxiedRegistry(reg.Name+"/"+name, proxyURL, httpClient)
			}
		default:
			slog.Warn("Unknown registry source type, skipping",
				"name", reg.Name, "type", reg.Type)
		}
	}

	// If the default name was set from config but doesn't correspond to any
	// registered source (and is not "embedded"), keep it anyway; queries will
	// surface a "registry not found" error at lookup time.

	return store
}

// discoverRegistries calls GET /v1/registries on a registry server and returns
// the names of all registries it hosts.
func discoverRegistries(httpClient *http.Client, serverURL string) ([]string, error) {
	endpoint := strings.TrimRight(serverURL, "/") + "/v1/registries"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to discover registries: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry server returned %d", resp.StatusCode)
	}

	var body struct {
		Registries []struct {
			Name string `json:"name"`
		} `json:"registries"`
	}
	const maxDiscoveryResponseSize = 1 * 1024 * 1024 // 1 MB
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxDiscoveryResponseSize)).Decode(&body); err != nil {
		return nil, fmt.Errorf("failed to decode registries response: %w", err)
	}

	names := make([]string, 0, len(body.Registries))
	for _, r := range body.Registries {
		if r.Name != "" && isValidRegistryName(r.Name) {
			names = append(names, r.Name)
		}
	}
	return names, nil
}

// isValidRegistryName validates a discovered registry name to prevent path
// traversal or injection when the name is concatenated into URLs.
func isValidRegistryName(name string) bool {
	if strings.Contains(name, "/") || strings.Contains(name, "..") || strings.Contains(name, "\\") {
		return false
	}
	return len(name) > 0 && len(name) <= 128
}

// buildProxiedHTTPClient creates an HTTP client suitable for proxied registry
// API calls, including auth token injection when a TokenSource is available.
// This mirrors the buildHTTPClient function in pkg/registry/api/shared.go.
func buildProxiedHTTPClient(allowPrivateIP bool, tokenSource auth.TokenSource) (*http.Client, error) {
	builder := networking.NewHttpClientBuilder().WithPrivateIPs(allowPrivateIP)
	if allowPrivateIP {
		builder = builder.WithInsecureAllowHTTP(true)
	}
	httpClient, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build HTTP client: %w", err)
	}
	httpClient.Transport = auth.WrapTransport(httpClient.Transport, tokenSource)
	return httpClient, nil
}

// resolveTokenSource creates a TokenSource from the registry auth config if configured.
// Returns nil if no auth is configured or if token source creation fails (logs warning).
func resolveTokenSource(regAuth *config.RegistryAuth, registryURL string) auth.TokenSource {
	if regAuth == nil || regAuth.Type != config.RegistryAuthTypeOAuth || regAuth.OAuth == nil {
		return nil
	}

	// Try to create secrets provider for token persistence.
	// We load the global config to access the secrets provider type. This is
	// intentional: secrets config is global, not per-registry.
	var secretsProvider secrets.Provider
	globalCfg, err := config.NewProvider().LoadOrCreateConfig()
	if err != nil {
		slog.Debug("Could not load config for secrets provider", "error", err)
	} else {
		providerType, err := globalCfg.Secrets.GetProviderType()
		if err != nil {
			slog.Debug("Secrets provider not available for registry auth token persistence",
				"error", err)
		} else {
			secretsProvider, err = secrets.CreateSecretProvider(providerType)
			if err != nil {
				slog.Warn("Failed to create secrets provider for registry auth, tokens will not be persisted",
					"error", err)
			} else {
				slog.Debug("Secrets provider created for registry auth token persistence",
					"provider_type", providerType)
			}
		}
	}

	tokenSource, err := auth.NewTokenSource(regAuth.OAuth, registryURL, secretsProvider, false)
	if err != nil {
		slog.Warn("Failed to create registry auth token source", "error", err)
		return nil
	}

	return tokenSource
}
