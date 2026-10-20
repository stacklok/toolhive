// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package plugins

import "testing"

// FuzzParsePluginManifestFromBytes checks that parsing an untrusted
// plugin.json never panics.
func FuzzParsePluginManifestFromBytes(f *testing.F) {
	for _, seed := range []string{
		`{"name":"good-plugin","version":"1.2.3","author":{"name":"tester","email":"t@example.com"},"keywords":["alpha"],"commands":["./commands/foo.md"],"skills":["./skills/bar"],"customField":42}`,
		`{"name":"x","keywords":"foo"}`,
		`{"name":"x","keywords":null}`,
		`{"name":"x","skills":["../escape"]}`,
		`{"name":"x","hooks":["./h","/etc/passwd"]}`,
		`{"name":"x","skills":[""]}`,
		`{"name":"x","name":"y"}`,
		`[]`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(_ *testing.T, content []byte) {
		_, _ = ParsePluginManifestFromBytes(content)
	})
}
