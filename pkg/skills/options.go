// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import regtypes "github.com/stacklok/toolhive-core/registry/types"

// ListOptions configures the behavior of the List operation.
type ListOptions struct {
	// Scope filters results by installation scope.
	Scope Scope `json:"scope,omitempty"`
	// ClientApp filters results by client application.
	ClientApp string `json:"client,omitempty"`
	// ProjectRoot filters results by project root path.
	ProjectRoot string `json:"project_root,omitempty"`
	// Group filters results to only skills that belong to the specified group.
	Group string `json:"group,omitempty"`
}

// InstallOptions configures the behavior of the Install operation.
type InstallOptions struct {
	// Name is the skill name or OCI reference to install.
	Name string `json:"name"`
	// Version is the specific version to install. Empty means latest.
	Version string `json:"version,omitempty"`
	// Scope is the installation scope.
	Scope Scope `json:"scope,omitempty"`
	// Clients lists target clients (e.g., "claude-code"). Empty means every
	// skill-supporting client detected on this host.
	Clients []string `json:"clients,omitempty"`
	// Force allows overwriting unmanaged skill directories.
	Force bool `json:"force,omitempty"`
	// ProjectRoot is the project root path for project-scoped installs.
	ProjectRoot string `json:"project_root,omitempty"`
	// Group is the group name to add the skill to after installation.
	Group string `json:"group,omitempty"`
	// AllowUnsigned permits installing a project-scoped skill whose artifact
	// carries no Sigstore signature. Without it, unsigned artifacts are
	// rejected; with it, the lock entry records the exception as
	// "unsigned: true". Skill content is AI-executed instructions, so this
	// is an explicit per-install trust decision, never a default.
	AllowUnsigned bool `json:"allow_unsigned,omitempty"`
	// PublicKey is the base64-encoded DER SPKI cosign public key a
	// project-scoped install must verify the artifact against, for artifacts
	// signed with a cosign key pair rather than keylessly. Required on true
	// first use of such an artifact — the signing key is recoverable from
	// neither the artifact nor its bundle, so nothing else can supply the
	// trust anchor — and pinned into the lock entry, which supplies it on
	// every install thereafter. A value that conflicts with what the lock
	// already pins is rejected, never ignored.
	PublicKey string `json:"public_key,omitempty"`
	// LayerData is the tar.gz content from an OCI layer. Internal use only — NOT exposed via HTTP API.
	LayerData []byte `json:"-"`
	// Reference is the full OCI reference (e.g. ghcr.io/org/skill:v1).
	Reference string `json:"-"`
	// Digest is the OCI digest for upgrade detection.
	Digest string `json:"-"`
	// LockSource overrides the value recorded as the lock entry's Source. When
	// empty, the entry's Source is Name as given by the caller before any
	// internal resolution. Set by Sync/Upgrade, which pass an already-resolved
	// Name that must not overwrite the entry's original Source. Internal use
	// only — NOT exposed via HTTP API.
	LockSource string `json:"-"`
	// LockResolvedReference overrides the value recorded as the lock entry's
	// ResolvedReference. When empty, the entry's ResolvedReference is
	// whatever this install actually resolved to. Set by Sync when
	// reinstalling at a pinned reference: without this override, a drift
	// repair would overwrite ResolvedReference with the internal pinned
	// form (e.g. a digest or commit hash spliced into the reference)
	// instead of preserving what Source originally resolved to. Internal
	// use only — NOT exposed via HTTP API.
	LockResolvedReference string `json:"-"`
	// RequiredByParent is set when this install is a transitively materialized
	// dependency (toolhive.requires) of another skill, naming that parent.
	// Empty means the user explicitly requested this install. Internal use
	// only — NOT exposed via HTTP API.
	RequiredByParent string `json:"-"`
	// ExpectedCanonicalName, when set, requires the resolved skill/manifest
	// name to equal this value before any install mutation. Used by
	// Sync/Upgrade so a lock entry cannot be repaired under a different
	// canonical identity. Internal use only — NOT exposed via HTTP API.
	ExpectedCanonicalName string `json:"-"`
	// SyncRestore forces re-extraction to every existing client even when
	// Digest matches the currently-installed digest. Set by Sync when
	// reinstalling at a pinned reference: the whole point is repairing
	// on-disk drift that happened without the pinned digest changing, so the
	// normal "same digest means content is already correct" fast path must
	// not apply. Internal use only — NOT exposed via HTTP API.
	SyncRestore bool `json:"-"`
	// AllowSignerChange lets install-time verification re-record the
	// observed identity instead of enforcing the lock file's recorded one.
	// Internal use only — set by upgrade when its signer-change guard was
	// explicitly overridden.
	AllowSignerChange bool `json:"-"`
	// Unsigned records the trust decision that this install proceeded
	// without a verified signature (via AllowUnsigned). Set internally by
	// install-time verification; recorded as `unsigned: true` in the lock
	// entry.
	Unsigned bool `json:"-"`
	// CatalogProvenance is the independently optional provenance constraints
	// declared by the skill registry/catalog entry this install resolved
	// from. On true first use, install-time verification checks each non-empty
	// field against the observed signature. A lock entry, including a legacy
	// entry with no trust state, always takes precedence over this. Internal
	// use only — NOT exposed via HTTP API.
	CatalogProvenance *regtypes.Provenance `json:"-"`
	// Provenance carries the verified signer identity established during
	// install-time verification, for recording into the lock entry. Set by
	// the verification step, nil when the artifact is unsigned or
	// verification did not run. Internal use only — NOT exposed via HTTP API.
	Provenance *ProvenanceInfo `json:"-"`
	// SigstoreBundle is the serialized Sigstore bundle backing Provenance,
	// persisted alongside the install record so sync can re-verify offline.
	// Internal use only — NOT exposed via HTTP API.
	SigstoreBundle []byte `json:"-"`
}

