// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package skillsvc provides the default implementation of skills.SkillService.
package skillsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	nameref "github.com/google/go-containerregistry/pkg/name"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/stacklok/toolhive-core/httperr"
	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/storage"
)

// ociTagRegexp matches valid OCI tag strings per the distribution spec.
// Reference: https://github.com/opencontainers/distribution-spec/blob/v1.1.1/spec.md#pulling-manifests
var ociTagRegexp = regexp.MustCompile(`^[\w][\w.-]{0,127}$`)

// Option configures the skill service.
type Option func(*service)

// WithPathResolver sets the path resolver for skill installations.
func WithPathResolver(pr skills.PathResolver) Option {
	return func(s *service) {
		s.pathResolver = pr
	}
}

// WithInstaller sets the installer for filesystem operations.
func WithInstaller(inst skills.Installer) Option {
	return func(s *service) {
		s.installer = inst
	}
}

// WithOCIStore sets the local OCI store for skill artifacts.
func WithOCIStore(store *ociskills.Store) Option {
	return func(s *service) {
		s.ociStore = store
	}
}

// WithPackager sets the skill packager for building OCI artifacts.
func WithPackager(p ociskills.SkillPackager) Option {
	return func(s *service) {
		s.packager = p
	}
}

// WithRegistryClient sets the registry client for push/pull operations.
func WithRegistryClient(rc ociskills.RegistryClient) Option {
	return func(s *service) {
		s.registry = rc
	}
}

// skillLock provides per-skill mutual exclusion keyed by scope/name/projectRoot.
// Entries are never evicted. This is acceptable because the number of distinct
// skills on a single machine is expected to remain small (< 1000).
type skillLock struct {
	mu sync.Mutex
	// locks holds per-key mutexes. INVARIANT: entries must never be deleted
	// from this map. The two-phase lock() method depends on pointers remaining
	// valid after the global mutex is released. See lock() for details.
	locks map[string]*sync.Mutex
}

// lock acquires a per-skill mutex and returns a function that releases it.
func (sl *skillLock) lock(name string, scope skills.Scope, projectRoot string) func() {
	sl.mu.Lock()
	key := string(scope) + "/" + name + "/" + projectRoot
	m, ok := sl.locks[key]
	if !ok {
		m = &sync.Mutex{}
		sl.locks[key] = m
	}
	sl.mu.Unlock()

	m.Lock()
	return m.Unlock
}

// service is the default implementation of skills.SkillService.
type service struct {
	locks        skillLock
	store        storage.SkillStore
	pathResolver skills.PathResolver
	installer    skills.Installer
	ociStore     *ociskills.Store
	packager     ociskills.SkillPackager
	registry     ociskills.RegistryClient
}

// New creates a new SkillService backed by the given store.
func New(store storage.SkillStore, opts ...Option) skills.SkillService {
	s := &service{
		store: store,
		locks: skillLock{locks: make(map[string]*sync.Mutex)},
	}
	for _, o := range opts {
		o(s)
	}
	if s.installer == nil {
		s.installer = skills.NewInstaller()
	}
	return s
}

// List returns all installed skills matching the given options.
func (s *service) List(ctx context.Context, opts skills.ListOptions) ([]skills.InstalledSkill, error) {
	filter := storage.ListFilter{
		Scope: opts.Scope,
	}
	return s.store.List(ctx, filter)
}

// Install installs a skill. When the Name field contains an OCI reference
// (detected by the presence of '/', ':', or '@'), the artifact is pulled from
// the registry and extracted. When LayerData is provided, the skill is extracted
// to disk and a full installation record is created. Without LayerData, a
// pending record is created.
func (s *service) Install(ctx context.Context, opts skills.InstallOptions) (*skills.InstallResult, error) {
	scope := defaultScope(opts.Scope)

	// Canonicalize the project root so that equivalent paths
	// (e.g. trailing slash, ".." segments) produce the same lock key
	// and DB record.
	if opts.ProjectRoot != "" {
		opts.ProjectRoot = filepath.Clean(opts.ProjectRoot)
	}

	ref, isOCI, err := parseOCIReference(opts.Name)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("invalid OCI reference %q: %w", opts.Name, err),
			http.StatusBadRequest,
		)
	}
	if isOCI {
		return s.installFromOCI(ctx, opts, scope, ref)
	}

	// Plain skill name — validate and proceed with existing flow.
	if err := skills.ValidateSkillName(opts.Name); err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	defer unlock()

	// Without layer data, fall back to creating a pending record.
	if len(opts.LayerData) == 0 {
		return s.installPending(ctx, opts, scope)
	}

	return s.installWithExtraction(ctx, opts, scope)
}

