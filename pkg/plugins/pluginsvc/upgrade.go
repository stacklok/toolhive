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

	allowSignerChange, blocked := s.resolveSignerPolicy(ctx, opts, entry, latest, &outcome)
	if blocked {
		return upgradePlan{entry: entry, outcome: outcome}
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

// resolveSignerPolicy applies the signer-change guard to one entry, and
// reports both whether the plan stops here and whether --allow-signer-change
// applies to THIS entry.
//
// The guard runs at plan time so plan-only modes stay install-free, probing
// with a nil expected identity so a differing signer is reported as a change
// rather than a bare failure; blocked plans carry no pinnedRef, exactly like
// ref changes.
//
// The returned flag is not simply opts.AllowSignerChange. The override is
// project-wide but authorizes dropping an anchor the artifact has genuinely
// moved off — not ignoring one it still satisfies. Narrowing it per entry is
// what stops a plugin that needs the override from unpinning every
// key-pinned plugin beside it.
//
// Local-store candidates never reach this point — they returned earlier with
// layerData set — because they carry no signature to probe.
// verifyLocalInstall refuses them outright at install time when the entry is
// locked to a signer, which is the stronger check.
func (s *service) resolveSignerPolicy(
	ctx context.Context,
	opts plugins.UpgradeOptions,
	entry lockfile.Entry,
	latest resolvedLatest,
	outcome *plugins.UpgradeOutcome,
) (allowSignerChange, blocked bool) {
	if entry.Provenance == nil {
		// No recorded signer, so there is no signer change to authorize:
		// forwarding the override would only clear the install's
		// expectations for an entry that has none to clear.
		return false, false
	}
	if entry.Provenance.PublicKey != "" {
		// A keyed entry is measured against its pin whether or not the
		// override was passed; only what a genuine move authorizes differs.
		// Measuring once keeps the two modes from disagreeing about what the
		// candidate is.
		verdict := s.judgeKeyedCandidate(ctx, entry, latest)
		if recordKeyedVerdict(verdict, opts.AllowSignerChange, outcome) {
			return false, true
		}
		return opts.AllowSignerChange && verdict.kind == keyedMovedToKeyless, false
	}
	if s.guardSignerChange(ctx, entry, latest, opts.AllowSignerChange, outcome) {
		return false, true
	}
	return opts.AllowSignerChange, false
}

// guardSignerChange probes the candidate artifact's signer identity and
// fills outcome when the upgrade must not proceed: the candidate is signed
// by a different identity versus the recorded provenance, its signature
// cannot be verified at all, it is unsigned, or its certificate's repository
// ref or runner class differs from what is recorded. Returns true when
// blocked. Only a differing identity or provenance field is reported as a
// signer change, and only that arm is waived by allowSignerChange; the rest
// are failures in both modes, because --allow-signer-change would not
// resolve them — it re-verifies from scratch, which an unsigned or
// unverifiable candidate fails just the same. Running the guard under the
// override is what keeps a project-wide flag from waving those through to
// an install that then either refuses them or, worse, records them.
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
	allowSignerChange bool,
	outcome *plugins.UpgradeOutcome,
) bool {
	probe, probeErr := s.probeCandidateSigner(ctx, entry.Name, latest)
	switch {
	case probeErr != nil && errors.Is(probeErr, verifier.ErrUnsigned):
		// Not a signer change, because the remedy a signer change names
		// cannot resolve it: --allow-signer-change re-verifies from scratch
		// and re-records what it observes, and an unsigned artifact fails
		// that verification exactly as it fails this one. Upgrade has no
		// unsigned-consent flag, so the only way onto an unsigned artifact
		// is the reinstall that records the exception explicitly.
		outcome.Status = plugins.UpgradeStatusFailed
		outcome.Reason = plugins.FailureReasonUnsignedRejected
		outcome.Error = fmt.Errorf("candidate is unsigned, and this entry is pinned to a signer"+
			" identity: %w. Upgrade has no unsigned-consent flag, and --allow-signer-change is not"+
			" one — it re-verifies from scratch, which an unsigned artifact still fails. To move"+
			" this plugin to an unsigned artifact, reinstall it: %s",
			probeErr, projectReinstallCommand(entry, "--allow-unsigned")).Error()
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
		// The one arm the override resolves: it re-verifies from scratch
		// and re-records the identity actually observed.
		if allowSignerChange {
			return false
		}
		outcome.Status = plugins.UpgradeStatusSignerChangeBlocked
		outcome.NewSignerIdentity = probe.SignerIdentity
		return true
	}
	return false
}

