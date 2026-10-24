// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package plugins

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/httperr"
)

func TestValidatePushSigning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    PushOptions
		wantErr string // empty means the choice is accepted
	}{
		{name: "key alone", opts: PushOptions{Key: "/tmp/cosign.key"}},
		{name: "identity token alone", opts: PushOptions{IdentityToken: "a.b.c"}},
		{name: "no_sign alone", opts: PushOptions{NoSign: true}},
		{
			name:    "nothing chosen",
			opts:    PushOptions{Reference: "ghcr.io/test/plugin:v1"},
			wantErr: "signing credential required",
		},
		{
			name:    "no_sign with identity token",
			opts:    PushOptions{NoSign: true, IdentityToken: "a.b.c"},
			wantErr: "cannot be combined",
		},
		{
			name:    "no_sign with key",
			opts:    PushOptions{NoSign: true, Key: "/tmp/cosign.key"},
			wantErr: "cannot be combined",
		},
		{
			name:    "key with identity token",
			opts:    PushOptions{Key: "/tmp/cosign.key", IdentityToken: "a.b.c"},
			wantErr: "specify only one",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePushSigning(tt.opts)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			// The rejection is the caller's fault, and must be reported as
			// such by the handler and the service alike.
			assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
		})
	}
}
