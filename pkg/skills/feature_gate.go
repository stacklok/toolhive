// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"os"
	"strings"
)

// LockFileEnvVar gates the project-level skills lock file feature (RFC
// THV-0080) while it lands across multiple PRs. The feature is inert on
// main until every PR in the stack — lock file, sync, upgrade, and Sigstore
// signing/verification — has merged; this keeps each PR mergeable on its own
// without exposing partial, unsigned-by-default behavior in between.
//
// This is intentionally a plain env var, not persisted config or a CLI flag:
// it exists only for the duration of the rollout and is expected to be
// removed once the feature ships, matching the existing TOOLHIVE_DEV /
// TOOLHIVE_REMOTE_HEALTHCHECKS precedent for staged/dev-only behavior.
const LockFileEnvVar = "TOOLHIVE_SKILLS_LOCK_ENABLED"

// LockFileFeatureEnabled reports whether the project-level skills lock file
// feature is enabled for this process.
func LockFileFeatureEnabled() bool {
	return strings.EqualFold(os.Getenv(LockFileEnvVar), "true")
}