// recordKeyedVerdict turns a keyed verdict into a plan outcome and reports
// whether the upgrade stops here.
//
// A genuine key-to-keyless move is the one case the two modes treat
// differently: without the override it is blocked so the caller can decide,
// and with the override it proceeds so the pin can be dropped. Everything
// else is identical in both modes — in particular an undecided candidate
// stops the upgrade either way, so the override can never convert a failed
// measurement into permission to re-anchor.
//
// The identity on a blocked move is load-bearing rather than cosmetic: the
// CLI renders a blocked outcome carrying no NewSignerIdentity as "unsigned",
// so leaving it empty would report a keyless candidate as the one thing the
// measurement just established it is not.
func recordKeyedVerdict(
	verdict keyedVerdict, allowSignerChange bool, outcome *plugins.UpgradeOutcome,
) bool {
	switch verdict.kind {
	case keyedPinHolds:
		return false
	case keyedMovedToKeyless:
		if allowSignerChange {
			return false
		}
		outcome.Status = plugins.UpgradeStatusSignerChangeBlocked
		outcome.NewSignerIdentity = verdict.identity
		return true
	case keyedUndecided:
		outcome.Status = plugins.UpgradeStatusFailed
		outcome.Reason = verdict.reason
		outcome.Error = verdict.err
		return true
	}
	return false
}

// keyedVerdictKind is what a candidate turned out to be when measured
// against the cosign public key its lock entry pins.
type keyedVerdictKind int

const (
	// keyedPinHolds: the candidate verifies against the pinned key, so the
	// signer demonstrably has not changed.
	keyedPinHolds keyedVerdictKind = iota
	// keyedMovedToKeyless: the pinned key conclusively does not apply AND a
	// keyless signature verifies. This is the only genuine key-to-keyless
	// move, and the only state in which dropping the pin is justified.
	keyedMovedToKeyless
	// keyedUndecided: no conclusion could be reached — the candidate carries
	// nothing, carries something that verifies neither way, or verification
	// could not be completed at all.
	keyedUndecided
)

// keyedVerdict is the measurement of a candidate against a pinned key,
// together with the outcome to record when it is not usable.
//
// One measurement serves both callers because they ask the same question and
// differ only in what a genuine move authorizes: the guard blocks it, and
// --allow-signer-change drops the pin for it. Sharing the verdict is what
// keeps those two from disagreeing about what the candidate is.
type keyedVerdict struct {
	kind     keyedVerdictKind
	identity string                // observed keyless identity, keyedMovedToKeyless only
	reason   plugins.FailureReason // keyedUndecided only
	err      string                // keyedUndecided only
}

// judgeKeyedCandidate measures a candidate against the public key its entry
// pins, verifying it keylessly when the key does not apply.
//
// The keyless probe is not only for the all-keyless case. VerifyOCIWithKey
// reports ErrKeylessSigned only when EVERY attached bundle carries a
// certificate, so an artifact mid-migration — a valid keyless bundle beside
// a stale key-pair one — comes back as ErrSignatureInvalid instead. Deciding
// on the keyed error alone would call that a damaged signature, when it is
// the supported transition. Only the probe tells the two apart.
//
// SECURITY: an operational failure is never a conclusion. A registry,
// transport, or context error says nothing about which key signed the
// artifact, so it yields keyedUndecided and the upgrade fails. Treating it
// as "the pin no longer applies" would let a transient network fault stand
// in as evidence that a plugin moved off its key — and under a project-wide
// --allow-signer-change that is enough to drop the pin and re-anchor an
// artifact that still carries a perfectly valid signature by the pinned key.
// An anchor may only be dropped on a conclusive mismatch plus a keyless
// signature that actually verifies.
func (s *service) judgeKeyedCandidate(
	ctx context.Context, entry lockfile.Entry, latest resolvedLatest,
) keyedVerdict {
	pubKeyPEM, err := verifier.DecodePublicKey(entry.Provenance.PublicKey)
	if err != nil {
		return keyedVerdict{
			kind:   keyedUndecided,
			reason: plugins.FailureReasonUnknown,
			err:    fmt.Errorf("lock entry's pinned %w", err).Error(),
		}
	}

	_, verifyErr := s.artifactVerifier().VerifyOCIWithKey(ctx, latest.ref, latest.digest, pubKeyPEM)
	if verifyErr == nil {
		return keyedVerdict{kind: keyedPinHolds}
	}
	if errors.Is(verifyErr, verifier.ErrUnsigned) {
		// Nothing is attached at all, so there is no keyless bundle to find
		// and no signer change to authorize.
		return keyedVerdict{
			kind:   keyedUndecided,
			reason: plugins.FailureReasonUnsignedRejected,
			err: fmt.Errorf("candidate is unsigned, and this entry is pinned to a cosign public"+
				" key: %w. Upgrade has no unsigned-consent flag, and --allow-signer-change is not"+
				" one — it re-verifies from scratch, which an unsigned artifact still fails. To"+
				" move this plugin to an unsigned artifact, reinstall it: %s",
				verifyErr, projectReinstallCommand(entry, "--allow-unsigned")).Error(),
		}
	}

	if !conclusiveKeyedMismatch(verifyErr) {
		// The pin could not be evaluated, so nothing about the candidate has
		// been established — least of all that it moved off the key. Stop
		// before the keyless probe: a candidate carrying BOTH a still-valid
		// pinned-key signature and a valid keyless one would otherwise be
		// re-anchored to keyless on the strength of a transient fault.
		return keyedVerdict{
			kind:   keyedUndecided,
			reason: plugins.FailureReasonUnknown,
			err: fmt.Errorf("verifying candidate against the pinned cosign public key: %w",
				verifyErr).Error(),
		}
	}

	probe, probeErr := s.probeCandidateSigner(ctx, entry.Name, latest)
	if probeErr == nil {
		return keyedVerdict{kind: keyedMovedToKeyless, identity: probe.SignerIdentity}
	}
	return keyedVerdict{
		kind:   keyedUndecided,
		reason: keyedFailureReason(verifyErr, probeErr),
		err:    keyedFailureMessage(entry, verifyErr, probeErr),
	}
}

