// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package plugins

import (
	"context"

	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// ListOptions configures the behavior of the List operation. Alias for
// skills.ListOptions (identical shape: Scope, ClientApp, ProjectRoot, Group).
type ListOptions = skills.ListOptions

// InstallOptions configures the behavior of the Install operation. Mirrors
// skills.InstallOptions with the plugin-specific addition of Reference/Digest
// passthrough fields used by install-from-OCI flows.
type InstallOptions struct {
	// Name is the plugin name or OCI reference to install.
	Name string `json:"name"`
	// Version is the specific version to install. Empty means latest.
	Version string `json:"version,omitempty"`
	// Scope is the installation scope.
	Scope Scope `json:"scope,omitempty"`
	// Clients lists target clients (e.g., "claude-code").
	Clients []string `json:"clients,omitempty"`
	// Force allows overwriting unmanaged plugin directories.
	Force bool `json:"force,omitempty"`
	// ProjectRoot is the project root path for project-scoped installs.
	ProjectRoot string `json:"project_root,omitempty"`
	// Group is the group name to add the plugin to after installation.
	Group string `json:"group,omitempty"`
	// AllowUnsigned permits installing a project-scoped plugin whose
	// artifact carries no Sigstore signature. Without it, unsigned
	// artifacts are rejected; with it, the lock entry records the exception
	// as "unsigned: true". A plugin contributes hooks, agents, and MCP
	// servers to the client that loads it, so this is an explicit
	// per-install trust decision, never a default.
	AllowUnsigned bool `json:"allow_unsigned,omitempty"`
	// LayerData is the tar.gz content from an OCI layer. Internal use only — NOT exposed via HTTP API.
	LayerData []byte `json:"-"`
	// Reference is the full OCI reference (e.g. ghcr.io/org/plugin:v1).
	Reference string `json:"-"`
	// Digest is the OCI digest for upgrade detection.
	Digest string `json:"-"`
	// Components is the plugin's component inventory, hydrated from the OCI
	// artifact config by install-from-OCI/git flows. Internal use only.
	Components ComponentInventory `json:"-"`
	// Dependencies is the plugin's external dependency list, hydrated from the
	// OCI artifact config. Internal use only.
	Dependencies []Dependency `json:"-"`
	// Tag is the OCI tag, hydrated from the resolved reference. Internal use only.
	Tag string `json:"-"`
	// Description is the plugin description, hydrated from the OCI artifact
	// config or git manifest. Internal use only.
	Description string `json:"-"`
	// LockSource overrides the value recorded as the lock entry's Source. When
	// empty, the entry's Source is Name as given by the caller before any
	// internal resolution. Set by Sync/Upgrade, which pass an already-resolved
	// Name that must not overwrite the entry's original Source. Internal use
	// only — NOT exposed via HTTP API.
	LockSource string `json:"-"`
	// LockResolvedReference overrides the value recorded as the lock entry's
	// ResolvedReference. When empty, the entry's ResolvedReference is
	// whatever this install actually resolved to. Set by Sync when
	// reinstalling at a pinned reference. Internal use only — NOT exposed
	// via HTTP API.
	LockResolvedReference string `json:"-"`
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
	// explicitly overridden. NOT exposed via HTTP API.
	AllowSignerChange bool `json:"-"`
	// ExpectedCanonicalName, when set, requires the resolved plugin/manifest name
	// to equal this value before any install mutation. Used by Sync/Upgrade so a
	// lock entry cannot be repaired under a different canonical identity.
	ExpectedCanonicalName string `json:"-"`
	// Unsigned records the trust decision that this install proceeded
	// without a verified signature (via AllowUnsigned). Set internally by
	// install-time verification; recorded as `unsigned: true` in the lock
	// entry. Internal use only — NOT exposed via HTTP API.
	Unsigned bool `json:"-"`
	// Provenance carries the verified signer identity established during
	// install-time verification, for recording into the lock entry. Set by
	// the verification step, nil when the artifact is unsigned or
	// verification did not run. Unlike skills.InstallOptions, this is the
	// lock file's own shape: plugins have no API-facing provenance type to
	// convert through yet, and a conversion pair that exists only to be
	// round-tripped is a place for recorded trust data to get dropped.
	// Internal use only — NOT exposed via HTTP API.
	Provenance *lockfile.Provenance `json:"-"`
	// SigstoreBundle is the serialized Sigstore bundle backing Provenance,
	// persisted alongside the install record so sync can re-verify offline.
	// Internal use only — NOT exposed via HTTP API.
	SigstoreBundle []byte `json:"-"`
}

// InstallResult contains the outcome of an Install operation.
type InstallResult struct {
	// Plugin is the installed plugin.
	Plugin InstalledPlugin `json:"plugin"`
	// PreExisting is the store record as it was before this install, or nil
	// when this install created the record. Rollback uses it to restore the
	// previous state instead of destructively deleting a record this call
	// did not create. Internal use only — NOT exposed via HTTP API.
	PreExisting *InstalledPlugin `json:"-"`
	// ContentDigest is the dirhash of the canonical plugin tree (ExtractPlugin
	// output), computed at install time for recording in the lock file.
	// Internal use only — NOT exposed via HTTP API.
	ContentDigest string `json:"-"`
	// RestoreFiles undoes this install's on-disk writes: dematerialize a
	// fresh install, or restore the previous tree after a failed upgrade.
	// Callers must join a non-nil error into the failure they are
	// compensating; discarding it can hide a partial restore. Internal use
	// only — NOT exposed via HTTP API.
	RestoreFiles func(context.Context) error `json:"-"`
}

// UninstallOptions configures the behavior of the Uninstall operation. Alias
// for skills.UninstallOptions (identical shape).
type UninstallOptions = skills.UninstallOptions

// InfoOptions configures the behavior of the Info operation. Alias for
// skills.InfoOptions.
type InfoOptions = skills.InfoOptions

// PluginInfo contains detailed information about an installed plugin.
type PluginInfo struct {
	// Metadata contains the plugin's metadata.
	Metadata PluginMetadata `json:"metadata"`
	// InstalledPlugin contains the full installation record.
	InstalledPlugin *InstalledPlugin `json:"installed_plugin,omitempty"`
	// UnmaterializedComponents lists, per client type, the component types the
	// plugin declares that the installed client adapter does NOT load. Populated
	// by Info by diffing InstalledPlugin.Components against each installed
	// client adapter's SupportedComponents.
	UnmaterializedComponents map[string][]ComponentType `json:"unmaterialized_components,omitempty"`
	// ProjectScopeDegradedClients lists the client types for which a
	// project-scoped install degraded (the adapter could only materialize at
	// user scope — e.g. Codex always writes to the user-scoped config.toml).
	// Populated by Info; empty for user-scoped installs. Recomputed at read
	// time from the stored scope + each adapter's capability, mirroring the
	// UnmaterializedComponents pattern (no persistence needed — the degradation
	// is deterministic from scope + client type).
	ProjectScopeDegradedClients []string `json:"project_scope_degraded_clients,omitempty"`
}

// ContentOptions configures the behavior of the GetContent operation. Alias
// for skills.ContentOptions.
type ContentOptions = skills.ContentOptions

// BuildOptions configures the behavior of the Build operation. Alias for
// skills.BuildOptions (Path, Tag).
type BuildOptions = skills.BuildOptions

// BuildResult contains the outcome of a Build operation. Alias for
// skills.BuildResult (Reference).
type BuildResult = skills.BuildResult

// PushOptions configures the behavior of the Push operation. Alias for
// skills.PushOptions (Reference).
type PushOptions = skills.PushOptions

// SyncOptions configures a lock-file sync. Alias for skills.SyncOptions
// (identical shape: ProjectRoot, Clients, Prune, Check, AllowUnsigned, Adopt).
type SyncOptions = skills.SyncOptions

// SyncResult is the outcome of a lock-file sync. Alias for skills.SyncResult.
type SyncResult = skills.SyncResult

// SyncFailure describes one plugin that failed to sync. Alias for
// skills.SyncFailure.
type SyncFailure = skills.SyncFailure

// FailureReason is a typed sync/upgrade failure reason. Alias for
// skills.FailureReason.
type FailureReason = skills.FailureReason

// Typed failure reasons for sync/upgrade operations. Aliased from
// skills.FailureReason because the RFC THV-0080 contract is shared.
const (
	FailureReasonRegistryUnreachable = skills.FailureReasonRegistryUnreachable
	FailureReasonDigestMissing       = skills.FailureReasonDigestMissing
	FailureReasonValidationRejected  = skills.FailureReasonValidationRejected
	FailureReasonLockWriteFailed     = skills.FailureReasonLockWriteFailed
	FailureReasonSignatureInvalid    = skills.FailureReasonSignatureInvalid
	FailureReasonSignerMismatch      = skills.FailureReasonSignerMismatch
	FailureReasonKeySigned           = skills.FailureReasonKeySigned
	FailureReasonUnsignedRejected    = skills.FailureReasonUnsignedRejected
	FailureReasonUnknown             = skills.FailureReasonUnknown

	// FailureReasonProvenanceFieldMismatch means the artifact verifies
	// against the recorded signer, but a pinned certificate field (the
	// repository ref or runner environment) no longer matches.
	FailureReasonProvenanceFieldMismatch = skills.FailureReasonProvenanceFieldMismatch
)

// UpgradeOptions configures a lock-file upgrade. Alias for
// skills.UpgradeOptions (identical shape including AllowRefChange /
// AllowSignerChange).
type UpgradeOptions = skills.UpgradeOptions

// UpgradeResult is the outcome of a lock-file upgrade. Alias for
// skills.UpgradeResult.
type UpgradeResult = skills.UpgradeResult

// UpgradeOutcome describes one plugin considered for upgrade. Alias for
// skills.UpgradeOutcome.
type UpgradeOutcome = skills.UpgradeOutcome

// UpgradeStatus is the per-entry upgrade outcome. Alias for
// skills.UpgradeStatus.
type UpgradeStatus = skills.UpgradeStatus

// Per-entry upgrade outcomes. Aliased from skills.UpgradeStatus because the
// RFC THV-0080 contract is shared.
const (
	UpgradeStatusUpgraded            = skills.UpgradeStatusUpgraded
	UpgradeStatusUpToDate            = skills.UpgradeStatusUpToDate
	UpgradeStatusNotUpgradable       = skills.UpgradeStatusNotUpgradable
	UpgradeStatusRefChangeBlocked    = skills.UpgradeStatusRefChangeBlocked
	UpgradeStatusSignerChangeBlocked = skills.UpgradeStatusSignerChangeBlocked
	UpgradeStatusFailed              = skills.UpgradeStatusFailed
)
