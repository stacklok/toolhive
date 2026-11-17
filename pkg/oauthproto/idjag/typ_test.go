// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package idjag

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsIDJAG(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		typ  string
		want bool
	}{
		{name: "exact", typ: "oauth-id-jag+jwt", want: true},
		{name: "upper case", typ: "OAUTH-ID-JAG+JWT", want: true},
		{name: "application prefix", typ: "application/oauth-id-jag+jwt", want: true},
		{name: "mixed case with prefix", typ: "Application/OAuth-ID-JAG+JWT", want: true},
		{name: "JWT", typ: "JWT", want: false},
		{name: "at+jwt", typ: "at+jwt", want: false},
		{name: "empty", typ: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsIDJAG(tt.typ))
		})
	}
}
