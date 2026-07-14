// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/plugins"
)

// normalizeProjectRoot validates and normalizes the scope/projectRoot pair for
// a plugin operation. Mirror of skillsvc.normalizeProjectRoot; plugins.Scope is
// an alias of skills.Scope, so the normalization rule is identical.
func normalizeProjectRoot(scope plugins.Scope, projectRoot string) (plugins.Scope, string, error) {
	normalizedScope, normalizedRoot, err := plugins.NormalizeScopeAndProjectRoot(scope, projectRoot)
	if err != nil {
		return normalizedScope, normalizedRoot, httperr.WithCode(err, http.StatusBadRequest)
	}
	return normalizedScope, normalizedRoot, nil
}

// defaultScope returns ScopeUser when s is empty, otherwise returns s unchanged.
func defaultScope(s plugins.Scope) plugins.Scope {
	if s == "" {
		return plugins.ScopeUser
	}
	return s
}
