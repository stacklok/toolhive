// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfirmBrowserSignInNonInteractiveDeclinesSilently exercises the
// non-interactive path: in this test process, os.Stdin is not a terminal,
// so this reaches the decline branch without needing to fake TTY detection
// (same approach as TestRequireConfirmationNonInteractiveWithoutYesRefuses
// in skill_confirm_test.go). Unlike requireConfirmation, this must NOT
// error — skill push has no --yes flag to hint at, so Acquire's own
// actionable failure text is the only message the user sees.
func TestConfirmBrowserSignInNonInteractiveDeclinesSilently(t *testing.T) {
	t.Parallel()
	confirmed, err := confirmBrowserSignIn()
	require.NoError(t, err)
	assert.False(t, confirmed)
}
