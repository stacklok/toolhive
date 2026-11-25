// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// TestIsLoopbackHostForms pins the recognised loopback forms. The bare "::1"
// case matters because url.Hostname() strips brackets: a caller passing a
// parsed hostname hands us that form, and a string-matching implementation
// misses it.
func TestIsLoopbackHostForms(t *testing.T) {
	t.Parallel()

	loopback := []string{
		"localhost", "LOCALHOST", "localhost:8080",
		"127.0.0.1", "127.0.0.1:8080", "127.0.0.2", // any 127/8 address
		"::1", "[::1]", "[::1]:8080",
	}
	for _, host := range loopback {
		assert.True(t, oauthproto.IsLoopbackHost(host), "%q must be loopback", host)
	}

	nonLoopback := []string{
		"", "example.com", "127.0.0.1.example.com", "10.0.0.1", "192.168.1.1",
		"::2", "[::2]:8080", "localhost.example.com",
	}
	for _, host := range nonLoopback {
		assert.False(t, oauthproto.IsLoopbackHost(host), "%q must not be loopback", host)
	}
}
