// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package instructions

import (
	"strings"
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
			// The value as a file or a YAML block holds it: white space
			// around it is dropped.
			cfg := &Config{Mode: tt.mode, Value: "\n  policy\n"}
			assert.Equal(t, tt.want, cfg.Apply(tt.server))
		})
	}
}

func TestConfigApplyTrimsASCIIWhiteSpaceOnly(t *testing.T) {
	t.Parallel()

	cfg := &Config{Mode: ModeReplace, Value: "\t\u00a0policy\u00a0\r\n"}
	assert.Equal(t, "\u00a0policy\u00a0", cfg.Apply("server"))
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
		{name: "white space only", cfg: &Config{Value: " \n\t\f\r"}, wantErr: "only white space"},
		// The CRD's \S pattern, checked with RE2, treats only ASCII as white
		// space, so Validate does too.
		{name: "no-break spaces only", cfg: &Config{Value: "\u00a0\u00a0"}},
		{name: "longest value", cfg: &Config{Value: strings.Repeat("é", MaxValueLength)}},
		{name: "value too long", cfg: &Config{Value: strings.Repeat("a", MaxValueLength+1)}, wantErr: "more than the 65536 allowed"},
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