// Uninstall removes an installed skill and cleans up files for all clients.
func (s *service) Uninstall(ctx context.Context, opts skills.UninstallOptions) error {
	if err := skills.ValidateSkillName(opts.Name); err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	scope := defaultScope(opts.Scope)

	if opts.ProjectRoot != "" {
		opts.ProjectRoot = filepath.Clean(opts.ProjectRoot)
	}

	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	defer unlock()

	// Look up the existing record to find which clients have files.
	existing, err := s.store.Get(ctx, opts.Name, scope, opts.ProjectRoot)
	if err != nil {
		return err
	}

	// Remove files for each client — best-effort: collect errors but don't
	// abort on the first failure so we clean up as much as possible.
	var cleanupErrs []error
	if s.pathResolver != nil {
		for _, clientType := range existing.Clients {
			skillPath, pathErr := s.pathResolver.GetSkillPath(clientType, opts.Name, scope, opts.ProjectRoot)
			if pathErr != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("resolving path for client %q: %w", clientType, pathErr))
				continue
			}
			if rmErr := s.installer.Remove(skillPath); rmErr != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("removing files for client %q: %w", clientType, rmErr))
			}
		}
	}

	if err := s.store.Delete(ctx, opts.Name, scope, opts.ProjectRoot); err != nil {
		return err
	}

	return errors.Join(cleanupErrs...)
}

// Info returns detailed information about a skill.
func (s *service) Info(ctx context.Context, opts skills.InfoOptions) (*skills.SkillInfo, error) {
	if err := skills.ValidateSkillName(opts.Name); err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	skill, err := s.store.Get(ctx, opts.Name, defaultScope(opts.Scope), "")
	if err != nil {
		return nil, err
	}

	return &skills.SkillInfo{
		Metadata:       skill.Metadata,
		InstalledSkill: &skill,
	}, nil
}

// Validate checks whether a skill definition is valid.
func (*service) Validate(_ context.Context, path string) (*skills.ValidationResult, error) {
	if err := validateLocalPath(path); err != nil {
		return nil, err
	}
	return skills.ValidateSkillDir(path)
}

// Build packages a skill directory into a local OCI artifact.
func (s *service) Build(ctx context.Context, opts skills.BuildOptions) (*skills.BuildResult, error) {
	if s.packager == nil || s.ociStore == nil {
		return nil, httperr.WithCode(
			errors.New("OCI packaging is not configured"),
			http.StatusInternalServerError,
		)
	}
	if err := validateLocalPath(opts.Path); err != nil {
		return nil, err
	}
	result, err := s.packager.Package(ctx, opts.Path, ociskills.DefaultPackageOptions())
	if err != nil {
		return nil, fmt.Errorf("packaging skill: %w", err)
	}

	// Tag resolution precedence:
	// 1. Explicit tag from BuildOptions.Tag
	// 2. Skill name from the parsed config (SKILL.md frontmatter)
	// 3. No tag — use raw digest as the reference
	tag := func() string {
		if opts.Tag != "" {
			return opts.Tag
		}
		if result.Config != nil && result.Config.Name != "" {
			return result.Config.Name
		}
		return ""
	}()

	if tag != "" {
		if err := validateOCITag(tag); err != nil {
			return nil, err
		}
	}

	if tag != "" {
		if tagErr := s.ociStore.Tag(ctx, result.IndexDigest, tag); tagErr != nil {
			return nil, fmt.Errorf("tagging artifact: %w", tagErr)
		}
	}

	ref := func() string {
		if tag != "" {
			return tag
		}
		return result.IndexDigest.String()
	}()

	return &skills.BuildResult{Reference: ref}, nil
}

// Push pushes a locally built skill artifact to a remote OCI registry.
func (s *service) Push(ctx context.Context, opts skills.PushOptions) error {
	if s.registry == nil || s.ociStore == nil {
		return httperr.WithCode(
			errors.New("OCI registry is not configured"),
			http.StatusInternalServerError,
		)
	}
	if opts.Reference == "" {
		return httperr.WithCode(
			errors.New("reference is required"),
			http.StatusBadRequest,
		)
	}

	d, err := s.ociStore.Resolve(ctx, opts.Reference)
	if err != nil {
		slog.Debug("failed to resolve OCI reference", "reference", opts.Reference, "error", err)
		return httperr.WithCode(
			fmt.Errorf("reference %q not found in local store", opts.Reference),
			http.StatusNotFound,
		)
	}

	if err := s.registry.Push(ctx, s.ociStore, d, opts.Reference); err != nil {
		return fmt.Errorf("pushing to registry: %w", err)
	}

	return nil
}

