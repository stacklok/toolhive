// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package healthcontext

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsHealthCheck_WrongValueType(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), healthCheckContextKey{}, "not-a-bool")
	assert.False(t, IsHealthCheck(ctx), "non-bool value should not be treated as health check marker")
}

func TestIsHealthCheck_FalseValue(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), healthCheckContextKey{}, false)
	assert.False(t, IsHealthCheck(ctx), "explicit false value should not be treated as health check marker")
}
