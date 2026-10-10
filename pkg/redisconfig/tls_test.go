// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package redisconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/test/testkit/redistls"
)

func TestTLSConfigLoad(t *testing.T) {
	t.Parallel()
	certificate := redistls.CACertPEM(t)
	for _, tc := range []struct {
		name      string
		config    *TLSConfig
		contents  []byte
		writeFile bool
		wantError string
	}{
		{name: "absent disables TLS"},
		{name: "empty enables verified TLS", config: &TLSConfig{}},
		{name: "custom CA", config: &TLSConfig{CACertFile: "ca.pem"}, contents: certificate, writeFile: true},
		{name: "missing CA", config: &TLSConfig{CACertFile: "missing.pem"}, wantError: "failed to read Redis CA cert file"},
		{name: "empty CA", config: &TLSConfig{CACertFile: "ca.pem"}, writeFile: true, wantError: "is empty"},
		{name: "malformed CA", config: &TLSConfig{CACertFile: "ca.pem"}, contents: []byte("not a certificate"), writeFile: true, wantError: "failed to parse CA certificate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.config != nil && tc.config.CACertFile != "" {
				tc.config.CACertFile = filepath.Join(t.TempDir(), tc.config.CACertFile)
				if tc.writeFile {
					require.NoError(t, os.WriteFile(tc.config.CACertFile, tc.contents, 0600))
				}
			}
			got, err := tc.config.Load()
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if tc.config == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.False(t, got.InsecureSkipVerify)
			assert.Equal(t, tc.contents, got.CACert)
		})
	}
}