// ociPullTimeout is the maximum time allowed for pulling an OCI artifact.
const ociPullTimeout = 5 * time.Minute

// maxCompressedLayerSize is the maximum compressed layer size we'll load into
// memory. Skills are typically small (< 1MB compressed); this limit prevents a
// malicious artifact from causing OOM before the decompression limits kick in.
const maxCompressedLayerSize int64 = 50 * 1024 * 1024 // 50 MB

func parseOCIReference(name string) (nameref.Reference, bool, error) {
	// Structural check: skill names never contain '/', ':', or '@'.
	// OCI references always require at least one of these.
	if !strings.ContainsAny(name, "/:@") {
		return nil, false, nil
	}

	ref, err := nameref.ParseReference(name)
	if err != nil {
		return nil, true, err
	}
	return ref, true, nil
}

// installFromOCI pulls a skill artifact from a remote registry, extracts
// metadata and layer data, then delegates to the standard extraction flow.
func (s *service) installFromOCI(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
	ref nameref.Reference,
) (*skills.InstallResult, error) {
	if s.registry == nil || s.ociStore == nil {
		return nil, httperr.WithCode(
			errors.New("OCI registry is not configured"),
			http.StatusInternalServerError,
		)
	}
	if s.pathResolver == nil {
		return nil, httperr.WithCode(
			errors.New("path resolver is required for OCI installs"),
			http.StatusInternalServerError,
		)
	}

	ociRef := opts.Name

	pullCtx, cancel := context.WithTimeout(ctx, ociPullTimeout)
	defer cancel()

	pulledDigest, err := s.registry.Pull(pullCtx, s.ociStore, ociRef)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("pulling OCI artifact %q: %w", ociRef, err),
			http.StatusBadGateway,
		)
	}

	layerData, skillConfig, err := s.extractOCIContent(ctx, pulledDigest)
	if err != nil {
		return nil, err
	}

	if err := skills.ValidateSkillName(skillConfig.Name); err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("skill artifact contains invalid name: %w", err),
			http.StatusUnprocessableEntity,
		)
	}

	// Supply chain defense: the declared skill name must match the last path
	// component of the OCI reference. The Agent Skills spec requires that the
	// name field matches the parent directory name; by extension, it should
	// match the repository name in the OCI reference. A mismatch could
	// indicate a supply chain attack (e.g., a trusted reference pointing to
	// an artifact that overwrites a different skill).
	repo := ref.Context().RepositoryStr()
	if idx := strings.LastIndex(repo, "/"); idx >= 0 {
		repo = repo[idx+1:]
	}
	if repo != skillConfig.Name {
		return nil, httperr.WithCode(
			fmt.Errorf(
				"skill name %q in artifact does not match OCI reference repository %q",
				skillConfig.Name, repo,
			),
			http.StatusUnprocessableEntity,
		)
	}

	// Hydrate install options from the pulled artifact.
	opts.Name = skillConfig.Name
	opts.LayerData = layerData
	opts.Reference = ociRef
	opts.Digest = pulledDigest.String()
	if opts.Version == "" && skillConfig.Version != "" {
		opts.Version = skillConfig.Version
	}
	// Note: version is optional; if both are empty, install without a version.

	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	defer unlock()

	return s.installWithExtraction(ctx, opts, scope)
}

