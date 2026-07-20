// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package obo

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrEnterpriseRequired_IsSentinel(t *testing.T) {
	t.Parallel()

	// Wrapping the sentinel and unwrapping with errors.Is must work both
	// directly and through fmt.Errorf("...: %w", ...).
	wrapped := fmt.Errorf("outer wrap: %w", ErrEnterpriseRequired)
	assert.ErrorIs(t, wrapped, ErrEnterpriseRequired)
}

func TestValidationError_ErrorAndAsThroughWrap(t *testing.T) {
	t.Parallel()

	original := &ValidationError{Message: "audience must be a non-empty URL"}

	// Error() returns the Message verbatim — the reconciler writes this
	// string into condition.Message, so handler authors control the
	// kubectl-describe output through this field.
	assert.Equal(t, "audience must be a non-empty URL", original.Error())

	// errors.As must match the typed error through a fmt.Errorf wrap so
	// handler authors can wrap with extra context without breaking the
	// reconciler's triage.
	wrapped := fmt.Errorf("validating obo spec: %w", original)
	var got *ValidationError
	require.True(t, errors.As(wrapped, &got),
		"errors.As must match *ValidationError through a fmt.Errorf wrap")
	assert.Same(t, original, got,
		"errors.As must return the same pointer that was wrapped")

	// errors.Is(err, ErrEnterpriseRequired) must NOT match a ValidationError
	// — they live in different buckets of the OBOHandler error contract.
	assert.False(t, errors.Is(original, ErrEnterpriseRequired),
		"ValidationError must not be confused with the EnterpriseRequired sentinel")
	assert.False(t, errors.Is(wrapped, ErrEnterpriseRequired),
		"wrapped ValidationError must not be confused with the EnterpriseRequired sentinel")
}
