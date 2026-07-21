// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

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
	// Clients lists target clients (e.g., "claude-code"). Empty means first skill-supporting client.
	Clients []string `json:"clients,omitempty"`
	// Force allows overwriting unmanaged skill directories.
	Force bool `json:"force,omitempty"`
	// ProjectRoot is the project root path for project-scoped installs.
	ProjectRoot string `json:"project_root,omitempty"`
	// Group is the group name to add the skill to after installation.
	Group string `json:"group,omitempty"`
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
	// RequiredByParent is set when this install is a transitively materialized
	// dependency (toolhive.requires) of another skill, naming that parent.
	// Empty means the user explicitly requested this install. Internal use
	// only — NOT exposed via HTTP API.
	RequiredByParent string `json:"-"`
	// Visited tracks skill names already materialized in this dependency
	// tree, preventing infinite recursion on a requires cycle. Left nil by
	// external callers; Install initializes it on first entry and threads it
	// through recursive dependency installs. Internal use only — NOT exposed
	// via HTTP API.
	Visited map[string]struct{} `json:"-"`
	// SyncRestore forces re-extraction to every existing client even when
	// Digest matches the currently-installed digest. Set by Sync when
	// reinstalling at a pinned reference: the whole point is repairing
	// on-disk drift that happened without the pinned digest changing, so the
	// normal "same digest means content is already correct" fast path must
	// not apply. Internal use only — NOT exposed via HTTP API.
	SyncRestore bool `json:"-"`
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
	FailureReasonUnknown             FailureReason = "unknown"
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
	// AllowRefChange permits resolvedReference changes during upgrade.
	AllowRefChange bool `json:"allow_ref_change,omitempty"`
	// Clients lists target clients (e.g., "claude-code"). Empty means every
	// skill-supporting client detected on this host.
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
