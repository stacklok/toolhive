// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package binding_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/session/binding"
)

func TestFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		iss     string
		sub     string
		want    string
		wantErr bool
	}{
		{
			name: "typical OIDC issuer and subject",
			iss:  "https://idp.example",
			sub:  "user-42",
			want: "https://idp.example\x00user-42",
		},
		{
			name: "issuer with path",
			iss:  "https://accounts.google.com/o/oauth2",
			sub:  "109876543210987654321",
			want: "https://accounts.google.com/o/oauth2\x00109876543210987654321",
		},
		{
			name: "minimal non-empty inputs",
			iss:  "i",
			sub:  "s",
			want: "i\x00s",
		},
		{
			name: "email-like subject",
			iss:  "https://auth.example.com",
			sub:  "alice@example.com",
			want: "https://auth.example.com\x00alice@example.com",
		},
		{name: "empty iss", iss: "", sub: "sub", wantErr: true},
		{name: "empty sub", iss: "iss", sub: "", wantErr: true},
		{name: "NUL in iss", iss: "a\x00b", sub: "sub", wantErr: true},
		{name: "NUL in sub", iss: "iss", sub: "a\x00b", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := binding.Format(tt.iss, tt.sub)
			if tt.wantErr {
				assert.Empty(t, got)
				require.ErrorIs(t, err, binding.ErrInvalidBinding)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)

			gotIss, gotSub, ok := binding.Parse(got)
			require.True(t, ok, "Parse must return ok=true for a value produced by Format")
			assert.Equal(t, tt.iss, gotIss)
			assert.Equal(t, tt.sub, gotSub)
		})
	}
}

func TestParse_RejectsInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty string", input: ""},
		{name: "no NUL separator", input: "nothing-here"},
		{name: "empty iss half", input: "\x00sub"},
		{name: "empty sub half", input: "iss\x00"},
		// Defense-in-depth: a value that splits into three parts on NUL must be
		// rejected so a corrupt or adversarial store write cannot smuggle extra
		// data past Parse.
		{name: "trailing NUL with extra data", input: "iss\x00sub\x00trailer"},
		// Callers must call IsUnauthenticated before Parse on the restore path;
		// the sentinel must not accidentally parse as a bound binding.
		{name: "unauthenticated sentinel", input: binding.UnauthenticatedSentinel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			iss, sub, ok := binding.Parse(tt.input)
			assert.False(t, ok)
			assert.Empty(t, iss)
			assert.Empty(t, sub)
		})
	}
}

func TestIsUnauthenticated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "sentinel", input: binding.UnauthenticatedSentinel, want: true},
		{name: "empty string", input: "", want: false},
		{name: "arbitrary string", input: "foo", want: false},
		{name: "bound binding", input: "iss\x00sub", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, binding.IsUnauthenticated(tt.input))
		})
	}
}
