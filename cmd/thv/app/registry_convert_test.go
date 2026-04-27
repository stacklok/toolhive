// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Test mutates package-level flag state so subtests run sequentially.
//
//nolint:paralleltest // Sequential by design — package globals shared across subtests.
func TestRegistryConvertPreRunE(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		out       string
		inPlace   bool
		noBackup  bool
		expectErr bool
	}{
		{name: "no flags is valid", expectErr: false},
		{name: "in only is valid", in: "registry.json", expectErr: false},
		{name: "out only is valid", out: "out.json", expectErr: false},
		{name: "in and out is valid", in: "registry.json", out: "out.json", expectErr: false},
		{name: "in-place with in is valid", in: "registry.json", inPlace: true, expectErr: false},
		{name: "in-place without in is invalid", inPlace: true, expectErr: true},
		{name: "in-place with out is invalid", in: "registry.json", out: "out.json", inPlace: true, expectErr: true},
		{name: "no-backup without in-place is invalid", in: "registry.json", noBackup: true, expectErr: true},
		{name: "in-place with no-backup is valid", in: "registry.json", inPlace: true, noBackup: true, expectErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			convertIn = tt.in
			convertOut = tt.out
			convertInPlace = tt.inPlace
			convertNoBackup = tt.noBackup
			t.Cleanup(func() {
				convertIn = ""
				convertOut = ""
				convertInPlace = false
				convertNoBackup = false
			})

			err := registryConvertPreRunE(nil, nil)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}
