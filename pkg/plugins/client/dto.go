// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import "github.com/stacklok/toolhive/pkg/plugins"

// --- request/response dto (mirror pkg/api/v1/plugins_types.go) ---

type installRequest struct {
	Name        string        `json:"name"`
	Version     string        `json:"version,omitempty"`
	Scope       plugins.Scope `json:"scope,omitempty"`
	ProjectRoot string        `json:"project_root,omitempty"`
	Clients     []string      `json:"clients,omitempty"`
	Force       bool          `json:"force,omitempty"`
	Group       string        `json:"group,omitempty"`
	// AllowUnsigned mirrors plugins.InstallOptions.AllowUnsigned; without
	// it here the CLI flag would silently never reach the server.
	AllowUnsigned bool `json:"allow_unsigned,omitempty"`
}

type validateRequest struct {
	Path string `json:"path"`
}

type buildRequest struct {
	Path string `json:"path"`
	Tag  string `json:"tag,omitempty"`
}

type pushRequest struct {
	Reference string `json:"reference"`
	// IdentityToken and NoSign mirror pushPluginRequest. Without them here the
	// CLI's signing flags would be dropped at the HTTP boundary and every push
	// would be rejected as missing a signing credential. There is no key field:
	// plugin signing is keyless-only (#6442).
	IdentityToken string `json:"identity_token,omitempty"`
	NoSign        bool   `json:"no_sign,omitempty"`
}

type listResponse struct {
	Plugins []plugins.InstalledPlugin `json:"plugins"`
}

type installResponse struct {
	Plugin plugins.InstalledPlugin `json:"plugin"`
	// Provenance and Unsigned mirror installPluginResponse. Without them the
	// CLI — which is a pure HTTP client — could never report the trust state
	// the server recorded, and would silently print every install as if it
	// were untracked.
	Provenance *plugins.ProvenanceInfo `json:"provenance,omitempty"`
	Unsigned   bool                    `json:"unsigned,omitempty"`
}

type listBuildsResponse struct {
	Builds []plugins.LocalBuild `json:"builds"`
}

type syncRequest struct {
	ProjectRoot string   `json:"project_root"`
	Clients     []string `json:"clients,omitempty"`
	Prune       bool     `json:"prune,omitempty"`
	Check       bool     `json:"check,omitempty"`
	Adopt       bool     `json:"adopt,omitempty"`
	// AllowUnsigned mirrors plugins.SyncOptions.AllowUnsigned for adoption.
	AllowUnsigned bool `json:"allow_unsigned,omitempty"`
}

type upgradeRequest struct {
	ProjectRoot string   `json:"project_root"`
	Names       []string `json:"names,omitempty"`
	// AllowSignerChange mirrors plugins.UpgradeOptions.AllowSignerChange.
	AllowSignerChange bool     `json:"allow_signer_change,omitempty"`
	Preview           bool     `json:"preview,omitempty"`
	FailOnChanges     bool     `json:"fail_on_changes,omitempty"`
	AllowRefChange    bool     `json:"allow_ref_change,omitempty"`
	Clients           []string `json:"clients,omitempty"`
}
