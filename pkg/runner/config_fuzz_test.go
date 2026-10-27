// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"bytes"
	"testing"
)

// FuzzReadJSON checks that reading an untrusted (e.g. imported) RunConfig
// never panics.
func FuzzReadJSON(f *testing.F) {
	for _, seed := range []string{
		`{"schema_version":"v0.1.0","name":"fetch","image":"ghcr.io/stackloklabs/gofetch/server:latest","transport":"streamable-http","port":8080,"target_port":8080,"cmd_args":["--flag"],"env_vars":{"K":"V"},"volumes":["/a:/b:ro"],"secrets":["s,target=T"],"permission_profile":{"network":{"outbound":{"insecure_allow_all":true}}}}`,
		`{"name":"x","oidc_config":{"issuer":"https://issuer.example.com","audience":"aud"}}`,
		`{"name":"x","authz_config":{"version":"1.0","type":"cedarv1","cedar":{"policies":["permit(principal, action, resource);"]}}}`,
		`{"remote_url":"https://example.com/mcp","remote_auth_config":{"client_id":"id","scopes":["openid"]}}`,
		`{}`,
		`null`,
		`{"env_vars":null,"volumes":[1]}`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = ReadJSON(bytes.NewReader(data))
	})
}
