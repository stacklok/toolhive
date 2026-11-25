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

type jsonAmbiguityScanner struct {
	containers   []jsonContainer
	ambiguous    bool
	rootComplete bool
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
	scanner := jsonAmbiguityScanner{
		containers: make([]jsonContainer, 0, 8),
	}

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return scanner.isAmbiguous()
		}
		if err != nil || scanner.rootComplete {
			return false
		}
		if !scanner.consumeToken(token) {
			return false
		}
	}
}

func (s *jsonAmbiguityScanner) isAmbiguous() bool {
	return s.rootComplete && len(s.containers) == 0 && s.ambiguous
}

func (s *jsonAmbiguityScanner) consumeToken(token json.Token) bool {
	if len(s.containers) == 0 {
		return s.consumeRootToken(token)
	}

	current := &s.containers[len(s.containers)-1]
	if current.delim == '{' && current.expectingKey {
		return s.consumeObjectKey(current, token)
	}
	if current.delim == '{' {
		current.expectingKey = true
	}

	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}
	return s.consumeValueDelimiter(current, delim)
}

func (s *jsonAmbiguityScanner) consumeRootToken(token json.Token) bool {
	delim, ok := token.(json.Delim)
	if !ok {
		s.rootComplete = true
		return true
	}
	if delim != '{' && delim != '[' {
		return false
	}
	s.containers = append(s.containers, newJSONContainer(delim))
	return true
}

func (s *jsonAmbiguityScanner) consumeObjectKey(current *jsonContainer, token json.Token) bool {
	if delim, ok := token.(json.Delim); ok && delim == '}' {
		s.closeContainer()
		return true
	}

	key, ok := token.(string)
	if !ok {
		return false
	}
	folded := foldJSONMemberName(key)
	if _, exists := current.seen[folded]; exists {
		s.ambiguous = true
	} else {
		current.seen[folded] = struct{}{}
	}
	current.expectingKey = false
	return true
}

func (s *jsonAmbiguityScanner) consumeValueDelimiter(current *jsonContainer, delim json.Delim) bool {
	switch delim {
	case '{', '[':
		s.containers = append(s.containers, newJSONContainer(delim))
	case ']':
		if current.delim != '[' {
			return false
		}
		s.closeContainer()
	default:
		return false
	}
	return true
}

func (s *jsonAmbiguityScanner) closeContainer() {
	s.containers = s.containers[:len(s.containers)-1]
	if len(s.containers) == 0 {
		s.rootComplete = true
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
