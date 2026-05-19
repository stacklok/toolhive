// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package obo

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrEnterpriseRequired_IsSentinel(t *testing.T) {
	t.Parallel()

	// Wrapping the sentinel and unwrapping with errors.Is must work both
	// directly and through fmt.Errorf("...: %w", ...).
	wrapped := fmt.Errorf("outer wrap: %w", ErrEnterpriseRequired)
	assert.ErrorIs(t, wrapped, ErrEnterpriseRequired)
}
