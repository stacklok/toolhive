// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllowAllGate_CheckCreateServer(t *testing.T) {
	t.Parallel()

	gate := allowAllGate{}
	err := gate.CheckCreateServer(context.Background(), NewRunConfig())
	assert.NoError(t, err)
}

func TestNoopPolicyGate_CheckCreateServer(t *testing.T) {
	t.Parallel()

	gate := NoopPolicyGate{}
	err := gate.CheckCreateServer(context.Background(), NewRunConfig())
	assert.NoError(t, err)
}

func TestRegisterPolicyGate(t *testing.T) {
	t.Parallel()

	// Save and restore the original gate after the test.
	policyGateMu.Lock()
	original := policyGate
	policyGateMu.Unlock()
	t.Cleanup(func() {
		policyGateMu.Lock()
		policyGate = original
		policyGateMu.Unlock()
	})

	sentinel := errors.New("blocked by test policy")
	denyGate := &errorPolicyGate{err: sentinel}

	RegisterPolicyGate(denyGate)

	got := ActivePolicyGate()
	require.Equal(t, denyGate, got)

	err := got.CheckCreateServer(context.Background(), NewRunConfig())
	require.ErrorIs(t, err, sentinel)
}

func TestActivePolicyGate_DefaultIsAllowAll(t *testing.T) {
	t.Parallel()

	// Save and restore gate so parallel tests are not affected.
	policyGateMu.Lock()
	original := policyGate
	policyGateMu.Unlock()
	t.Cleanup(func() {
		policyGateMu.Lock()
		policyGate = original
		policyGateMu.Unlock()
	})

	// Reset to the package default for this subtest.
	policyGateMu.Lock()
	policyGate = allowAllGate{}
	policyGateMu.Unlock()

	got := ActivePolicyGate()
	assert.IsType(t, allowAllGate{}, got)

	err := got.CheckCreateServer(context.Background(), NewRunConfig())
	assert.NoError(t, err)
}

// errorPolicyGate is a test helper that always returns the configured error.
type errorPolicyGate struct {
	NoopPolicyGate
	err error
}

func (g *errorPolicyGate) CheckCreateServer(_ context.Context, _ *RunConfig) error {
	return g.err
}
