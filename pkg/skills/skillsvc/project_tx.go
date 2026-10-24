// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"github.com/stacklok/toolhive/pkg/projecttxn"
)

const projectTxLockDir = "skills-project-locks"

// projectTxLockPath places stable transaction locks in ToolHive's state
// directory. The bounded stripe filename gives every process using the same
// ToolHive state a deterministic path without writing lock artifacts into
// the project or following a repository-controlled gitdir path.
func projectTxLockPath(projectRoot string) (string, error) {
	return projecttxn.LockPath(projectRoot)
}
