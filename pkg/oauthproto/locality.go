// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import "strings"

// IsLoopbackHost reports whether host is a loopback hostname or IP address.
// pkg/networking wraps this function in its own IsLocalhost to avoid a
// reverse import dependency from this leaf package into networking.
//
// Recognised forms: "localhost", "localhost:<port>", "127.0.0.1", "127.0.0.1:<port>",
// "[::1]", "[::1]:<port>".
func IsLoopbackHost(host string) bool {
	return strings.HasPrefix(host, "localhost:") ||
		strings.HasPrefix(host, "127.0.0.1:") ||
		strings.HasPrefix(host, "[::1]:") ||
		host == "localhost" ||
		host == "127.0.0.1" ||
		host == "[::1]"
}
