// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInitials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		email string
		want  string
	}{
		{name: "Ada Lovelace", want: "AL"},
		{name: "Ada", want: "A"},
		{email: "ada@example.com", want: "AE"},
		{name: "", email: "", want: "?"},
		// Non-ASCII names: slicing the first byte instead of the first rune
		// would split a multi-byte UTF-8 sequence and render invalid
		// UTF-8/replacement characters.
		{name: "Élodie Dupont", want: "ÉD"},
		{name: "李小龍", want: "李"},
		{name: "Øyvind Åsen", want: "ØÅ"},
	}
	for _, tt := range tests {
		t.Run(tt.name+tt.email, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, initials(tt.name, tt.email))
		})
	}
}
