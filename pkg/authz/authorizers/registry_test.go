// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// mockFactory is a test implementation of AuthorizerFactory
type mockFactory struct {
	validateErr error
	createErr   error
	authorizer  Authorizer
}

func (f *mockFactory) ValidateConfig(_ json.RawMessage) error {
	return f.validateErr
}

func (f *mockFactory) CreateAuthorizer(_ json.RawMessage, _ string) (Authorizer, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.authorizer, nil
}

// mockAuthorizer is a test implementation of Authorizer
type mockAuthorizer struct{}

func (*mockAuthorizer) AuthorizeWithJWTClaims(
	_ context.Context,
	_ MCPFeature,
	_ MCPOperation,
	_ string,
	_ map[string]interface{},
) (bool, error) {
	return true, nil
}

func TestGetFactory(t *testing.T) {
	t.Parallel()

	// Test getting a non-existent factory
	factory := GetFactory("nonexistent")
	assert.Nil(t, factory, "Expected nil for non-existent factory")
}

func TestIsRegistered(t *testing.T) {
	t.Parallel()

	// Test non-existent type
	assert.False(t, IsRegistered("nonexistent"), "Expected false for non-existent type")
}

func TestRegisteredTypes(t *testing.T) {
	t.Parallel()

	// RegisteredTypes should return a list (even if empty)
	types := RegisteredTypes()
	assert.NotNil(t, types, "Expected non-nil list of types")
}

//nolint:paralleltest // This test modifies global registry state and cannot be parallelized
func TestRegisterNewType(t *testing.T) {
	// Register a new type that doesn't exist
	testType := "test-authorizer-type-unique"

	// First verify it's not registered (might already be from a previous test run, skip if so)
	if IsRegistered(testType) {
		t.Skip("Type already registered from previous test run")
	}

	// Register the new type
	mockFactory := &mockFactory{
		authorizer: &mockAuthorizer{},
	}
	Register(testType, mockFactory)

	// Verify it's now registered
	assert.True(t, IsRegistered(testType), "Type should be registered after Register")

	// Verify we can get the factory
	factory := GetFactory(testType)
	assert.NotNil(t, factory, "Factory should be retrievable")
	assert.Equal(t, mockFactory, factory, "Factory should match what was registered")

	// Verify it appears in RegisteredTypes
	types := RegisteredTypes()
	found := false
	for _, typ := range types {
		if typ == testType {
			found = true
			break
		}
	}
	assert.True(t, found, "Expected %s to be in registered types", testType)
}

//nolint:paralleltest // This test modifies global registry state and cannot be parallelized
func TestRegisterPanicsOnDuplicate(t *testing.T) {
	// Register a unique type for this test
	testType := "test-authorizer-type-duplicate-check"

	// Skip if already registered from a previous test run
	if IsRegistered(testType) {
		// Type already exists, directly test the panic case
		assert.Panics(t, func() {
			Register(testType, &mockFactory{})
		}, "Expected panic when registering duplicate factory")
		return
	}

	// First register a new type
	Register(testType, &mockFactory{
		authorizer: &mockAuthorizer{},
	})

	// Trying to register it again should panic
	assert.Panics(t, func() {
		Register(testType, &mockFactory{})
	}, "Expected panic when registering duplicate factory")
}