// ProvenanceInfo is the verified signer identity of an installed artifact,
// the in-memory mirror of the lock file's provenance block.
type ProvenanceInfo struct {
	// SignerIdentity is the certificate subject identity (workflow path for
	// GitHub Actions certificates, SAN verbatim otherwise).
	SignerIdentity string `json:"signer_identity"`
	// CertIssuer is the OIDC issuer that authenticated the signer.
	CertIssuer string `json:"cert_issuer"`
	// RepositoryURI is the source repository from the certificate
	// extensions, when present.
	RepositoryURI string `json:"repository_uri,omitempty"`
	// RepositoryRef is the git ref the signing workflow ran on, from Fulcio
	// certificate extension 1.3.6.1.4.1.57264.1.14. Empty means
	// unconstrained, matching lock files written before the field existed.
	RepositoryRef string `json:"repository_ref,omitempty"`
	// RunnerEnvironment is the runner class the signing workflow executed in
	// (e.g. "github-hosted"), from Fulcio certificate extension
	// 1.3.6.1.4.1.57264.1.11. Empty means unconstrained.
	RunnerEnvironment string `json:"runner_environment,omitempty"`
	// SigstoreURL is the Sigstore instance the signature chains to.
	SigstoreURL string `json:"sigstore_url,omitempty"`
	// PublicKey is the base64-encoded DER SPKI cosign public key a
	// key-pair-signed entry is pinned to. Set only when SignerIdentity and
	// CertIssuer are empty: the two anchors are mutually exclusive.
	PublicKey string `json:"public_key,omitempty"`
	// Provisional marks provenance with a documented verification gap
	// (git signatures until transparency-log validation lands).
	Provisional bool `json:"provisional,omitempty"`
}

// InstallResult contains the outcome of an Install operation.
type InstallResult struct {
	// Skill is the installed skill.
	Skill InstalledSkill `json:"skill"`
	// PreExisting is the store record as it was before this install, or nil
	// when this install created the record. Rollback uses it to restore the
	// previous state instead of destructively deleting a record this call
	// did not create. Internal use only — NOT exposed via HTTP API.
	PreExisting *InstalledSkill `json:"-"`
	// RestoreFiles restores on-disk skill trees overwritten by a force
	// reinstall or upgrade. Set when extraction snapshotted prior content;
	// rollback calls it so a failed install does not leave half-written
	// files. Internal use only — NOT exposed via HTTP API.
	RestoreFiles func() error `json:"-"`
	// Provenance is the verified signer identity this install recorded —
	// surfaced so callers can display what trust-on-first-use pinned.
	Provenance *ProvenanceInfo `json:"provenance,omitempty"`
	// Unsigned reports that the install was recorded as an explicit
	// unsigned exception.
	Unsigned bool `json:"unsigned,omitempty"`
}

// UninstallOptions configures the behavior of the Uninstall operation.
type UninstallOptions struct {
	// Name is the skill name to uninstall.
	Name string `json:"name"`
	// Scope is the scope from which to uninstall.
	Scope Scope `json:"scope,omitempty"`
	// ProjectRoot is the project root path for project-scoped skills.
	ProjectRoot string `json:"project_root,omitempty"`
	// Visited tracks skill names already removed in this cascade-uninstall
	// tree, preventing infinite recursion on a requiredBy cycle. Left nil by
	// external callers; Uninstall initializes it on first entry and threads
	// it through recursive cascade removals. Internal use only — NOT exposed
	// via HTTP API.
	Visited map[string]struct{} `json:"-"`
}

