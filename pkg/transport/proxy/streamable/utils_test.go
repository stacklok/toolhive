// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsSupportedMCPVersion verifies membership in supportedMCPVersions: every
// known MCP revision date returns true, and unknown/garbage/empty strings
// return false. This function is only consulted by handlePost when strict
// protocol validation is enabled (see WithStrictProtocolValidation).
func TestIsSupportedMCPVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{name: "2024-11-05 is supported", version: "2024-11-05", want: true},
		{name: "2025-03-26 is supported", version: "2025-03-26", want: true},
		{name: "2025-06-18 is supported", version: "2025-06-18", want: true},
		{name: "2025-11-25 is supported", version: "2025-11-25", want: true},
		{name: "2026-07-28 is supported", version: "2026-07-28", want: true},
		{name: "unknown future date is not supported", version: "1999-01-01", want: false},
		{name: "garbage string is not supported", version: "not-a-version", want: false},
		{name: "empty string is not supported", version: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isSupportedMCPVersion(tt.version))
		})
	}
}
