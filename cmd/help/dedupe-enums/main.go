// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Command dedupe-enums works around a swag v2.0.0-rc5 nondeterminism bug:
// loadExternalPackage (in swag's packages.go) reparses an external module's
// package without memoizing by import path, which can non-deterministically
// repeat the "enum"/"x-enum-varnames" arrays it emits for types defined
// outside this module (model.ArgumentType, model.Format, model.Icon from
// github.com/modelcontextprotocol/registry). The repeat count varies by
// machine — 2x and 3x have both been seen. When an array is the same block
// repeated back to back, this collapses it to one copy.
//
// Run after `swag init`, on each generated docs/server file:
//
//	go run ./cmd/help/dedupe-enums docs/server/docs.go docs/server/swagger.json docs/server/swagger.yaml
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// jsonArray only matches quoted-string enum values (swag emits every enum in
// docs/server today as strings); a doubled numeric/integer external enum
// would pass through undeduped. yamlArray matches any scalar, so it has no
// such gap.
var (
	jsonArray = regexp.MustCompile(`(?m)^([ \t]*"(?:enum|x-enum-varnames)": \[\n)((?:[ \t]*".*?",?\n)+?)([ \t]*\])`)
	yamlArray = regexp.MustCompile(`(?m)^([ \t]*(?:enum|x-enum-varnames):\n)((?:[ \t]*- .+\n)+)`)
)

func main() {
	for _, path := range os.Args[1:] {
		data, err := os.ReadFile(path) // #nosec G304 -- path is a CLI argument we control (Taskfile/verify.sh)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		fixed := jsonArray.ReplaceAllStringFunc(string(data), dedupeJSONBlock)
		fixed = yamlArray.ReplaceAllStringFunc(fixed, dedupeYAMLBlock)

		if fixed == string(data) {
			continue
		}
		if err := os.WriteFile(path, []byte(fixed), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("deduped", path)
	}
}

func dedupeJSONBlock(match string) string {
	g := jsonArray.FindStringSubmatch(match)
	header, body, footer := g[1], g[2], g[3]

	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	trimmed := make([]string, len(lines))
	for i, l := range lines {
		trimmed[i] = strings.TrimSuffix(l, ",")
	}

	period, ok := firstPeriodIfRepeated(trimmed)
	if !ok {
		return match
	}
	return header + strings.Join(period, ",\n") + "\n" + footer
}

func dedupeYAMLBlock(match string) string {
	g := yamlArray.FindStringSubmatch(match)
	header, body := g[1], g[2]

	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	period, ok := firstPeriodIfRepeated(lines)
	if !ok {
		return match
	}
	return header + strings.Join(period, "\n") + "\n"
}

// firstPeriodIfRepeated reports whether lines is the same block repeated
// back to back some whole number of times, returning one copy of that block
// if so. It only catches this shape (contiguous, order-preserved
// duplication), not any repeated multiset — if swag's bug ever reordered
// values within a later copy, this wouldn't fire. That matches the upstream
// bug: it re-appends the same const list, never reorders it. The repeat
// count varies between machines (2x and 3x have both been observed), so
// this collapses any count rather than only a doubling.
func firstPeriodIfRepeated(lines []string) ([]string, bool) {
	n := len(lines)
	if n < 2 {
		return nil, false
	}
	for period := 1; period <= n/2; period++ {
		if n%period != 0 {
			continue
		}
		repeated := true
		for i := period; i < n; i++ {
			if lines[i] != lines[i-period] {
				repeated = false
				break
			}
		}
		if repeated {
			return lines[:period], true
		}
	}
	return nil, false
}
