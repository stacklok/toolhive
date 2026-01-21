// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcpregistrystatus

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewDefaultStatusDeriver(t *testing.T) {
	t.Parallel()

	deriver := NewDefaultStatusDeriver()
	assert.NotNil(t, deriver)
	assert.IsType(t, &DefaultStatusDeriver{}, deriver)
}