// InfoOptions configures the behavior of the Info operation.
type InfoOptions struct {
	// Name is the skill name to look up.
	Name string `json:"name"`
	// Scope filters the lookup by installation scope.
	Scope Scope `json:"scope,omitempty"`
	// ProjectRoot filters the lookup by project root path.
	ProjectRoot string `json:"project_root,omitempty"`
}

// SkillInfo contains detailed information about an installed skill.
type SkillInfo struct {
	// Metadata contains the skill's metadata.
	Metadata SkillMetadata `json:"metadata"`
	// InstalledSkill contains the full installation record.
	InstalledSkill *InstalledSkill `json:"installed_skill,omitempty"`
	// Provenance is the signer identity the project's lock file records
	// for this skill, when project-scoped and lock-managed.
	Provenance *ProvenanceInfo `json:"provenance,omitempty"`
	// Unsigned reports that the lock file records an explicit unsigned
	// exception for this skill.
	Unsigned bool `json:"unsigned,omitempty"`
}

// ContentOptions configures the behavior of the GetContent operation.
type ContentOptions struct {
	// Reference is an OCI reference (e.g. ghcr.io/org/skill:v1) or a local build tag.
	Reference string `json:"reference"`
}

// SkillFileEntry represents a single file within a skill artifact.
type SkillFileEntry struct {
	// Path is the file path within the artifact.
	Path string `json:"path"`
	// Size is the uncompressed file size in bytes.
	Size int `json:"size"`
}

// SkillContent contains the SKILL.md body and file listing extracted from an OCI artifact.
type SkillContent struct {
	// Name is the skill name from the OCI config labels.
	Name string `json:"name"`
	// Description is the skill description from the OCI config labels.
	Description string `json:"description"`
	// Version is the skill version from the OCI config labels.
	Version string `json:"version,omitempty"`
	// License is the SPDX license identifier from the OCI config labels.
	License string `json:"license,omitempty"`
	// Body is the raw SKILL.md markdown content.
	Body string `json:"body"`
	// Files is the list of all files in the artifact with their sizes.
	Files []SkillFileEntry `json:"files"`
}

// ValidationResult contains the outcome of a Validate operation.
type ValidationResult struct {
	// Valid indicates whether the skill definition is valid.
	Valid bool `json:"valid"`
	// Errors is a list of validation errors, if any.
	Errors []string `json:"errors,omitempty"`
	// Warnings is a list of non-blocking validation warnings, if any.
	Warnings []string `json:"warnings,omitempty"`
}

// BuildOptions configures the behavior of the Build operation.
type BuildOptions struct {
	// Path is the local directory path containing the skill definition.
	Path string `json:"path"`
	// Tag is the OCI tag to use for the built artifact.
	Tag string `json:"tag,omitempty"`
}

// BuildResult contains the outcome of a Build operation.
type BuildResult struct {
	// Reference is the OCI reference of the built skill artifact.
	Reference string `json:"reference"`
}

// PushOptions configures the behavior of the Push operation.
type PushOptions struct {
	// Reference is the OCI reference to push.
	Reference string `json:"reference"`
	// Key is the path to a cosign PEM private key used to sign the pushed
	// artifact (COSIGN_PASSWORD decrypts encrypted keys). Mutually exclusive
	// with IdentityToken. One of Key, IdentityToken, or NoSign is required.
	Key string `json:"key,omitempty"`
	// IdentityToken is a short-lived OIDC identity token (raw JWT) used for
	// keyless signing: the server exchanges it with Fulcio for a short-lived
	// signing certificate and records the signature in Rekor. Mutually
	// exclusive with Key. One of Key, IdentityToken, or NoSign is required.
	IdentityToken string `json:"identity_token,omitempty"`
	// NoSign pushes without signing. Consumers installing the artifact
	// project-scoped will need an explicit unsigned exception.
	NoSign bool `json:"no_sign,omitempty"`
}

