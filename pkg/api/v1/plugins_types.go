// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import "github.com/stacklok/toolhive/pkg/plugins"

// pluginListResponse represents the response for listing plugins.
//
//	@Description	Response containing a list of installed plugins
type pluginListResponse struct {
	// List of installed plugins
	Plugins []plugins.InstalledPlugin `json:"plugins"`
}

// installPluginRequest represents the request to install a plugin.
//
//	@Description	Request to install a plugin
type installPluginRequest struct {
	// Name or OCI reference of the plugin to install
	Name string `json:"name"`
	// Version to install (empty means latest)
	Version string `json:"version,omitempty"`
	// Scope for the installation
	Scope plugins.Scope `json:"scope,omitempty"`
	// ProjectRoot is the project root path for project-scoped installs
	ProjectRoot string `json:"project_root,omitempty"`
	// Clients lists target client identifiers (e.g., "claude-code"),
	// or ["all"] to target every plugin-supporting client.
	// Omitting this field installs to all available clients.
	Clients []string `json:"clients,omitempty"`
	// Force allows overwriting unmanaged plugin directories
	Force bool `json:"force,omitempty"`
	// AllowUnsigned permits installing a project-scoped plugin without a
	// verified signature; the exception is recorded in the project's lock
	// file.
	AllowUnsigned bool `json:"allow_unsigned,omitempty"`
	// PublicKey is the base64-encoded DER SPKI cosign public key the artifact
	// must verify against, for artifacts signed with a cosign key pair rather
	// than keylessly. Required the first time such an artifact is installed
	// project-scoped, and pinned in the lock file from then on.
	PublicKey string `json:"public_key,omitempty"`
	// Group is the group name to add the plugin to after installation
	Group string `json:"group,omitempty"`
}

// installPluginResponse represents the response after installing a plugin.
//
//	@Description	Response after successfully installing a plugin
type installPluginResponse struct {
	// The installed plugin
	Plugin plugins.InstalledPlugin `json:"plugin"`
	// The signer identity trust-on-first-use pinned for this install, when
	// the artifact was verified. Omitted for unsigned or non-lock-managed
	// installs.
	Provenance *plugins.ProvenanceInfo `json:"provenance,omitempty"`
	// Whether the install was recorded as an explicit unsigned exception.
	Unsigned bool `json:"unsigned,omitempty"`
}

// validatePluginRequest represents the request to validate a plugin.
//
//	@Description	Request to validate a plugin definition
type validatePluginRequest struct {
	// Path to the plugin definition directory
	Path string `json:"path"`
}

// buildPluginRequest represents the request to build a plugin.
//
//	@Description	Request to build a plugin from a local directory
type buildPluginRequest struct {
	// Path to the plugin definition directory
	Path string `json:"path"`
	// OCI tag for the built artifact
	Tag string `json:"tag,omitempty"`
}

// pushPluginRequest represents the request to push a plugin.
//
// The signing choice is mutually exclusive and not optional: exactly one of
// key, identity_token, or no_sign must be set. Swagger 2.0 cannot express
// "exactly one of", so it is stated here and enforced at runtime by the
// handler before dispatch, and again by the service
// (plugins.ValidatePushSigning, HTTP 400). Unknown fields are still
// rejected: this is the only credential-bearing plugin request, so a
// misspelled signing field must not decode to "sign however you like".
//
//	@Description	Request to push a built plugin artifact. Exactly one of key, identity_token, or no_sign is required.
type pushPluginRequest struct {
	// OCI reference to push
	Reference string `json:"reference" binding:"required"`
	// Key is the path to a cosign private key, resolved on the server's
	// filesystem. Accepted only when the request carries the secret capability
	// from the owner-protected local server discovery file; other requests are
	// refused with 403, since honoring one would let an untrusted caller have
	// the server sign with any key it can read. Use IdentityToken when calling
	// a remote or manually configured server. Consumers installing the result
	// project-scoped must supply the matching public key on first use
	// (install's public_key).
	Key string `json:"key,omitempty"`
	// IdentityToken is a short-lived OIDC identity token used for keyless
	// signing, mutually exclusive with Key
	IdentityToken string `json:"identity_token,omitempty"`
	// NoSign pushes without signing
	NoSign bool `json:"no_sign,omitempty"`
}

// pluginBuildListResponse represents the response for listing locally-built OCI plugin artifacts.
//
//	@Description	Response containing a list of locally-built OCI plugin artifacts
type pluginBuildListResponse struct {
	// List of locally-built OCI plugin artifacts
	Builds []plugins.LocalBuild `json:"builds"`
}

// syncPluginsRequest represents the request to sync a project's plugins.
//
//	@Description	Request to restore a project's installed plugins to match its lock file
type syncPluginsRequest struct {
	// ProjectRoot is the project root path whose lock file should be synced
	ProjectRoot string `json:"project_root"`
	// Clients lists target client identifiers. Empty means every
	// plugin-supporting client detected on this host.
	Clients []string `json:"clients,omitempty"`
	// Prune removes project-scoped plugins installed but not present in the lock file
	Prune bool `json:"prune,omitempty"`
	// Check verifies on-disk content against the lock file without installing or writing anything
	Check bool `json:"check,omitempty"`
	// Adopt writes lock entries for existing unmanaged project-scope installs
	Adopt bool `json:"adopt,omitempty"`
	// AllowUnsigned permits recording a plugin as unsigned in the lock file,
	// in two cases: adopting an install whose signature state cannot be
	// established (see Adopt), and repairing an entry that records no trust
	// decision at all, whose reinstall otherwise fails closed on unsigned
	// content.
	AllowUnsigned bool `json:"allow_unsigned,omitempty"`
}

// upgradePluginsRequest represents the request to upgrade a project's plugins.
//
//	@Description	Request to re-resolve a project's lock entries and install newer content
type upgradePluginsRequest struct {
	// ProjectRoot is the project root path whose lock file should be upgraded
	ProjectRoot string `json:"project_root"`
	// Names restricts the upgrade to specific plugin names. Empty means every entry.
	Names []string `json:"names,omitempty"`
	// Preview reports what would change without installing (still fetches to compare digests)
	Preview bool `json:"preview,omitempty"`
	// FailOnChanges exits with an error when any mutable source would upgrade
	FailOnChanges bool `json:"fail_on_changes,omitempty"`
	// AllowRefChange permits resolvedReference changes during upgrade
	AllowRefChange bool `json:"allow_ref_change,omitempty"`
	// AllowSignerChange permits upgrading to an artifact signed by a
	// different identity than the recorded one
	AllowSignerChange bool `json:"allow_signer_change,omitempty"`
	// Clients lists target client identifiers. Empty means every
	// plugin-supporting client detected on this host.
	Clients []string `json:"clients,omitempty"`
}
