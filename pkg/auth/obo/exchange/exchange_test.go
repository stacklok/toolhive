// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package exchange_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/auth/obo/exchange"
)

func TestTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		audience string
		scopes   []string
		want     string
	}{
		{name: "neither set is empty", want: ""},
		{name: "scopes are space-joined", scopes: []string{"a", "b"}, want: "a b"},
		{name: "scopes win over audience", audience: "api://other", scopes: []string{"s"}, want: "s"},
		{name: "blank scopes fall back to audience", audience: "api://backend", scopes: []string{"", " "},
			want: "api://backend/.default"},
		{name: "bare audience gets /.default", audience: "api://backend", want: "api://backend/.default"},
		{name: "audience is trimmed", audience: "  api://backend  ", want: "api://backend/.default"},
		{name: "one trailing slash is dropped", audience: "api://backend/", want: "api://backend/.default"},
		{name: ".default audience is kept", audience: "api://backend/.default", want: "api://backend/.default"},
		{name: ".default audience with trailing slash", audience: "api://backend/.default/",
			want: "api://backend/.default"},
		{name: "whitespace-only audience is empty", audience: "   ", want: ""},
		{name: "slash-only audience is empty", audience: "/", want: ""},
		{name: "client id audience gets /.default", audience: "00000000-0000-0000-0000-000000000001",
			want: "00000000-0000-0000-0000-000000000001/.default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, exchange.Target(tt.audience, tt.scopes))
		})
	}
}
