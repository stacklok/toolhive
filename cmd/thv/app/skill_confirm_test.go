// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireConfirmationYesSkipsPrompt(t *testing.T) {
	t.Parallel()
	confirmed, err := requireConfirmation("do the thing", true)
	require.NoError(t, err)
	assert.True(t, confirmed)
}

// TestRequireConfirmationNonInteractiveWithoutYesRefuses exercises the
// non-interactive path: in this test process, os.Stdin is not a terminal
// (it's whatever `go test` wires up), so this reaches the policy-rejection
// branch without needing to fake TTY detection.
func TestRequireConfirmationNonInteractiveWithoutYesRefuses(t *testing.T) {
	t.Parallel()
	confirmed, err := requireConfirmation("do the thing", false)
	require.Error(t, err)
	assert.False(t, confirmed)
	assert.Equal(t, ExitCodePolicyRejection, ExitCodeFromError(err))
	assert.Contains(t, err.Error(), "--yes")
}
