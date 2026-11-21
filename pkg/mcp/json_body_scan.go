// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import "encoding/json"

// jsonBodyScan reads a JSON body for the end of its top-level value as the
// body arrives, so that a body that arrives in many writes is read once and
// validated once, when its value has closed, instead of being validated whole
// at every flush. Its zero value is ready to read a body from its first byte.
//
// Only a body whose first byte opens an object or array is read this way: the
// scan tracks the depth of the objects and arrays open outside strings, and
// the value has closed when that depth returns to zero. A body that starts
// otherwise holds a scalar, which no client reads a tools list from, and is
// validated whole at every call, since a scalar may end at any byte.
type jsonBodyScan struct {
	// read is the length of the prefix of the body the scan has read.
	read int
	// state is where the scan stands in the body.
	state jsonScanState
	// depth is the number of objects and arrays open at read.
	depth int
	// inString reports that read is inside a string, and escaped that the
	// byte at read follows the backslash that escapes it.
	inString, escaped bool
	// valid is the verdict on the body once its top-level value has closed.
	valid bool
}

// jsonScanState is where a jsonBodyScan stands in the body it reads.
type jsonScanState int

const (
	// jsonScanStart means the scan has not found the first byte of the value.
	jsonScanStart jsonScanState = iota
	// jsonScanContainer means the value is an object or array that has not
	// closed yet.
	jsonScanContainer
	// jsonScanClosed means the top-level value has closed, and valid holds the
	// verdict on the body: it stays valid only while white space follows.
	jsonScanClosed
	// jsonScanScalar means the value is neither an object nor an array.
	jsonScanScalar
)

// isValid reports whether body is one valid JSON value, as json.Valid does,
// reading only the bytes appended since the previous call. body must start
// with the bytes the previous call read; a new body takes a new scan.
func (s *jsonBodyScan) isValid(body []byte) bool {
	if s.state == jsonScanStart {
		for s.read < len(body) && isJSONSpace(body[s.read]) {
			s.read++
		}
		if s.read == len(body) {
			return false
		}
		s.state = jsonScanScalar
		if c := body[s.read]; c == '{' || c == '[' {
			s.state = jsonScanContainer
		}
	}

	switch s.state {
	case jsonScanContainer:
		for s.read < len(body) {
			s.readContainerByte(body[s.read])
			s.read++
			if s.depth == 0 {
				s.state = jsonScanClosed
				s.valid = json.Valid(body)
				s.read = len(body)
				return s.valid
			}
		}
		return false

	case jsonScanClosed:
		for ; s.read < len(body); s.read++ {
			if !isJSONSpace(body[s.read]) {
				s.valid = false
			}
		}
		return s.valid

	case jsonScanStart, jsonScanScalar:
		// The scan is past the start once it has read a byte, so only a
		// scalar reaches this case: it may end at any byte, so the body is
		// validated whole.
		s.read = len(body)
		return json.Valid(body)
	}
	return false
}

// readContainerByte reads one byte of an object or array, tracking the depth
// of the containers open outside strings and the string the scan is in.
func (s *jsonBodyScan) readContainerByte(c byte) {
	switch {
	case s.inString:
		switch {
		case s.escaped:
			s.escaped = false
		case c == '\\':
			s.escaped = true
		case c == '"':
			s.inString = false
		}
	case c == '"':
		s.inString = true
	case c == '{' || c == '[':
		s.depth++
	case c == '}' || c == ']':
		s.depth--
	}
}

// isJSONSpace reports whether c is white space under the JSON grammar, which
// a JSON value may be surrounded by.
func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
