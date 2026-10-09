// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcrkey

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScopesHashPinnedCanonicalValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		scopes []string
		want   string
	}{
		{name: "nil", scopes: nil, want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{name: "empty", scopes: []string{}, want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{name: "one scope", scopes: []string{"openid"}, want: "a780fc8d532ce48dc62ac7d2027bf9b07f036b7f5ec1824a882d89c35d183887"},
		{name: "multiple scopes", scopes: []string{"openid", "profile", "email"}, want: "62358d8fc3cf5a7913083c82c1153191119dbcba28970bc42426bed8ade52918"},
		{name: "boundary ab c", scopes: []string{"ab", "c"}, want: "0acdf9a2665198da784232d827b01ae3d24af5db060d723950cfcd47cd82ac07"},
		{name: "boundary a bc", scopes: []string{"a", "bc"}, want: "b4c620724d95022be9765d6f660a36f90835acab2437b5e8cd71812940b29346"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, ScopesHash(tc.scopes))
		})
	}
}

func TestScopesHashCanonicalizesOrderAndDuplicates(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		"62358d8fc3cf5a7913083c82c1153191119dbcba28970bc42426bed8ade52918",
		ScopesHash([]string{"profile", "openid", "email", "openid"}),
	)
}

func TestScopesHashDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	scopes := []string{"profile", "openid", "profile", "email"}
	original := append([]string(nil), scopes...)
	_ = ScopesHash(scopes)

	assert.Equal(t, original, scopes)
}