// SyncOptions configures the behavior of the Sync operation.
type SyncOptions struct {
	// ProjectRoot is the project root path whose lock file should be synced.
	ProjectRoot string `json:"project_root"`
	// Clients lists target clients (e.g., "claude-code"). Empty means every
	// skill-supporting client detected on this host.
	Clients []string `json:"clients,omitempty"`
	// Prune removes project-scoped skills that are installed but not present
	// in the lock file. When false, such skills are only reported.
	Prune bool `json:"prune,omitempty"`
	// Check verifies on-disk content against contentDigest without installing
	// or writing anything.
	Check bool `json:"check,omitempty"`
	// AllowUnsigned permits adopting a skill whose signature state cannot
	// be established (no stored bundle), recording an explicit unsigned
	// exception — the same trust decision as an unsigned install.
	AllowUnsigned bool `json:"allow_unsigned,omitempty"`
	// Adopt writes lock entries for existing unmanaged project-scope installs.
	Adopt bool `json:"adopt,omitempty"`
}

// FailureReason is a typed failure reason for sync/upgrade operations, per
// RFC THV-0080's exit-code and automation contract. Only reasons the current
// feature set can actually produce are defined; the RFC's remaining values
// (the Sigstore verification reasons and ref-change-blocked, which surfaces
// as an UpgradeStatus rather than a failure) land together with the code
// that emits them.
type FailureReason string

// Typed failure reasons for sync/upgrade operations.
const (
	// FailureReasonRegistryUnreachable means the skill's remote source — an
	// OCI registry or a git host — could not be reached.
	FailureReasonRegistryUnreachable FailureReason = "registry-unreachable"
	FailureReasonDigestMissing       FailureReason = "digest-missing"
	FailureReasonValidationRejected  FailureReason = "validation-rejected"
	FailureReasonLockWriteFailed     FailureReason = "lock-write-failed"
	// FailureReasonSignatureInvalid means the artifact carries signature
	// material that failed cryptographic verification.
	FailureReasonSignatureInvalid FailureReason = "signature-invalid"
	// FailureReasonSignerMismatch means the artifact verifies, but against
	// an identity other than the one recorded in the lock file.
	FailureReasonSignerMismatch FailureReason = "signer-mismatch"
	// FailureReasonProvenanceFieldMismatch means the artifact verifies
	// against the recorded signer identity and issuer, but its
	// certificate's repository ref or runner environment differs from what
	// is pinned — a narrower case than FailureReasonSignerMismatch, whose
	// remediation (--allow-signer-change) is nonetheless the same.
	FailureReasonProvenanceFieldMismatch FailureReason = "provenance-field-mismatch"
	// FailureReasonKeySigned means the artifact carries only cosign
	// key-pair signatures, which install-time verification cannot check.
	// Distinct from FailureReasonSignatureInvalid: nothing is wrong with
	// the signature, there is simply no trust anchor to check it against.
	FailureReasonKeySigned FailureReason = "key-signed-unverifiable"
	// FailureReasonUnsignedRejected means the artifact is unsigned and the
	// operation did not permit unsigned installs.
	FailureReasonUnsignedRejected FailureReason = "unsigned-rejected"
	FailureReasonUnknown          FailureReason = "unknown"
)

// SyncFailure describes a single skill that failed to sync.
type SyncFailure struct {
	// Name is the skill name that failed.
	Name string `json:"name"`
	// Reason is a typed failure reason for CI and automation.
	Reason FailureReason `json:"reason,omitempty"`
	// Error is a human-readable description of the failure.
	Error string `json:"error"`
}

// SyncResult contains the outcome of a Sync operation.
type SyncResult struct {
	// Installed lists skills that were installed or reinstalled to match the lock file.
	Installed []string `json:"installed,omitempty"`
	// Drifted lists skills whose on-disk contentDigest differed from the lock
	// file. Normally these are reinstalled to match it; when Check is set,
	// nothing is written and this field reports the drift only.
	Drifted []string `json:"drifted,omitempty"`
	// Missing lists lock entries with no corresponding install record at all
	// — the fresh-clone state. Normally these are installed at their pinned
	// reference; when Check is set, nothing is written and this field
	// reports the gap only.
	Missing []string `json:"missing,omitempty"`
	// AlreadyCurrent lists skills that already matched the lock file.
	AlreadyCurrent []string `json:"already_current,omitempty"`
	// NeverManaged lists project-scoped skills never recorded as lock-managed.
	NeverManaged []string `json:"never_managed,omitempty"`
	// RemovedFromLock lists previously managed skills absent from the lock file.
	RemovedFromLock []string `json:"removed_from_lock,omitempty"`
	// Pruned lists removed-from-lock skills that were uninstalled because Prune was set.
	Pruned []string `json:"pruned,omitempty"`
	// Failed lists skills that could not be synced, with the reason for each.
	// Drift alone is never reported here — see Drifted.
	Failed []SyncFailure `json:"failed,omitempty"`
}

