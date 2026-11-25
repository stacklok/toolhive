// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"errors"
	"net/http"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireRefusal asserts that err is a 400 OAuth error with exactly the given
// error code and error_description.
func requireRefusal(t *testing.T, err error, wantCode, wantDescription string) {
	t.Helper()
	requireRefusalStatus(t, err, http.StatusBadRequest, wantCode, wantDescription)
}

// requireRefusalStatus is requireRefusal for a refusal with a status other
// than 400.
func requireRefusalStatus(t *testing.T, err error, wantStatus int, wantCode, wantDescription string) {
	t.Helper()
	require.Error(t, err)
	var rfcErr *fosite.RFC6749Error
	require.True(t, errors.As(err, &rfcErr), "expected fosite RFC6749Error, got %v", err)
	assert.Equal(t, wantCode, rfcErr.ErrorField)
	assert.Equal(t, wantStatus, rfcErr.CodeField)
	assert.Equal(t, wantDescription, rfcErr.GetDescription())
}
