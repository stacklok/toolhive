// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode"
)

var ambiguousRequest = &ambiguousRequestError{}

type ambiguousRequestError struct{}

// Error is deliberately fixed so conflicting request-controlled names and
// values are never reflected to the client.
func (*ambiguousRequestError) Error() string { return "Invalid Request" }

// Code implements CodedError.
func (*ambiguousRequestError) Code() int64 { return CodeInvalidRequest }

// Data implements CodedError.
func (*ambiguousRequestError) Data() map[string]any { return nil }

type jsonContainer struct {
	delim        json.Delim
	seen         map[string]struct{}
	expectingKey bool
}

// hasAmbiguousJSONMembers reports whether a syntactically valid JSON value has
// duplicate or case-fold-equivalent member names in any object. JSON permits
// case-distinct names, but ToolHive rejects them deliberately because downstream
// decoders may merge them and act on a value different from the one authorized.
// Syntax errors and trailing values are left to the request parser.
func hasAmbiguousJSONMembers(body []byte) bool {
	body = bytes.TrimPrefix(body, UTF8BOM)
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	containers := make([]jsonContainer, 0, 8)
	ambiguous := false
	rootComplete := false

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return rootComplete && len(containers) == 0 && ambiguous
		}
		if err != nil || rootComplete {
			return false
		}

		if len(containers) == 0 {
			delim, ok := token.(json.Delim)
			if !ok {
				rootComplete = true
				continue
			}
			if delim != '{' && delim != '[' {
				return false
			}
			containers = append(containers, newJSONContainer(delim))
			continue
		}

		current := &containers[len(containers)-1]
		if current.delim == '{' && current.expectingKey {
			if delim, ok := token.(json.Delim); ok && delim == '}' {
				containers = containers[:len(containers)-1]
				if len(containers) == 0 {
					rootComplete = true
				}
				continue
			}

			key, ok := token.(string)
			if !ok {
				return false
			}
			folded := foldJSONMemberName(key)
			if _, exists := current.seen[folded]; exists {
				ambiguous = true
			} else {
				current.seen[folded] = struct{}{}
			}
			current.expectingKey = false
			continue
		}

		if current.delim == '{' {
			current.expectingKey = true
		}

		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				containers = append(containers, newJSONContainer(delim))
			case ']':
				if current.delim != '[' {
					return false
				}
				containers = containers[:len(containers)-1]
				if len(containers) == 0 {
					rootComplete = true
				}
			default:
				return false
			}
		}
	}
}

func newJSONContainer(delim json.Delim) jsonContainer {
	container := jsonContainer{delim: delim}
	if delim == '{' {
		container.seen = make(map[string]struct{})
		container.expectingKey = true
	}
	return container
}

// foldJSONMemberName returns one stable representative for each Unicode simple
// case-folding equivalence class, matching strings.EqualFold semantics.
func foldJSONMemberName(name string) string {
	var folded strings.Builder
	folded.Grow(len(name))
	for _, r := range name {
		canonical := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < canonical {
				canonical = next
			}
		}
		folded.WriteRune(canonical)
	}
	return folded.String()
}