// UpgradeOptions configures the behavior of the Upgrade operation.
type UpgradeOptions struct {
	// ProjectRoot is the project root path whose lock file should be upgraded.
	ProjectRoot string `json:"project_root"`
	// Names restricts the upgrade to specific skill names. Empty means every
	// entry in the lock file.
	Names []string `json:"names,omitempty"`
	// Preview reports what would change without installing (still fetches
	// artifacts to compare digests).
	Preview bool `json:"preview,omitempty"`
	// FailOnChanges exits with an error when any mutable source would upgrade.
	FailOnChanges bool `json:"fail_on_changes,omitempty"`
	// AllowRefChange permits an upgrade whose candidate lives in a different
	// repository. Tag moves within the same repository are always allowed —
	// they are how a mutable source advances.
	AllowRefChange bool `json:"allow_ref_change,omitempty"`
	// AllowSignerChange permits upgrading to an artifact signed by a
	// different identity than the one recorded in the lock file; the new
	// identity is recorded in its place.
	AllowSignerChange bool `json:"allow_signer_change,omitempty"`
	// Clients lists target clients (e.g., "claude-code"). Empty preserves the
	// skill's currently installed client list; when that is also empty,
	// every skill-supporting client detected on this host is used.
	Clients []string `json:"clients,omitempty"`
}

// UpgradeStatus represents the outcome of upgrading a single skill.
type UpgradeStatus string

const (
	// UpgradeStatusUpgraded indicates the skill was installed at a new digest.
	UpgradeStatusUpgraded UpgradeStatus = "upgraded"
	// UpgradeStatusUpToDate indicates the resolved source still points at the pinned digest.
	UpgradeStatusUpToDate UpgradeStatus = "up-to-date"
	// UpgradeStatusNotUpgradable indicates the entry is pinned to an immutable
	// reference (an OCI digest or a full git commit hash) and cannot be upgraded.
	UpgradeStatusNotUpgradable UpgradeStatus = "not-upgradable"
	// UpgradeStatusRefChangeBlocked indicates re-resolution changed resolvedReference.
	UpgradeStatusRefChangeBlocked UpgradeStatus = "ref-change-blocked"
	// UpgradeStatusSignerChangeBlocked indicates the candidate artifact is
	// signed by a different identity (or unsigned) versus the identity the
	// lock file records.
	UpgradeStatusSignerChangeBlocked UpgradeStatus = "signer-change-blocked"
	// UpgradeStatusFailed indicates the upgrade attempt failed.
	UpgradeStatusFailed UpgradeStatus = "failed"
)

// UpgradeOutcome describes the result of attempting to upgrade one skill.
type UpgradeOutcome struct {
	// Name is the skill name.
	Name string `json:"name"`
	// Status is the outcome of the upgrade attempt.
	Status UpgradeStatus `json:"status"`
	// OldDigest is the digest pinned in the lock file before this operation.
	OldDigest string `json:"old_digest,omitempty"`
	// NewDigest is the digest the source currently resolves to. Equal to
	// OldDigest when Status is UpgradeStatusUpToDate.
	NewDigest string `json:"new_digest,omitempty"`
	// NewResolvedReference is the new resolvedReference when it changed.
	NewResolvedReference string `json:"new_resolved_reference,omitempty"`
	// NewSignerIdentity is the candidate's signer identity when it differs
	// from the recorded one (empty when the candidate is unsigned).
	NewSignerIdentity string `json:"new_signer_identity,omitempty"`
	// Reason is a typed failure reason when Status is UpgradeStatusFailed.
	Reason FailureReason `json:"reason,omitempty"`
	// Error is a human-readable description of the failure, set only when Status is UpgradeStatusFailed.
	Error string `json:"error,omitempty"`
}

// UpgradeResult contains the outcome of an Upgrade operation.
type UpgradeResult struct {
	// Outcomes contains one entry per skill considered for upgrade.
	Outcomes []UpgradeOutcome `json:"outcomes"`
}

// LocalBuild represents a locally-built OCI skill artifact in the local store.
type LocalBuild struct {
	// Tag is the OCI tag or name used to reference the artifact.
	Tag string `json:"tag"`
	// Digest is the OCI digest of the artifact (sha256:...).
	Digest string `json:"digest"`
	// Name is the skill name extracted from the artifact metadata, if available.
	Name string `json:"name,omitempty"`
	// Description is the skill description extracted from the artifact metadata, if available.
	Description string `json:"description,omitempty"`
	// Version is the skill version extracted from the artifact metadata, if available.
	Version string `json:"version,omitempty"`
}
