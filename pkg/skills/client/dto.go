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
	Client      string       `json:"client,omitempty"`
	Force       bool         `json:"force,omitempty"`
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
	Skills []skills.InstalledSkill `json:"skills"`
}

type installResponse struct {
	Skill skills.InstalledSkill `json:"skill"`
}
