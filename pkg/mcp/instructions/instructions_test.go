// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package instructions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigApply(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mode   Mode
		server string
		want   string
	}{
		{name: "replace with server instructions", mode: ModeReplace, server: "server", want: "policy"},
		{name: "replace without server instructions", mode: ModeReplace, server: "", want: "policy"},
		{name: "append with server instructions", mode: ModeAppend, server: "server", want: "server\n\npolicy"},
		{name: "append without server instructions", mode: ModeAppend, server: "", want: "policy"},
		{name: "prepend with server instructions", mode: ModePrepend, server: "server", want: "policy\n\nserver"},
		{name: "prepend without server instructions", mode: ModePrepend, server: "", want: "policy"},
		{name: "empty mode appends", mode: "", server: "server", want: "server\n\npolicy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{Mode: tt.mode, Value: "policy"}
			assert.Equal(t, tt.want, cfg.Apply(tt.server))
		})
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *Config
		wantErr string
	}{
		{name: "replace", cfg: &Config{Mode: ModeReplace, Value: "policy"}},
		{name: "append", cfg: &Config{Mode: ModeAppend, Value: "policy"}},
		{name: "prepend", cfg: &Config{Mode: ModePrepend, Value: "policy"}},
		{name: "empty mode", cfg: &Config{Value: "policy"}},
		{name: "nil", cfg: nil, wantErr: "required"},
		{name: "unknown mode", cfg: &Config{Mode: "Append", Value: "policy"}, wantErr: `invalid instructions mode "Append"`},
		{name: "empty value", cfg: &Config{Mode: ModeAppend}, wantErr: "must not be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
