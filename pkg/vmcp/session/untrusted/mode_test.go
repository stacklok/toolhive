// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

//nolint:paralleltest // t.Setenv modifies the process environment.
func TestModeEnabled(t *testing.T) {
	cases := []struct {
		name  string
		value *string // nil = unset
		want  bool
	}{
		{name: "unset defaults to off", value: nil, want: false},
		{name: "empty string is off", value: ptr(""), want: false},
		{name: `"true" is on`, value: ptr("true"), want: true},
		{name: `"1" is on`, value: ptr("1"), want: true},
		{name: `"false" is off`, value: ptr("false"), want: false},
		{name: `"TRUE" is off (exact match only)`, value: ptr("TRUE"), want: false},
		{name: `"yes" is off`, value: ptr("yes"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.value != nil {
				t.Setenv(EnvEnableUntrustedMode, *tc.value)
			}
			assert.Equal(t, tc.want, ModeEnabled())
		})
	}
}

//nolint:paralleltest // t.Setenv modifies the process environment.
func TestMarkBackend(t *testing.T) {
	newServer := func(flagged bool) *mcpv1beta1.MCPServer {
		return &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "srv", UID: "uid-1"},
			Spec:       mcpv1beta1.MCPServerSpec{Untrusted: flagged},
		}
	}

	t.Run("flagged + mode on stamps the untrusted metadata", func(t *testing.T) {
		t.Setenv(EnvEnableUntrustedMode, "true")
		md := map[string]string{}
		MarkBackend(newServer(true), md)
		assert.Equal(t, "true", md[MetadataKeyUntrusted])
		assert.Equal(t, "uid-1", md[MetadataKeyMCPServerUID])
	})

	t.Run("flagged + mode off stamps nothing", func(t *testing.T) {
		t.Setenv(EnvEnableUntrustedMode, "false")
		md := map[string]string{}
		MarkBackend(newServer(true), md)
		assert.Empty(t, md)
	})

	t.Run("unflagged stamps nothing regardless of the mode", func(t *testing.T) {
		t.Setenv(EnvEnableUntrustedMode, "true")
		md := map[string]string{}
		MarkBackend(newServer(false), md)
		assert.Empty(t, md)
	})
}

func ptr(s string) *string { return &s }
