// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package conversions

import (
	"regexp"
	"unicode"
)

var nonIdentChar = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// SanitizeName converts an MCP tool name into a valid Starlark identifier.
// Characters that are not alphanumeric or underscore are replaced with
// underscores. Leading digits get a prefix underscore.
func SanitizeName(name string) string {
	s := nonIdentChar.ReplaceAllString(name, "_")
	if len(s) > 0 && unicode.IsDigit(rune(s[0])) {
		s = "_" + s
	}
	if s == "" {
		s = "_"
	}
	return s
}
