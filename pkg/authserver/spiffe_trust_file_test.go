// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateSPIFFEBundleFilePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path    string
		wantErr string
	}{
		{path: "/etc/toolhive/bundle.json"},
		{path: "", wantErr: "non-empty absolute"},
		{path: "bundle.json", wantErr: "non-empty absolute"},
		{path: "/etc/toolhive/../bundle.json", wantErr: "traversal"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			err := validateSPIFFEBundleFilePath(tt.path, 0)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
