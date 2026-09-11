// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import "github.com/stacklok/toolhive/pkg/skills"

// --- request/response dto (mirror pkg/api/v1/skills_types.go) ---

type installRequest struct {
	Name        string       `json:"name"`
	Version     string       `json:"version,omitempty"`
	Scope       skills.Scope `json:"scope,omitempty"`
	ProjectRoot string       `json:"project_root,omitempty"`
	Clients     []string     `json:"clients,omitempty"`
	Force       bool         `json:"force,omitempty"`
	Group       string       `json:"group,omitempty"`
	// AllowUnsigned mirrors skills.InstallOptions.AllowUnsigned; without it
	// here the CLI flag would silently never reach the server.
	AllowUnsigned bool `json:"allow_unsigned,omitempty"`
	// PublicKey mirrors skills.InstallOptions.PublicKey: the base64 DER SPKI
	// the CLI encoded from the --public-key file, since a path would not
	// resolve on a server in another process or on another host.
	PublicKey string `json:"public_key,omitempty"`
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
	Key       string `json:"key,omitempty"`
	// IdentityToken mirrors skills.PushOptions.IdentityToken; without it here
	// the --identity-token flag would silently never reach the server.
	IdentityToken string `json:"identity_token,omitempty"`
	NoSign        bool   `json:"no_sign,omitempty"`
}

type listResponse struct {
	Skills []skills.InstalledSkill `json:"skills"`
}

type installResponse struct {
	Skill skills.InstalledSkill `json:"skill"`
	// Provenance and Unsigned mirror installSkillResponse. Without them the
	// CLI — which is a pure HTTP client — could never report the trust state
	// the server recorded, and would silently print every install as if it
	// were untracked.
	Provenance *skills.ProvenanceInfo `json:"provenance,omitempty"`
	Unsigned   bool                   `json:"unsigned,omitempty"`
}

type listBuildsResponse struct {
	Builds []skills.LocalBuild `json:"builds"`
}

type syncRequest struct {
	ProjectRoot string   `json:"project_root"`
	Clients     []string `json:"clients,omitempty"`
	Prune       bool     `json:"prune,omitempty"`
	Check       bool     `json:"check,omitempty"`
	Adopt       bool     `json:"adopt,omitempty"`
	// AllowUnsigned mirrors skills.SyncOptions.AllowUnsigned for adoption.
	AllowUnsigned bool `json:"allow_unsigned,omitempty"`
}

type upgradeRequest struct {
	ProjectRoot string   `json:"project_root"`
	Names       []string `json:"names,omitempty"`
	// AllowSignerChange mirrors skills.UpgradeOptions.AllowSignerChange.
	AllowSignerChange bool     `json:"allow_signer_change,omitempty"`
	Preview           bool     `json:"preview,omitempty"`
	FailOnChanges     bool     `json:"fail_on_changes,omitempty"`
	AllowRefChange    bool     `json:"allow_ref_change,omitempty"`
	Clients           []string `json:"clients,omitempty"`
}
