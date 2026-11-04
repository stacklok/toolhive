// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package claims

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// FuzzDecodeStringArray feeds arbitrary JSON-decoded claim values to both
// decoders and checks they never panic and agree with each other.
func FuzzDecodeStringArray(f *testing.F) {
	for _, seed := range []string{
		`["admin","users"]`, `[]`, `null`, `"admin"`, `["a",1,null,{"b":2}]`,
		`[["nested"]]`, `{"groups":["a"]}`, `42`, `true`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		var raw any
		if json.Unmarshal(data, &raw) != nil {
			return
		}
		strict, err := DecodeStringArray(raw)
		lenient, recognized := DecodeStringArrayLenient(raw)
		if err == nil {
			// Anything the strict decoder accepts, the lenient one must
			// accept with the same values.
			require.True(t, recognized)
			require.Equal(t, len(strict), len(lenient))
			for i := range strict {
				require.Equal(t, strict[i], lenient[i])
			}
		}
	})
}
