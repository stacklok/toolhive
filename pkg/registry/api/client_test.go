// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegistryHTTPError(t *testing.T) {
	t.Parallel()

	t.Run("Error", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name       string
			statusCode int
			body       string
			want       string
		}{
			{
				name:       "formats 401 with body",
				statusCode: http.StatusUnauthorized,
				body:       "access denied",
				want:       "registry returned HTTP 401: access denied",
			},
			{
				name:       "formats 500 with body",
				statusCode: http.StatusInternalServerError,
				body:       "server error",
				want:       "registry returned HTTP 500: server error",
			},
			{
				name:       "formats with empty body",
				statusCode: http.StatusNotFound,
				body:       "",
				want:       "registry returned HTTP 404: ",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				err := &RegistryHTTPError{StatusCode: tt.statusCode, Body: tt.body}
				require.Equal(t, tt.want, err.Error())
			})
		}
	})

	t.Run("Unwrap", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name          string
			statusCode    int
			wantUnwrapped bool
			wantIsAuthErr bool
		}{
			{
				name:          "401 unwraps to ErrRegistryUnauthorized",
				statusCode:    http.StatusUnauthorized,
				wantUnwrapped: true,
				wantIsAuthErr: true,
			},
			{
				name:          "403 unwraps to ErrRegistryUnauthorized",
				statusCode:    http.StatusForbidden,
				wantUnwrapped: true,
				wantIsAuthErr: true,
			},
			{
				name:          "500 unwraps to nil",
				statusCode:    http.StatusInternalServerError,
				wantUnwrapped: false,
				wantIsAuthErr: false,
			},
			{
				name:          "404 unwraps to nil",
				statusCode:    http.StatusNotFound,
				wantUnwrapped: false,
				wantIsAuthErr: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				err := &RegistryHTTPError{StatusCode: tt.statusCode, Body: "test"}

				unwrapped := err.Unwrap()
				if tt.wantUnwrapped {
					require.ErrorIs(t, unwrapped, ErrRegistryUnauthorized)
				} else {
					require.Nil(t, unwrapped)
				}

				require.Equal(t, tt.wantIsAuthErr, errors.Is(err, ErrRegistryUnauthorized))
			})
		}
	})

	t.Run("errors.Is works through wrapped errors", func(t *testing.T) {
		t.Parallel()

		inner := &RegistryHTTPError{StatusCode: http.StatusUnauthorized, Body: "no auth"}
		wrapped := fmt.Errorf("fetching server: %w", inner)

		require.ErrorIs(t, wrapped, ErrRegistryUnauthorized)
	})
}
