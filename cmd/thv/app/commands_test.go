// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsInformationalCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"no args", []string{"thv"}, true},
		{"version subcommand", []string{"thv", "version"}, true},
		{"help flag", []string{"thv", "--help"}, true},
		{"help shorthand", []string{"thv", "-h"}, true},
		{"version flag", []string{"thv", "--version"}, true},
		{"version shorthand", []string{"thv", "-v"}, true},
		{"run subcommand", []string{"thv", "run"}, false},
		{"list subcommand", []string{"thv", "list"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsInformationalCommand(tt.args))
		})
	}
}
