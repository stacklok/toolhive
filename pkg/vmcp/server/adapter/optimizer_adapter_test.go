// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOptimizerToolNameConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "find_tool", FindToolName)
	assert.Equal(t, "call_tool", CallToolName)
}