// conclusiveKeyedMismatch reports whether a failed keyed verification
// actually settled that the pinned key does not apply to the candidate.
//
// Only the verifier's own signature verdicts do. ErrKeylessSigned means
// every attached bundle is keyless; ErrSignatureInvalid means a bundle was
// checked against the key and did not pass. A registry, transport, or
// context error means the question was never answered, and must not be read
// as an answer.
func conclusiveKeyedMismatch(verifyErr error) bool {
	return errors.Is(verifyErr, verifier.ErrKeylessSigned) ||
		errors.Is(verifyErr, verifier.ErrSignatureInvalid)
}

// keyedFailureReason classifies a candidate that verifies neither against the
// pinned key nor keylessly. Only conclusive mismatches reach here — an
// operational failure was already reported as undecided, because calling one
// signature-invalid would attach a destructive remedy to a network blip.
func keyedFailureReason(verifyErr, probeErr error) plugins.FailureReason {
	if errors.Is(verifyErr, verifier.ErrKeylessSigned) {
		// Every bundle is keyless, so the pinned key never had one to check
		// and the keyless verdict is the whole diagnosis.
		if reason := classifySignatureError(probeErr); reason != "" {
			return reason
		}
		return plugins.FailureReasonUnknown
	}
	return plugins.FailureReasonSignatureInvalid
}

// keyedFailureMessage explains a candidate that verifies neither way, naming
// a remedy only where one exists.
func keyedFailureMessage(entry lockfile.Entry, verifyErr, probeErr error) string {
	switch {
	case errors.Is(verifyErr, verifier.ErrKeylessSigned):
		return fmt.Errorf("candidate dropped key-pair signing for keyless, but its keyless"+
			" signature does not verify: %w", probeErr).Error()
	default:
		return fmt.Errorf("candidate does not verify against the cosign public key this entry is"+
			" pinned to — either it was signed with a different key or the signature is damaged:"+
			" %w (re-anchoring to a new key is not supported in place; reinstall it: %s)",
			verifyErr, projectReinstallCommand(entry, "--public-key <path-or-base64>")).Error()
	}
}

// projectReinstallCommand renders a reinstall the caller can actually run.
//
// Naming the flag alone is not enough to act on. `thv ai-plugin install`
// requires the plugin argument, and it defaults to --scope user, where
// validateInstallPublicKey rejects --public-key outright and --allow-unsigned
// records nothing — a lock entry's trust anchor only exists project-scoped.
// So the bare flag would be refused before it verified anything.
func projectReinstallCommand(entry lockfile.Entry, flag string) string {
	source := entry.Source
	if source == "" {
		source = entry.Name
	}
	return fmt.Sprintf("`thv ai-plugin uninstall %s --scope project` then"+
		" `thv ai-plugin install %s --scope project %s`"+
		" (add --project-root if you are not in the project directory)",
		entry.Name, source, flag)
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
