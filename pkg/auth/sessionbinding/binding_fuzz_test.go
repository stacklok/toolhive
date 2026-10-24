// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionbinding

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// FuzzParse checks that Parse never panics and that anything it accepts
// formats back to the same string.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"https://issuer.example.com\x00user-123",
		UnauthenticatedSentinel,
		"\x00sub",
		"iss\x00",
		"iss\x00sub\x00extra",
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		iss, sub, ok := Parse(s)
		if !ok {
			return
		}
		formatted, err := Format(iss, sub)
		require.NoError(t, err)
		require.Equal(t, s, formatted)
	})
}

// FuzzFormat checks that every formatted binding parses back to its inputs,
// which also means distinct (iss, sub) pairs never share a binding string.
func FuzzFormat(f *testing.F) {
	f.Add("https://issuer.example.com", "user-123")
	f.Add("iss", "")
	f.Add("is\x00s", "sub")
	f.Add("unauthenticated", "x")

	f.Fuzz(func(t *testing.T, iss, sub string) {
		formatted, err := Format(iss, sub)
		if err != nil {
			return
		}
		require.NotEqual(t, UnauthenticatedSentinel, formatted)
		gotIss, gotSub, ok := Parse(formatted)
		require.True(t, ok)
		require.Equal(t, iss, gotIss)
		require.Equal(t, sub, gotSub)
	})
}
