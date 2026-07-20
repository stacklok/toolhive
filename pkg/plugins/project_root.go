// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package plugins

import "github.com/stacklok/toolhive/pkg/skills"

// ValidateProjectRoot validates a project root path and returns its cleaned
// form. Re-exported from skills: project-root validation (absolute, no
// traversal, no symlinks, must be a git repo) is identical for plugins.
var ValidateProjectRoot = skills.ValidateProjectRoot

// NormalizeScopeAndProjectRoot validates scope and project_root and returns
// normalized values. Re-exported from skills (the scope/project_root
// normalization rule is identical for plugins).
var NormalizeScopeAndProjectRoot = skills.NormalizeScopeAndProjectRoot
