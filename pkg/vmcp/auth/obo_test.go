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
	"github.com/stacklok/toolhive/pkg/auth/obo"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// withDefaultOBOFactory captures and restores the package-level OBO strategy
// factory so tests that call RegisterOBOStrategy do not leak state.
func withDefaultOBOFactory(t *testing.T) {
	t.Helper()
	oboMu.RLock()
	original := currentOBOStrategyFactory
	oboMu.RUnlock()
	t.Cleanup(func() {
		oboMu.Lock()
		currentOBOStrategyFactory = original
		oboMu.Unlock()
	})
}

// fakeEnvReader is a sentinel env.Reader used to verify that NewOBOStrategy forwards the reader to the factory.
type fakeEnvReader struct{}

func (*fakeEnvReader) Getenv(_ string) string            { return "" }
func (*fakeEnvReader) LookupEnv(_ string) (string, bool) { return "", false }

// namedStrategy returns a distinct error message to identify which strategy instance was returned.
type namedStrategy struct{ tag string }

func (*namedStrategy) Name() string { return authtypes.StrategyTypeOBO }
func (s *namedStrategy) Authenticate(_ context.Context, _ *http.Request, _ *authtypes.BackendAuthStrategy) error {
	return errors.New(s.tag)
}
func (s *namedStrategy) Validate(_ *authtypes.BackendAuthStrategy) error {
	return errors.New(s.tag)
}

func TestOBOStrategyStub_Name(t *testing.T) {
	t.Parallel()

	s := &oboStrategyStub{}
	assert.Equal(t, authtypes.StrategyTypeOBO, s.Name())
}

func TestOBOStrategyStub_Authenticate_ReturnsEnterpriseRequired(t *testing.T) {
	t.Parallel()

	s := &oboStrategyStub{}
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com", nil)
	require.NoError(t, err)

	err = s.Authenticate(req.Context(), req, &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeOBO})
	require.Error(t, err)
	assert.ErrorIs(t, err, obo.ErrEnterpriseRequired)
}

func TestOBOStrategyStub_Validate_ReturnsEnterpriseRequired(t *testing.T) {
	t.Parallel()

	s := &oboStrategyStub{}
	err := s.Validate(&authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeOBO})
	require.Error(t, err)
	assert.ErrorIs(t, err, obo.ErrEnterpriseRequired)
}

//nolint:paralleltest // Reads package-level currentOBOStrategyFactory; must not race other tests.
func TestNewOBOStrategy_DefaultReturnsStub(t *testing.T) {
	withDefaultOBOFactory(t)

	strategy := NewOBOStrategy(nil)
	require.NotNil(t, strategy)
	assert.Equal(t, authtypes.StrategyTypeOBO, strategy.Name())

	err := strategy.Validate(&authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeOBO})
	assert.ErrorIs(t, err, obo.ErrEnterpriseRequired)
}

//nolint:paralleltest // Mutates package-level currentOBOStrategyFactory; must not race other tests.
func TestRegisterOBOStrategy_ReplacesFactory(t *testing.T) {
	withDefaultOBOFactory(t)

	const tag = "registered-strategy"
	RegisterOBOStrategy(func(_ env.Reader) Strategy {
		return &namedStrategy{tag: tag}
	})

	got := NewOBOStrategy(nil)
	err := got.Validate(&authtypes.BackendAuthStrategy{})
	require.Error(t, err)
	assert.Equal(t, tag, err.Error(), "factory replacement should produce the registered strategy")
	assert.NotErrorIs(t, err, obo.ErrEnterpriseRequired, "registered strategy should not return the stub error")
}

//nolint:paralleltest // Mutates package-level currentOBOStrategyFactory; must not race other tests.
func TestRegisterOBOStrategy_LastWriteWins(t *testing.T) {
	withDefaultOBOFactory(t)

	RegisterOBOStrategy(func(_ env.Reader) Strategy {
		return &namedStrategy{tag: "first"}
	})
	RegisterOBOStrategy(func(_ env.Reader) Strategy {
		return &namedStrategy{tag: "second"}
	})

	got := NewOBOStrategy(nil)
	err := got.Validate(&authtypes.BackendAuthStrategy{})
	require.Error(t, err)
	assert.Equal(t, "second", err.Error(), "last RegisterOBOStrategy call should win")
}

//nolint:paralleltest // Mutates package-level currentOBOStrategyFactory; must not race other tests.
func TestRegisterOBOStrategy_NilPanics(t *testing.T) {
	withDefaultOBOFactory(t)

	assert.Panics(t, func() {
		RegisterOBOStrategy(nil)
	})
}

//nolint:paralleltest // Mutates package-level currentOBOStrategyFactory; must not race other tests.
func TestRegisterOBOStrategy_EnvReaderPassedThrough(t *testing.T) {
	withDefaultOBOFactory(t)

	// Verify the envReader passed to NewOBOStrategy reaches the factory.
	var captured env.Reader
	fakeReader := &fakeEnvReader{}
	RegisterOBOStrategy(func(r env.Reader) Strategy {
		captured = r
		return &oboStrategyStub{}
	})

	NewOBOStrategy(fakeReader)
	assert.Equal(t, fakeReader, captured, "env.Reader must be forwarded to the factory")
}