// extractOCIContent navigates the OCI content graph from a pulled digest,
// extracting the skill config and raw layer data.
func (s *service) extractOCIContent(ctx context.Context, d digest.Digest) ([]byte, *ociskills.SkillConfig, error) {
	isIndex, err := s.ociStore.IsIndex(ctx, d)
	if err != nil {
		return nil, nil, fmt.Errorf("checking OCI content type: %w", err)
	}

	manifestDigest := d
	if isIndex {
		// Skill content is platform-agnostic — all platforms share the same
		// layer, so we can use the first manifest in the index.
		index, indexErr := s.ociStore.GetIndex(ctx, d)
		if indexErr != nil {
			return nil, nil, fmt.Errorf("reading OCI index: %w", indexErr)
		}
		if len(index.Manifests) == 0 {
			return nil, nil, httperr.WithCode(
				errors.New("OCI index contains no manifests"),
				http.StatusUnprocessableEntity,
			)
		}
		manifestDigest = index.Manifests[0].Digest
	}

	manifestBytes, err := s.ociStore.GetManifest(ctx, manifestDigest)
	if err != nil {
		return nil, nil, fmt.Errorf("reading OCI manifest: %w", err)
	}

	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, nil, fmt.Errorf("parsing OCI manifest: %w", err)
	}

	if len(manifest.Layers) == 0 {
		return nil, nil, httperr.WithCode(
			errors.New("OCI manifest contains no layers"),
			http.StatusUnprocessableEntity,
		)
	}

	// Skills use a single-layer format (one tar.gz). Validate the first
	// (and only expected) layer.
	if manifest.Layers[0].MediaType != ocispec.MediaTypeImageLayerGzip {
		return nil, nil, httperr.WithCode(
			fmt.Errorf("unexpected layer media type %q, expected %q",
				manifest.Layers[0].MediaType, ocispec.MediaTypeImageLayerGzip),
			http.StatusUnprocessableEntity,
		)
	}

	// Extract skill config from the OCI image config.
	configBytes, err := s.ociStore.GetBlob(ctx, manifest.Config.Digest)
	if err != nil {
		return nil, nil, fmt.Errorf("reading OCI config blob: %w", err)
	}

	var imgConfig ocispec.Image
	if err := json.Unmarshal(configBytes, &imgConfig); err != nil {
		return nil, nil, fmt.Errorf("parsing OCI image config: %w", err)
	}

	skillConfig, err := ociskills.SkillConfigFromImageConfig(&imgConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("extracting skill config from OCI artifact: %w", err)
	}

	// Guard against oversized layers before loading into memory.
	if manifest.Layers[0].Size > maxCompressedLayerSize {
		return nil, nil, httperr.WithCode(
			fmt.Errorf("compressed layer size %d bytes exceeds maximum %d bytes",
				manifest.Layers[0].Size, maxCompressedLayerSize),
			http.StatusUnprocessableEntity,
		)
	}

	// Extract the raw tar.gz layer data.
	layerData, err := s.ociStore.GetBlob(ctx, manifest.Layers[0].Digest)
	if err != nil {
		return nil, nil, fmt.Errorf("reading OCI layer blob: %w", err)
	}

	return layerData, skillConfig, nil
}

// installPending creates a pending skill record (no extraction).
func (s *service) installPending(
	ctx context.Context, opts skills.InstallOptions, scope skills.Scope,
) (*skills.InstallResult, error) {
	sk := skills.InstalledSkill{
		Metadata: skills.SkillMetadata{
			Name:    opts.Name,
			Version: opts.Version,
		},
		Scope:       scope,
		Status:      skills.InstallStatusPending,
		InstalledAt: time.Now().UTC(),
	}
	if err := s.store.Create(ctx, sk); err != nil {
		return nil, err
	}
	return &skills.InstallResult{Skill: sk}, nil
}

// installWithExtraction handles the full install flow: managed/unmanaged
// detection, extraction, and DB record creation or update.
func (s *service) installWithExtraction(
	ctx context.Context, opts skills.InstallOptions, scope skills.Scope,
) (*skills.InstallResult, error) {
	if s.pathResolver == nil {
		return nil, httperr.WithCode(
			fmt.Errorf("path resolver is required for extraction-based installs"),
			http.StatusInternalServerError,
		)
	}

	clientType := s.resolveClient(opts.Client)

	targetDir, err := s.pathResolver.GetSkillPath(clientType, opts.Name, scope, opts.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving skill path: %w", err)
	}

	// Check store for existing managed record.
	existing, storeErr := s.store.Get(ctx, opts.Name, scope, opts.ProjectRoot)
	isNotFound := errors.Is(storeErr, storage.ErrNotFound)

	switch {
	case storeErr != nil && !isNotFound:
		// Unexpected store error.
		return nil, fmt.Errorf("checking existing skill: %w", storeErr)

	case storeErr == nil && existing.Digest == opts.Digest:
		// Same digest — already installed, no-op.
		return &skills.InstallResult{Skill: existing}, nil

	case storeErr == nil:
		// Different digest — upgrade path.
		return s.upgradeSkill(ctx, opts, scope, clientType, targetDir, existing)

	default:
		// Not found in store — check for unmanaged directory.
		return s.freshInstall(ctx, opts, scope, clientType, targetDir)
	}
}

