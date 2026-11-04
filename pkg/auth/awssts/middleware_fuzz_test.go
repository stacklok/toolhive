// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package awssts

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// FuzzExtractSessionName checks that session-name extraction never panics on
// arbitrary JSON claim sets and only ever returns a non-empty string claim.
func FuzzExtractSessionName(f *testing.F) {
	for _, seed := range []struct{ claims, name string }{
		{`{"sub":"user-123","email":"user@example.com"}`, "sub"},
		{`{"sub":""}`, "sub"},
		{`{"sub":123}`, "sub"},
		{`{"sub":null}`, "sub"},
		{`{"email":["a@example.com"]}`, "email"},
		{`{}`, "sub"},
	} {
		f.Add([]byte(seed.claims), seed.name)
	}

	f.Fuzz(func(t *testing.T, data []byte, claimName string) {
		var claims map[string]any
		if json.Unmarshal(data, &claims) != nil {
			return
		}
		name, err := ExtractSessionName(claims, claimName)
		if err != nil {
			return
		}
		require.NotEmpty(t, name)
		require.Equal(t, claims[claimName], name)
	})
}
