// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionbinding

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
)

// FuzzParse checks that every accepted binding round-trips without dropping fields.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"https://issuer.example.com\x00user-123",
		"iss\x00sub\x00delegated\x00client",
		"iss\x00sub\x00delegated\x00client\x00actor-issuer\x00actor-subject",
		UnauthenticatedSentinel,
		"\x00sub",
		"iss\x00",
		"iss\x00sub\x00extra",
		"iss\x00sub\x00delegated\x00client\x00actor-issuer",
		"iss\x00sub\x00delegated\x00client\x00actor-issuer\x00actor-subject\x00extra",
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		b, ok := parseBinding(s)
		iss, sub, _ := Parse(s)
		oldIss, oldSub, oldOK := parseV069(s)
		require.Equal(t, oldOK, ok && b.route == "")
		if oldOK {
			require.Equal(t, oldIss, iss)
			require.Equal(t, oldSub, sub)
		}
		if !ok {
			return
		}
		formatted, err := formatBinding(b)
		require.NoError(t, err)
		require.Equal(t, s, formatted)
	})
}

// FuzzFormatDelegated checks that parsing recovers every encoded field, so two
// distinct bindings cannot share an encoding.
func FuzzFormatDelegated(f *testing.F) {
	f.Add("https://issuer.example.com", "user-123", "", false, false, "", "")
	f.Add("iss", "", "", false, false, "", "")
	f.Add("iss", "sub", "client", true, false, "", "")
	f.Add("iss", "sub", "client", true, true, "actor-issuer", "actor-subject")
	f.Add("iss", "", "client", true, false, "", "")
	f.Add("iss", "sub", "client", true, true, "", "actor-subject")
	f.Add("iss", "sub", "client", true, true, "actor\x00issuer", "actor-subject")
	f.Add("iss", "sub", "client\x00tail", true, false, "", "")
	f.Add("unauthenticated", "sub", "", false, false, "", "")

	f.Fuzz(func(t *testing.T, iss, sub, clientID string, delegated, hasActor bool, actIss, actSub string) {
		b := parsedBinding{iss: iss, sub: sub}
		if delegated {
			b.route = string(auth.AdmissionRouteDelegated)
			b.clientID = clientID
			if hasActor {
				b.actIss, b.actSub = actIss, actSub
			}
		}
		formatted, err := formatBinding(b)
		if !delegated {
			old, oldErr := formatV069(iss, sub)
			require.Equal(t, old, formatted)
			require.Equal(t, oldErr != nil, err != nil)
		}
		if err != nil {
			require.ErrorIs(t, err, ErrInvalidBinding)
			return
		}
		got, ok := parseBinding(formatted)
		require.True(t, ok)
		require.Equal(t, b, got)
		gotIss, gotSub, ok := Parse(formatted)
		require.True(t, ok)
		require.Equal(t, iss, gotIss)
		require.Equal(t, sub, gotSub)
		if delegated {
			_, _, oldOK := parseV069(formatted)
			require.False(t, oldOK)
		}
	})
}
