// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import "testing"

func TestLockFileFeatureEnabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "unset defaults to disabled", value: "", want: false},
		{name: "true enables", value: "true", want: true},
		{name: "mixed case true enables", value: "True", want: true},
		{name: "false stays disabled", value: "false", want: false},
		{name: "arbitrary value stays disabled", value: "1", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value == "" {
				t.Setenv(LockFileEnvVar, "")
			} else {
				t.Setenv(LockFileEnvVar, tt.value)
			}
			if got := LockFileFeatureEnabled(); got != tt.want {
				t.Errorf("LockFileFeatureEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
