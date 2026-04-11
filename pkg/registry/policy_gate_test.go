// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoopPolicyGate_CheckUpdateRegistry(t *testing.T) {
	t.Parallel()

	g := NoopPolicyGate{}
	err := g.CheckUpdateRegistry(context.Background(), &UpdateRegistryConfig{URL: "https://example.com"})
	assert.NoError(t, err)
}

func TestNoopPolicyGate_CheckDeleteRegistry(t *testing.T) {
	t.Parallel()

	g := NoopPolicyGate{}
	err := g.CheckDeleteRegistry(context.Background(), &DeleteRegistryConfig{Name: "default"})
	assert.NoError(t, err)
}

// errorPolicyGate is a test helper that always returns the configured error.
type errorPolicyGate struct {
	NoopPolicyGate
	err error
}

func (g *errorPolicyGate) CheckUpdateRegistry(_ context.Context, _ *UpdateRegistryConfig) error {
	return g.err
}

func (g *errorPolicyGate) CheckDeleteRegistry(_ context.Context, _ *DeleteRegistryConfig) error {
	return g.err
}

//nolint:paralleltest // Mutates global registry policy gate singleton
func TestRegisterPolicyGate_BlocksUpdate(t *testing.T) {
	regGateMu.Lock()
	original := regGate
	regGateMu.Unlock()
	t.Cleanup(func() {
		regGateMu.Lock()
		regGate = original
		regGateMu.Unlock()
	})

	sentinel := errors.New("blocked by test policy")
	RegisterPolicyGate(&errorPolicyGate{err: sentinel})

	got := ActivePolicyGate()
	err := got.CheckUpdateRegistry(context.Background(), &UpdateRegistryConfig{
		URL: "https://example.com/registry.json",
	})
	require.ErrorIs(t, err, sentinel)
}

//nolint:paralleltest // Mutates global registry policy gate singleton
func TestRegisterPolicyGate_BlocksDelete(t *testing.T) {
	regGateMu.Lock()
	original := regGate
	regGateMu.Unlock()
	t.Cleanup(func() {
		regGateMu.Lock()
		regGate = original
		regGateMu.Unlock()
	})

	sentinel := errors.New("blocked by test policy")
	RegisterPolicyGate(&errorPolicyGate{err: sentinel})

	err := ActivePolicyGate().CheckDeleteRegistry(context.Background(), &DeleteRegistryConfig{
		Name: "default",
	})
	require.ErrorIs(t, err, sentinel)
}

//nolint:paralleltest // Mutates global registry policy gate singleton
func TestActivePolicyGate_DefaultIsAllowAll(t *testing.T) {
	regGateMu.Lock()
	original := regGate
	regGateMu.Unlock()
	t.Cleanup(func() {
		regGateMu.Lock()
		regGate = original
		regGateMu.Unlock()
	})

	// Reset to the package default for this subtest.
	regGateMu.Lock()
	regGate = allowAllGate{}
	regGateMu.Unlock()

	got := ActivePolicyGate()
	assert.IsType(t, allowAllGate{}, got)

	assert.NoError(t, got.CheckUpdateRegistry(context.Background(), &UpdateRegistryConfig{}))
	assert.NoError(t, got.CheckDeleteRegistry(context.Background(), &DeleteRegistryConfig{}))
}

//nolint:paralleltest // Mutates global registry policy gate singleton
func TestRegisterPolicyGate_ReceivesUpdateConfig(t *testing.T) {
	regGateMu.Lock()
	original := regGate
	regGateMu.Unlock()
	t.Cleanup(func() {
		regGateMu.Lock()
		regGate = original
		regGateMu.Unlock()
	})

	var received UpdateRegistryConfig
	RegisterPolicyGate(&captureUpdateGate{captured: &received})

	_ = ActivePolicyGate().CheckUpdateRegistry(context.Background(), &UpdateRegistryConfig{
		URL:            "https://example.com/registry.json",
		AllowPrivateIP: true,
		HasAuth:        true,
	})

	assert.Equal(t, "https://example.com/registry.json", received.URL)
	assert.True(t, received.AllowPrivateIP)
	assert.True(t, received.HasAuth)
}

//nolint:paralleltest // Mutates global registry policy gate singleton
func TestRegisterPolicyGate_ReceivesDeleteConfig(t *testing.T) {
	regGateMu.Lock()
	original := regGate
	regGateMu.Unlock()
	t.Cleanup(func() {
		regGateMu.Lock()
		regGate = original
		regGateMu.Unlock()
	})

	var received DeleteRegistryConfig
	RegisterPolicyGate(&captureDeleteGate{captured: &received})

	_ = ActivePolicyGate().CheckDeleteRegistry(context.Background(), &DeleteRegistryConfig{
		Name: "custom",
	})

	assert.Equal(t, "custom", received.Name)
}

// captureUpdateGate records the UpdateRegistryConfig it receives.
type captureUpdateGate struct {
	NoopPolicyGate
	captured *UpdateRegistryConfig
}

func (g *captureUpdateGate) CheckUpdateRegistry(_ context.Context, cfg *UpdateRegistryConfig) error {
	*g.captured = *cfg
	return nil
}

// captureDeleteGate records the DeleteRegistryConfig it receives.
type captureDeleteGate struct {
	NoopPolicyGate
	captured *DeleteRegistryConfig
}

func (g *captureDeleteGate) CheckDeleteRegistry(_ context.Context, cfg *DeleteRegistryConfig) error {
	*g.captured = *cfg
	return nil
}
