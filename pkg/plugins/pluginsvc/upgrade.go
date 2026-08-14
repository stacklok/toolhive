// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	nameref "github.com/google/go-containerregistry/pkg/name"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// var _ ensures *service continues to satisfy the full lock service surface
// now that both Sync and Upgrade exist.
var _ plugins.PluginLockService = (*service)(nil)

// Upgrade re-resolves each targeted lock entry's Source and, when the
// resolved digest has changed, installs the newer content and rewrites the
// entry (Source itself is never rewritten — see RFC THV-0080). Entries
// pinned to an immutable reference (an OCI digest or a full git commit hash)
// are reported not-upgradable: there is nothing newer to resolve to.
//
// Signer-change guarding is intentionally omitted: plugin Sigstore
// verification lands in a later PR. AllowSignerChange is accepted on the
// options type (aliased from skills) but not enforced here.
func (s *service) Upgrade(ctx context.Context, opts plugins.UpgradeOptions) (*plugins.UpgradeResult, error) {
	if !plugins.LockFileFeatureEnabled() {
		return nil, httperr.WithCode(
			fmt.Errorf("plugin lock file is not enabled; set %s=true", plugins.LockFileEnvVar),
			http.StatusForbidden,
		)
	}

	_, projectRoot, err := normalizeProjectRoot(plugins.ScopeProject, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	opts.ProjectRoot = projectRoot

	root, err := lockfile.OpenRoot(projectRoot)
	if err != nil {
		return nil, err
	}
	lf, err := lockfile.Load(root)
	if err != nil {
		return nil, err
	}

	targets, err := selectUpgradeTargets(lf, opts.Names)
	if err != nil {
		return nil, err
	}

	plans := make([]upgradePlan, len(targets))
	for i, entry := range targets {
		plans[i] = s.planUpgrade(ctx, opts, entry)
	}

	result := &plugins.UpgradeResult{Outcomes: make([]plugins.UpgradeOutcome, 0, len(plans))}
	for _, p := range plans {
		if opts.FailOnChanges {
			result.Outcomes = append(result.Outcomes, p.outcome)
			continue
		}
		result.Outcomes = append(result.Outcomes, s.applyUpgrade(ctx, opts, p))
	}
	return result, nil
}

// selectUpgradeTargets returns the lock entries to upgrade: every plugins:
// entry when names is empty, or the named subset in the order requested.
func selectUpgradeTargets(lf *lockfile.Lockfile, names []string) ([]lockfile.Entry, error) {
	if len(names) == 0 {
		return lf.Plugins, nil
	}
	targets := make([]lockfile.Entry, 0, len(names))
	for _, name := range names {
		entry, ok := lf.GetPlugin(name)
		if !ok {
			return nil, httperr.WithCode(
				fmt.Errorf("plugin %q is not present in the lock file", name),
				http.StatusNotFound,
			)
		}
		targets = append(targets, entry)
	}
	return targets, nil
}

// upgradePlan is entry's resolved outcome before any install happens: either
// a terminal status (not-upgradable, up-to-date, ref-change-blocked, or a
// resolution failure) that needs no further action, or the pinned reference
// (and optional local layer bytes) to install when the upgrade is applied.
type upgradePlan struct {
	entry       lockfile.Entry
	outcome     plugins.UpgradeOutcome
	pinnedRef   string // set only when the upgrade needs installing
	resolvedRef string // the resolved reference to record as ResolvedReference
	layerData   []byte // set when the new content was resolved from the local OCI store
}

// resolvedLatest is the current state of a lock entry's Source, before any
// install. layerData is set only for local-store hits so apply can install
// those bytes without reinterpreting a bare tag as Docker Hub.
type resolvedLatest struct {
	ref       string
	digest    string
	layerData []byte
}

func (s *service) planUpgrade(ctx context.Context, opts plugins.UpgradeOptions, entry lockfile.Entry) upgradePlan {
	outcome := plugins.UpgradeOutcome{Name: entry.Name, OldDigest: entry.Digest}

	if isImmutableSource(entry) {
		outcome.Status = plugins.UpgradeStatusNotUpgradable
		return upgradePlan{entry: entry, outcome: outcome}
	}

	latest, err := s.resolveLatestState(ctx, entry.Source)
	if err != nil {
		outcome.Status = plugins.UpgradeStatusFailed
		outcome.Reason = classifySyncFailure(err)
		outcome.Error = err.Error()
		return upgradePlan{entry: entry, outcome: outcome}
	}
	outcome.NewDigest = latest.digest

	if latest.digest == entry.Digest {
		outcome.Status = plugins.UpgradeStatusUpToDate
		return upgradePlan{entry: entry, outcome: outcome}
	}

	if len(latest.layerData) > 0 {
		// Local-store hit: carry the exact artifact. buildPinnedReference
		// would parse a bare tag as index.docker.io/library/<tag>@digest.
		outcome.Status = plugins.UpgradeStatusUpgraded
		return upgradePlan{
			entry:     entry,
			outcome:   outcome,
			pinnedRef: entry.Name,
			layerData: latest.layerData,
		}
	}

	if latest.ref != entry.ResolvedReference {
		outcome.NewResolvedReference = latest.ref
		if entry.ResolvedReference != "" && repositoryMoved(entry.ResolvedReference, latest.ref) && !opts.AllowRefChange {
			outcome.Status = plugins.UpgradeStatusRefChangeBlocked
			return upgradePlan{entry: entry, outcome: outcome}
		}
	}

	pinnedRef, err := buildPinnedReference(lockfile.Entry{ResolvedReference: latest.ref, Digest: latest.digest})
	if err != nil {
		outcome.Status = plugins.UpgradeStatusFailed
		outcome.Reason = plugins.FailureReasonUnknown
		outcome.Error = fmt.Errorf("pinning resolved reference: %w", err).Error()
		return upgradePlan{entry: entry, outcome: outcome}
	}

	outcome.Status = plugins.UpgradeStatusUpgraded
	return upgradePlan{entry: entry, outcome: outcome, pinnedRef: pinnedRef, resolvedRef: latest.ref}
}

func (s *service) applyUpgrade(ctx context.Context, opts plugins.UpgradeOptions, plan upgradePlan) plugins.UpgradeOutcome {
	if plan.pinnedRef == "" || opts.Preview {
		return plan.outcome
	}

	clients := opts.Clients
	if len(clients) == 0 {
		if existing, err := s.store.Get(ctx, plan.entry.Name, plugins.ScopeProject, opts.ProjectRoot); err == nil {
			clients = existing.Clients
		}
	}

	if _, err := s.Install(ctx, plugins.InstallOptions{
		Name:                  plan.pinnedRef,
		LayerData:             plan.layerData,
		Digest:                plan.outcome.NewDigest,
		Scope:                 plugins.ScopeProject,
		ProjectRoot:           opts.ProjectRoot,
		Clients:               clients,
		LockSource:            plan.entry.Source,
		LockResolvedReference: lockableResolvedReference(plan.resolvedRef),
	}); err != nil {
		outcome := plan.outcome
		outcome.Status = plugins.UpgradeStatusFailed
		outcome.Reason = classifySyncFailure(err)
		outcome.Error = err.Error()
		return outcome
	}

	return plan.outcome
}

// resolveLatestState re-resolves source (a lock entry's original Source
// value) to its current resolvedReference, digest, and (for local-store
// hits) layer bytes, using the same dispatch order as Install (git, direct
// OCI, plain name via local store then registry), but stopping short of
// extraction or any DB/lock write. For OCI sources this still pulls the
// artifact into the local store — matching the RFC's "preview is not
// side-effect-free" note.
func (s *service) resolveLatestState(ctx context.Context, source string) (resolvedLatest, error) {
	if gitresolver.IsGitReference(source) {
		ref, digest, err := s.resolveGitLatest(ctx, source)
		return resolvedLatest{ref: ref, digest: digest}, err
	}

	ref, isOCI, parseErr := parseOCIReference(source)
	if parseErr != nil {
		return resolvedLatest{}, httperr.WithCode(
			fmt.Errorf("invalid OCI reference %q: %w", source, parseErr),
			http.StatusBadRequest,
		)
	}
	if isOCI {
		resolvedRef, digest, err := s.resolveOCILatest(ctx, ref)
		return resolvedLatest{ref: resolvedRef, digest: digest}, err
	}

	return s.resolvePlainNameLatest(ctx, source)
}

// resolvePlainNameLatest mirrors installByName: local OCI store first, then
// the registry lookup. A lock entry whose Source is a bare plugin name is
// otherwise stuck talking only to the registry, so a local rebuild would
// never be picked up by upgrade. A local hit returns the extracted layer so
// apply installs that artifact instead of a Docker Hub implicit reference.
func (s *service) resolvePlainNameLatest(ctx context.Context, source string) (resolvedLatest, error) {
	if s.ociStore != nil {
		opts := plugins.InstallOptions{Name: source}
		resolved, err := s.resolveFromLocalStore(ctx, &opts)
		if err != nil {
			return resolvedLatest{}, err
		}
		if resolved {
			return resolvedLatest{ref: opts.Reference, digest: opts.Digest, layerData: opts.LayerData}, nil
		}
	}
	ref, digest, err := s.resolveRegistryNameLatest(ctx, source)
	return resolvedLatest{ref: ref, digest: digest}, err
}

func (s *service) resolveGitLatest(ctx context.Context, gitURL string) (string, string, error) {
	gitRef, err := gitresolver.ParseGitReference(gitURL)
	if err != nil {
		return "", "", httperr.WithCode(fmt.Errorf("invalid git reference: %w", err), http.StatusBadRequest)
	}

	ctx, cancel := context.WithTimeout(ctx, gitresolver.CloneTimeout)
	defer cancel()

	cloneConfig := gitresolver.CloneConfigForRef(gitRef)
	client := gitresolver.ClientForURL(gitRef.URL, s.gitClient)
	repoInfo, err := client.Clone(ctx, cloneConfig)
	if err != nil {
		return "", "", httperr.WithCode(fmt.Errorf("resolving git plugin: %w", err), http.StatusBadGateway)
	}
	defer func() { _ = client.Cleanup(ctx, repoInfo) }()

	head, err := client.HeadCommit(repoInfo)
	if err != nil {
		return "", "", httperr.WithCode(fmt.Errorf("resolving git plugin: %w", err), http.StatusBadGateway)
	}
	return gitURL, head.Hash, nil
}

func (s *service) resolveOCILatest(ctx context.Context, ref nameref.Reference) (string, string, error) {
	if s.registry == nil || s.ociStore == nil {
		return "", "", httperr.WithCode(errors.New("OCI registry is not configured"), http.StatusInternalServerError)
	}
	if err := validateOCIRegistryHost(ref); err != nil {
		return "", "", err
	}

	pullCtx, cancel := context.WithTimeout(ctx, ociPullTimeout)
	defer cancel()

	d, err := s.registry.Pull(pullCtx, s.ociStore, qualifiedOCIRef(ref))
	if err != nil {
		return "", "", httperr.WithCode(fmt.Errorf("pulling %q: %w", ref.String(), err), classifyPullError(err))
	}
	return qualifiedOCIRef(ref), d.String(), nil
}

func (s *service) resolveRegistryNameLatest(ctx context.Context, source string) (string, string, error) {
	if s.pluginLookup == nil {
		return "", "", httperr.WithCode(
			fmt.Errorf("plugin %q not found in local store or registry", source),
			http.StatusNotFound,
		)
	}

	namespace, searchName := splitQualifiedName(source)
	hits, err := s.pluginLookup.SearchPlugins(ctx, searchName)
	if err != nil {
		slog.Warn("registry plugin lookup failed, falling back to not-found", "name", source, "error", err)
		return "", "", httperr.WithCode(
			fmt.Errorf("plugin %q not found in local store or registry", source),
			http.StatusNotFound,
		)
	}

	var matches []PluginSearchHit
	for _, hit := range hits {
		if !strings.EqualFold(hit.Name, searchName) {
			continue
		}
		if namespace != "" && !strings.EqualFold(hit.Namespace, namespace) {
			continue
		}
		matches = append(matches, hit)
	}
	switch {
	case len(matches) == 0:
		return "", "", httperr.WithCode(
			fmt.Errorf("plugin %q not found in local store or registry", source),
			http.StatusNotFound,
		)
	case len(matches) > 1:
		return "", "", ambiguousPluginNameError(source, matches)
	}

	pkg, pkgErr := selectOCIPluginPackage(source, matches[0].Packages)
	if pkgErr != nil {
		return "", "", pkgErr
	}
	ref, isOCIRef, parseErr := parseOCIReference(pkg.Reference)
	if parseErr != nil {
		return "", "", httperr.WithCode(
			fmt.Errorf("registry returned invalid OCI reference %q: %w", pkg.Reference, parseErr),
			http.StatusUnprocessableEntity,
		)
	}
	if !isOCIRef || ref == nil {
		return "", "", httperr.WithCode(
			fmt.Errorf("registry returned invalid OCI reference %q", pkg.Reference),
			http.StatusUnprocessableEntity,
		)
	}
	return s.resolveOCILatest(ctx, ref)
}
