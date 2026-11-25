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

func TestValidationError_ErrorAndAsThroughWrap(t *testing.T) {
	t.Parallel()

	original := &ValidationError{Message: "audience must be a non-empty URL"}

	// Error() returns the Message verbatim: the reconciler writes this string
	// into condition.Message.
	assert.Equal(t, "audience must be a non-empty URL", original.Error())

	// errors.As must match the typed error through a fmt.Errorf wrap so callers
	// can add context without breaking the reconciler's triage.
	wrapped := fmt.Errorf("validating obo spec: %w", original)
	var got *ValidationError
	require.True(t, errors.As(wrapped, &got),
		"errors.As must match *ValidationError through a fmt.Errorf wrap")
	assert.Same(t, original, got,
		"errors.As must return the same pointer that was wrapped")
}
