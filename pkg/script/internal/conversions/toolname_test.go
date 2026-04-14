// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package conversions

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{"simple name", "my_tool", "my_tool"},
		{"hyphens to underscores", "github-fetch-prs", "github_fetch_prs"},
		{"dots to underscores", "pagerduty.list.services", "pagerduty_list_services"},
		{"leading digit", "3scale-api", "_3scale_api"},
		{"empty string", "", "_"},
		{"all special chars", "---", "___"},
		{"mixed", "my-tool.v2/endpoint", "my_tool_v2_endpoint"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SanitizeName(tt.input)
			require.Equal(t, tt.expect, got)
		})
	}
}
