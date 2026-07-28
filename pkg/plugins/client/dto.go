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
}

type listResponse struct {
	Plugins []plugins.InstalledPlugin `json:"plugins"`
}

type installResponse struct {
	Plugin plugins.InstalledPlugin `json:"plugin"`
}

type listBuildsResponse struct {
	Builds []plugins.LocalBuild `json:"builds"`
}
