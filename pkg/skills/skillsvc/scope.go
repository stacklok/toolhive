// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
)

func normalizeProjectRoot(scope skills.Scope, projectRoot string) (skills.Scope, string, error) {
	normalizedScope, normalizedRoot, err := skills.NormalizeScopeAndProjectRoot(scope, projectRoot)
	if err != nil {
		return normalizedScope, normalizedRoot, httperr.WithCode(err, http.StatusBadRequest)
	}
	return normalizedScope, normalizedRoot, nil
}

// defaultScope returns ScopeUser when s is empty, otherwise returns s unchanged.
func defaultScope(s skills.Scope) skills.Scope {
	if s == "" {
		return skills.ScopeUser
	}
	return s
}