// upgradeSkill handles re-extraction when the digest differs from the stored record.
func (s *service) upgradeSkill(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
	clientType, targetDir string,
	existing skills.InstalledSkill,
) (*skills.InstallResult, error) {
	if _, err := s.installer.Extract(opts.LayerData, targetDir, true); err != nil {
		return nil, fmt.Errorf("extracting skill upgrade: %w", err)
	}

	sk := buildInstalledSkill(opts, scope, clientType, existing.Clients)
	if err := s.store.Update(ctx, sk); err != nil {
		// Rollback: clean up extracted files since the store record wasn't updated.
		_ = s.installer.Remove(targetDir)
		return nil, err
	}
	return &skills.InstallResult{Skill: sk}, nil
}

// freshInstall handles first-time installation when no store record exists.
func (s *service) freshInstall(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
	clientType, targetDir string,
) (*skills.InstallResult, error) {
	// Check for unmanaged directory on disk.
	if _, statErr := os.Stat(targetDir); statErr == nil && !opts.Force {
		return nil, httperr.WithCode(
			fmt.Errorf("directory %q exists but is not managed by ToolHive; use force to overwrite", targetDir),
			http.StatusConflict,
		)
	}

	if _, err := s.installer.Extract(opts.LayerData, targetDir, opts.Force); err != nil {
		return nil, fmt.Errorf("extracting skill: %w", err)
	}

	sk := buildInstalledSkill(opts, scope, clientType, nil)
	if err := s.store.Create(ctx, sk); err != nil {
		// Rollback: clean up extracted files since the store record wasn't created.
		_ = s.installer.Remove(targetDir)
		return nil, err
	}
	return &skills.InstallResult{Skill: sk}, nil
}

// resolveClient returns the provided client type, or falls back to the first
// skill-supporting client from the path resolver.
func (s *service) resolveClient(clientType string) string {
	if clientType != "" {
		return clientType
	}
	if s.pathResolver != nil {
		clients := s.pathResolver.ListSkillSupportingClients()
		if len(clients) > 0 {
			return clients[0]
		}
	}
	return ""
}

// buildInstalledSkill constructs an InstalledSkill from install options.
func buildInstalledSkill(
	opts skills.InstallOptions,
	scope skills.Scope,
	clientType string,
	existingClients []string,
) skills.InstalledSkill {
	clients := func() []string {
		if len(existingClients) > 0 {
			for _, c := range existingClients {
				if c == clientType {
					return existingClients
				}
			}
			// Defensive copy to avoid mutating the caller's slice.
			newClients := make([]string, len(existingClients), len(existingClients)+1)
			copy(newClients, existingClients)
			return append(newClients, clientType)
		}
		if clientType != "" {
			return []string{clientType}
		}
		return nil
	}()

	return skills.InstalledSkill{
		Metadata: skills.SkillMetadata{
			Name:    opts.Name,
			Version: opts.Version,
		},
		Scope:       scope,
		ProjectRoot: opts.ProjectRoot,
		Reference:   opts.Reference,
		Digest:      opts.Digest,
		Status:      skills.InstallStatusInstalled,
		InstalledAt: time.Now().UTC(),
		Clients:     clients,
	}
}

// validateLocalPath checks that a path is non-empty, absolute, and does not
// contain ".." path traversal segments. This prevents API clients from
// accessing arbitrary directories on the host filesystem via traversal.
func validateLocalPath(path string) error {
	if path == "" {
		return httperr.WithCode(errors.New("path is required"), http.StatusBadRequest)
	}
	if strings.ContainsRune(path, 0) {
		return httperr.WithCode(errors.New("path contains null bytes"), http.StatusBadRequest)
	}
	if !filepath.IsAbs(path) {
		return httperr.WithCode(
			fmt.Errorf("path must be absolute, got %q", path),
			http.StatusBadRequest,
		)
	}
	// Check the raw path for ".." segments before cleaning resolves them.
	// This catches traversal attempts like /safe/dir/../../../etc.
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment == ".." {
			return httperr.WithCode(errors.New("path must not contain '..' traversal segments"), http.StatusBadRequest)
		}
	}
	return nil
}

// validateOCITag checks that a tag conforms to the OCI distribution spec format.
func validateOCITag(tag string) error {
	if !ociTagRegexp.MatchString(tag) {
		return httperr.WithCode(
			fmt.Errorf("invalid OCI tag %q: must match %s", tag, ociTagRegexp.String()),
			http.StatusBadRequest,
		)
	}
	return nil
}

// defaultScope returns ScopeUser when s is empty, otherwise returns s unchanged.
func defaultScope(s skills.Scope) skills.Scope {
	if s == "" {
		return skills.ScopeUser
	}
	return s
}
