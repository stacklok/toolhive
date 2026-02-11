// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// NewSkillStore creates a SkillStore using OS environment for runtime detection.
func NewSkillStore() (SkillStore, error) {
	return NewSkillStoreWithDetector(&env.OSReader{})
}

// NewSkillStoreWithDetector creates a SkillStore with injected env reader for testability.
func NewSkillStoreWithDetector(envReader env.Reader) (SkillStore, error) {
	if runtime.IsKubernetesRuntimeWithEnv(envReader) {
		return &NoopSkillStore{}, nil
	}
	// TODO: Wire SQLite implementation in PR 2
	return &NoopSkillStore{}, nil
}
