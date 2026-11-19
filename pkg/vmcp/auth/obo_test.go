// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// withNoOBOOverride clears and restores the package-level OBO strategy
// override so tests that call RegisterOBOStrategy do not leak state.
func withNoOBOOverride(t *testing.T) {
	t.Helper()
	oboMu.Lock()
	original := oboStrategyOverride
	oboStrategyOverride = nil
	oboMu.Unlock()
	t.Cleanup(func() {
		oboMu.Lock()
		oboStrategyOverride = original
		oboMu.Unlock()
	})
}

// namedStrategy returns its tag from Validate to identify which instance was returned.
type namedStrategy struct{ tag string }

func (*namedStrategy) Name() string { return authtypes.StrategyTypeOBO }
func (s *namedStrategy) Authenticate(_ context.Context, _ *http.Request, _ *authtypes.BackendAuthStrategy) error {
	return errors.New(s.tag)
}
func (s *namedStrategy) Validate(_ *authtypes.BackendAuthStrategy) error {
	return errors.New(s.tag)
}

//nolint:paralleltest // Mutates package-level oboStrategyOverride; must not race other tests.
func TestNewOBOStrategy_DefaultIsBuiltIn(t *testing.T) {
	withNoOBOOverride(t)

	got := NewOBOStrategy(&env.OSReader{})
	assert.IsType(t, &strategies.OBOStrategy{}, got)
	assert.Equal(t, authtypes.StrategyTypeOBO, got.Name())
}

//nolint:paralleltest // Mutates package-level oboStrategyOverride; must not race other tests.
func TestRegisterOBOStrategy_OverridesBuiltIn(t *testing.T) {
	withNoOBOOverride(t)

	RegisterOBOStrategy(func(_ env.Reader) Strategy { return &namedStrategy{tag: "first"} })
	RegisterOBOStrategy(func(_ env.Reader) Strategy { return &namedStrategy{tag: "second"} })

	err := NewOBOStrategy(nil).Validate(&authtypes.BackendAuthStrategy{})
	require.Error(t, err)
	assert.Equal(t, "second", err.Error(), "the last registered factory must win")
}

func TestRegisterOBOStrategy_PanicsOnNil(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() { RegisterOBOStrategy(nil) })
}
