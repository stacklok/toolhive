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
	"github.com/stacklok/toolhive/pkg/skills/verifier"
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
// An upgrade re-resolves a mutable source, which is exactly when a
// compromised or transferred publisher would slip a differently-signed
// artifact into the pinned trust chain, so planning probes the candidate's
// signer identity before anything is installed — see guardSignerChange.
func (s *service) Upgrade(ctx context.Context, opts plugins.UpgradeOptions) (*plugins.UpgradeResult, error) {
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

	result := &plugins.UpgradeResult{Outcomes: make([]plugins.UpgradeOutcome, 0, len(targets))}
	for _, target := range targets {
		result.Outcomes = append(result.Outcomes, s.upgradeOne(ctx, opts, target.Name))
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

// upgradeOne reloads the named lock entry under the per-plugin lock, then
// plans and applies against that fresh snapshot. Planning every entry first
// and applying later would let a concurrent uninstall be resurrected, or a
// newer install be overwritten by this older plan.
func (s *service) upgradeOne(
	ctx context.Context, opts plugins.UpgradeOptions, name string,
) plugins.UpgradeOutcome {
	ctx, unlock := s.lockPlugin(ctx, name, plugins.ScopeProject, opts.ProjectRoot)
	defer unlock()

	root, err := lockfile.OpenRoot(opts.ProjectRoot)
	if err != nil {
		return plugins.UpgradeOutcome{
			Name: name, Status: plugins.UpgradeStatusFailed,
			Reason: classifySyncFailure(err), Error: err.Error(),
		}
	}
	lf, err := lockfile.Load(root)
	if err != nil {
		return plugins.UpgradeOutcome{
			Name: name, Status: plugins.UpgradeStatusFailed,
			Reason: classifySyncFailure(err), Error: err.Error(),
		}
	}
	entry, ok := lf.GetPlugin(name)
	if !ok {
		return plugins.UpgradeOutcome{
			Name:   name,
			Status: plugins.UpgradeStatusFailed,
			Reason: plugins.FailureReasonUnknown,
			Error:  fmt.Sprintf("plugin %q is no longer in the lock file", name),
		}
	}

	plan := s.planUpgrade(ctx, opts, entry)
	if opts.FailOnChanges {
		return plan.outcome
	}
	return s.applyUpgrade(ctx, opts, plan)
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
	// allowSignerChange is opts.AllowSignerChange narrowed to this entry. The
	// flag is project-wide, but honoring it per entry is not: forwarding it
	// for a key-pinned entry whose candidate still verifies against the pin
	// drops the key and sends a key-signed artifact through keyless
	// verification, which refuses it.
	allowSignerChange bool
}

// resolvedLatest is the current state of a lock entry's Source, before any
// install. layerData is set only for local-store hits so apply can install
// those bytes without reinterpreting a bare tag as Docker Hub.
type resolvedLatest struct {
	ref       string
	digest    string
	layerData []byte
	// commitPayload and commitSignature are the git candidate's signature
	// material, captured during resolution so the signer probe does not
	// need a second clone. Empty for OCI candidates.
	commitPayload   []byte
	commitSignature string
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
		// resolvedRef is the local tag for the DB; the lock leaves
		// resolvedReference empty so sync can restore by digest from the
		// local store (never retain an unrelated remote pin beside the new
		// local digest).
		outcome.Status = plugins.UpgradeStatusUpgraded
		return upgradePlan{
			entry:             entry,
			outcome:           outcome,
			pinnedRef:         entry.Name,
			resolvedRef:       latest.ref,
			layerData:         latest.layerData,
			allowSignerChange: opts.AllowSignerChange,
		}
	}

	if latest.ref != entry.ResolvedReference {
		outcome.NewResolvedReference = latest.ref
		if entry.ResolvedReference != "" && repositoryMoved(entry.ResolvedReference, latest.ref) && !opts.AllowRefChange {
			outcome.Status = plugins.UpgradeStatusRefChangeBlocked
			return upgradePlan{entry: entry, outcome: outcome}
		}
	}

	// Signer-change guard: when the entry records a signer identity, the
	// candidate must verify with the same one before the upgrade is even
	// planned. Verification happens here (plan-only modes stay
	// install-free) with a nil expected identity so a differing signer is
	// reported as a change rather than a bare failure; blocked plans carry
	// no pinnedRef, exactly like ref changes.
	//
	// Local-store candidates never reach this point — they returned above
	// with layerData set — because they carry no signature to probe.
	// verifyLocalInstall refuses them outright at install time when the
	// entry is locked to a signer, which is the stronger check.
	allowSignerChange := opts.AllowSignerChange
	if entry.Provenance != nil {
		if !opts.AllowSignerChange {
			if blocked := s.guardSignerChange(ctx, entry, latest, &outcome); blocked {
				return upgradePlan{entry: entry, outcome: outcome}
			}
		} else if entry.Provenance.PublicKey != "" {
			allowSignerChange = !s.candidateMatchesPinnedKey(ctx, entry, latest)
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
	return upgradePlan{
		entry:             entry,
		outcome:           outcome,
		pinnedRef:         pinnedRef,
		resolvedRef:       latest.ref,
		allowSignerChange: allowSignerChange,
	}
}

// guardSignerChange probes the candidate artifact's signer identity and
// fills outcome when the upgrade must not proceed: the candidate is signed
// by a different identity (or unsigned) versus the recorded provenance, its
// signature cannot be verified at all, or its certificate's repository ref
// or runner class differs from what is recorded. Returns true when blocked.
//
// A key-pinned entry has no identity to probe and is handed to
// guardKeyedSignerChange instead.
//
// The repository ref has NO automatic allowance for a tag-shaped rotation.
// An earlier version of the skills guard this mirrors let a recorded tag
// ref rotate to any other tag ref, reasoning that a release workflow signs
// each version on its own tag — but that also let a candidate signed from
// an attacker's OWN tag (e.g. "refs/tags/attacker-release") on the SAME
// repository replace a pinned tag, since nothing tied the candidate's tag
// to the specific version actually being upgraded to. Binding it correctly
// would need the resolved release source's own tag, which the git resolver
// does not surface at all (only the resolved commit hash) — so an OCI-only
// partial fix would leave git-sourced plugins with the identical hole.
// Every ref change — tag or branch, git or OCI — therefore blocks here
// exactly like a genuine signer-identity change, and needs the same
// explicit --allow-signer-change override. See stacklok/toolhive#6315
// review.
func (s *service) guardSignerChange(
	ctx context.Context,
	entry lockfile.Entry,
	latest resolvedLatest,
	outcome *plugins.UpgradeOutcome,
) bool {
	if entry.Provenance.PublicKey != "" {
		return s.guardKeyedSignerChange(ctx, entry, latest, outcome)
	}
	probe, probeErr := s.probeCandidateSigner(ctx, entry.Name, latest)
	switch {
	case probeErr != nil && errors.Is(probeErr, verifier.ErrUnsigned):
		outcome.Status = plugins.UpgradeStatusSignerChangeBlocked
		return true
	case probeErr != nil:
		outcome.Status = plugins.UpgradeStatusFailed
		outcome.Reason = classifySignatureError(probeErr)
		if outcome.Reason == "" {
			outcome.Reason = plugins.FailureReasonUnknown
		}
		outcome.Error = probeErr.Error()
		return true
	case probe.SignerIdentity != entry.Provenance.SignerIdentity ||
		probe.CertIssuer != entry.Provenance.CertIssuer ||
		runnerEnvironmentChanged(probe, entry.Provenance) ||
		repositoryRefChanged(probe, entry.Provenance):
		outcome.Status = plugins.UpgradeStatusSignerChangeBlocked
		outcome.NewSignerIdentity = probe.SignerIdentity
		return true
	}
	return false
}

// guardKeyedSignerChange is guardSignerChange for an entry pinned to a cosign
// public key. There is no identity to probe for and compare — a key-pair
// bundle carries no certificate — so the pinned key is applied to the
// candidate directly: verifying against it IS the evidence that the signer
// has not changed, and the upgrade then proceeds on the anchor the entry
// already records. No new key is accepted here; the lock supplies it.
//
// The candidate is verified over OCI unconditionally, with no git arm: the
// lock schema refuses publicKey on a git entry (a commit signature is checked
// against a certificate, never a key), so a key-pinned entry is always an OCI
// one and latest.commitPayload is always empty here.
//
// The arms are split by whether --allow-signer-change would actually help,
// because a blocked outcome is what tells the caller to reach for it. It
// helps for a candidate that moved to keyless signing: dropping the recorded
// key and re-verifying is exactly the key-to-keyless move resolveKeyAnchor
// supports. It does NOT help for a candidate that dropped its signature —
// the override re-verifies from scratch, and upgrade carries no
// unsigned-consent flag to pair with it, so the install it waves through
// fails unsigned-rejected anyway — nor for a candidate signed by a different
// key, which needs an in-place re-anchor v1 does not offer. Both of those
// report a failure naming the route that works instead of a remedy that
// leads nowhere.
func (s *service) guardKeyedSignerChange(
	ctx context.Context,
	entry lockfile.Entry,
	latest resolvedLatest,
	outcome *plugins.UpgradeOutcome,
) bool {
	pubKeyPEM, err := verifier.DecodePublicKey(entry.Provenance.PublicKey)
	if err != nil {
		outcome.Status = plugins.UpgradeStatusFailed
		outcome.Reason = plugins.FailureReasonUnknown
		outcome.Error = fmt.Errorf("lock entry's pinned %w", err).Error()
		return true
	}
	_, verifyErr := s.artifactVerifier().VerifyOCIWithKey(ctx, latest.ref, latest.digest, pubKeyPEM)
	if verifyErr == nil {
		return false
	}
	if errors.Is(verifyErr, verifier.ErrUnsigned) {
		// Nothing is attached at all, so there is no keyless bundle to find
		// and no signer change to authorize.
		outcome.Status = plugins.UpgradeStatusFailed
		outcome.Reason = plugins.FailureReasonUnsignedRejected
		outcome.Error = fmt.Errorf("candidate is unsigned, and this entry is pinned to a cosign"+
			" public key: %w. Upgrade has no unsigned-consent flag, and --allow-signer-change is"+
			" not one — it re-verifies from scratch, which an unsigned artifact still fails. To"+
			" move this plugin to an unsigned artifact, uninstall it and reinstall it with"+
			" `thv ai-plugin install --allow-unsigned`", verifyErr).Error()
		return true
	}
	return s.classifyKeyedCandidate(ctx, entry, latest, verifyErr, outcome)
}

// candidateMatchesPinnedKey reports whether the candidate still verifies
// against the public key the entry pins.
//
// It exists because --allow-signer-change is a project-wide flag applied per
// entry. The override authorizes dropping a recorded anchor when the
// artifact has genuinely moved off it — not ignoring an anchor the artifact
// still satisfies. resolveKeyAnchor drops the recorded key whenever the flag
// is set, so forwarding it here would push a still-key-signed candidate
// through keyless verification and fail it with ErrKeySigned. That is
// reachable without asking for it: needing the override for one plugin in a
// multi-plugin project would otherwise break every key-pinned plugin
// alongside it.
//
// An undecodable pin reports false. There is nothing to preserve, and the
// override is exactly the escape hatch for an entry whose anchor can no
// longer be applied.
func (s *service) candidateMatchesPinnedKey(
	ctx context.Context, entry lockfile.Entry, latest resolvedLatest,
) bool {
	pubKeyPEM, err := verifier.DecodePublicKey(entry.Provenance.PublicKey)
	if err != nil {
		return false
	}
	_, verifyErr := s.artifactVerifier().VerifyOCIWithKey(ctx, latest.ref, latest.digest, pubKeyPEM)
	return verifyErr == nil
}

// classifyKeyedCandidate decides what a candidate that failed verification
// against the pinned key actually is, by verifying it keylessly before
// committing to a diagnosis. A keyless bundle that verifies means the
// artifact moved to keyless signing: a signer change --allow-signer-change
// authorizes, reported with the identity it moved to.
//
// The probe is not only for the all-keyless case. VerifyOCIWithKey reports
// ErrKeylessSigned only when EVERY attached bundle carries a certificate, so
// an artifact mid-migration — a valid keyless bundle alongside a stale
// key-pair one — comes back as ErrSignatureInvalid instead. Deciding on the
// keyed error alone would call that a damaged signature and send the caller
// to uninstall-and-reinstall, when the override resolves it: dropping the
// pin lets ordinary keyless verification accept the valid bundle. Only the
// probe tells the two apart.
//
// Naming the identity is load-bearing rather than cosmetic. The CLI renders
// a blocked outcome carrying no NewSignerIdentity as "unsigned", so a
// keyless candidate left unnamed is reported as the one thing the guard has
// just established it is not.
func (s *service) classifyKeyedCandidate(
	ctx context.Context,
	entry lockfile.Entry,
	latest resolvedLatest,
	verifyErr error,
	outcome *plugins.UpgradeOutcome,
) bool {
	if probe, probeErr := s.probeCandidateSigner(ctx, entry.Name, latest); probeErr == nil {
		outcome.Status = plugins.UpgradeStatusSignerChangeBlocked
		outcome.NewSignerIdentity = probe.SignerIdentity
		return true
	} else if errors.Is(verifyErr, verifier.ErrKeylessSigned) {
		// Every bundle is keyless, so the pinned key never had one to check
		// and the keyless verdict is the whole diagnosis.
		outcome.Status = plugins.UpgradeStatusFailed
		outcome.Reason = classifySignatureError(probeErr)
		if outcome.Reason == "" {
			outcome.Reason = plugins.FailureReasonUnknown
		}
		outcome.Error = fmt.Errorf("candidate dropped key-pair signing for keyless, but its keyless"+
			" signature does not verify: %w", probeErr).Error()
		return true
	}

	outcome.Status = plugins.UpgradeStatusFailed
	if errors.Is(verifyErr, verifier.ErrSignatureInvalid) {
		outcome.Reason = plugins.FailureReasonSignatureInvalid
		outcome.Error = fmt.Errorf("candidate does not verify against the cosign public key this entry"+
			" is pinned to — either it was signed with a different key or the signature is damaged:"+
			" %w (re-anchoring to a new key is not supported in place; uninstall the plugin and"+
			" reinstall it with `thv ai-plugin install --public-key`)", verifyErr).Error()
		return true
	}
	// A registry, transport, or context failure says nothing about the
	// signature. Calling it signature-invalid would advise uninstalling a
	// working plugin over a network blip.
	outcome.Reason = plugins.FailureReasonUnknown
	outcome.Error = fmt.Errorf("verifying candidate against the pinned cosign public key: %w",
		verifyErr).Error()
	return true
}

// runnerEnvironmentChanged reports whether the candidate's runner class
// differs from the one recorded. An entry that recorded none is
// unconstrained — lock entries written before the field existed have it
// empty, as do certificates that carry no such extension.
func runnerEnvironmentChanged(probe *verifier.Result, recorded *lockfile.Provenance) bool {
	return recorded.RunnerEnvironment != "" && probe.RunnerEnvironment != recorded.RunnerEnvironment
}

// repositoryRefChanged reports whether the candidate's certificate ref
// differs from the one recorded, with the same absent-means-unconstrained
// rule as runnerEnvironmentChanged. Unlike the runner class, no ref value
// is treated as an automatically allowed rotation — see guardSignerChange's
// doc comment for why.
func repositoryRefChanged(probe *verifier.Result, recorded *lockfile.Provenance) bool {
	return recorded.RepositoryRef != "" && probe.RepositoryRef != recorded.RepositoryRef
}

// probeCandidateSigner verifies the candidate artifact chain-of-trust-only
// (nil expected identity) and returns the observed identity. Git candidates
// verify the signature material captured while resolving the candidate
// commit, rather than re-resolving it: skillsvc's probe clones a second
// time, which for plugins would both double the clone cost and let the
// probed commit drift from the one being planned.
//
// The commit payload and signature are bounded here for the same reason
// verifyGitInstall bounds them: both come straight off a remote commit, and
// this probe runs on every guarded upgrade — including --preview and
// --fail-on-changes, which install nothing — so an unbounded probe would let
// a hostile repository spend our CPU without ever reaching an install.
func (s *service) probeCandidateSigner(
	ctx context.Context, pluginName string, latest resolvedLatest,
) (*verifier.Result, error) {
	if len(latest.commitPayload) > 0 {
		if err := rejectOversizedBlob("commit payload", pluginName, len(latest.commitPayload)); err != nil {
			return nil, err
		}
		if err := rejectOversizedBlob("commit signature", pluginName, len(latest.commitSignature)); err != nil {
			return nil, err
		}
		return s.artifactVerifier().VerifyGit(ctx, latest.commitPayload, []byte(latest.commitSignature), nil)
	}
	return s.artifactVerifier().VerifyOCI(ctx, latest.ref, latest.digest, nil)
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

	// Local-store upgrades deliberately leave resolvedReference empty so
	// sync can restore by digest. Do not fall back to a previous remote pin
	// that no longer describes the installed artifact.
	lockResolved := lockableResolvedReference(plan.resolvedRef)
	if lockResolved == "" && len(plan.layerData) == 0 {
		lockResolved = plan.entry.ResolvedReference
	}

	if _, err := s.installAlreadyLocked(ctx, plugins.InstallOptions{
		Name:                  plan.pinnedRef,
		Reference:             plan.resolvedRef,
		LayerData:             plan.layerData,
		Digest:                plan.outcome.NewDigest,
		Scope:                 plugins.ScopeProject,
		ProjectRoot:           opts.ProjectRoot,
		Clients:               clients,
		LockSource:            plan.entry.Source,
		LockResolvedReference: lockResolved,
		AllowSignerChange:     plan.allowSignerChange,
		ExpectedCanonicalName: plan.entry.Name,
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
		return s.resolveGitLatest(ctx, source)
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

// resolveGitLatest re-resolves gitURL to its current HEAD commit. The
// commit's gitsign signature and the payload it covers travel with the
// result so the signer probe can verify the exact commit being planned
// without cloning the repository a second time.
func (s *service) resolveGitLatest(ctx context.Context, gitURL string) (resolvedLatest, error) {
	gitRef, err := gitresolver.ParseGitReference(gitURL)
	if err != nil {
		return resolvedLatest{}, httperr.WithCode(fmt.Errorf("invalid git reference: %w", err), http.StatusBadRequest)
	}

	ctx, cancel := context.WithTimeout(ctx, gitresolver.CloneTimeout)
	defer cancel()

	cloneConfig := gitresolver.CloneConfigForRef(gitRef)
	client := gitresolver.ClientForURL(gitRef.URL, s.gitClient)
	repoInfo, err := client.Clone(ctx, cloneConfig)
	if err != nil {
		return resolvedLatest{}, httperr.WithCode(fmt.Errorf("resolving git plugin: %w", err), http.StatusBadGateway)
	}
	defer func() { _ = client.Cleanup(ctx, repoInfo) }()

	head, err := client.HeadCommit(repoInfo)
	if err != nil {
		return resolvedLatest{}, httperr.WithCode(fmt.Errorf("resolving git plugin: %w", err), http.StatusBadGateway)
	}
	return resolvedLatest{
		ref:             gitURL,
		digest:          head.Hash,
		commitPayload:   head.Payload,
		commitSignature: head.Signature,
	}, nil
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
