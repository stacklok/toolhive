// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"
	"path/filepath"
)

// absProjectRoot makes a user-supplied --project-root absolute, leaving an
// empty value empty — for most commands that means "not project-scoped", and
// substituting the working directory there would silently change the scope.
//
// The API server requires an absolute project root, so a relative value would
// otherwise travel all the way to the server and come back as a validation
// error naming the wire field rather than the flag the user typed.
//
// Shared by the skill and ai-plugin commands: both take a --project-root flag
// and both validate it through skills.ValidateProjectRoot server-side (the
// plugins package re-exports it), so they need the same normalization.
func absProjectRoot(explicit string) (string, error) {
	if explicit == "" {
		return "", nil
	}
	abs, err := filepath.Abs(explicit)
	if err != nil {
		return "", fmt.Errorf("resolving --project-root %q: %w", explicit, err)
	}
	return abs, nil
}
