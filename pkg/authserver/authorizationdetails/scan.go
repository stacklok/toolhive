// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizationdetails

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode"
)

type scanFrame struct {
	object    bool
	expectKey bool
	folded    map[string]struct{}
}

// arrayScanner tracks the container stack while validArray walks the tokens.
type arrayScanner struct {
	frames  []scanFrame
	started bool
	done    bool
}

// validArray reports whether raw is exactly one JSON array, nested at most
// maxDepth containers deep, in which no object repeats a member name.
//
// Member names are compared after JSON unescaping, so "type" and "\u0074ype"
// collide. Names that differ only in letter case also collide, using the same
// simple case folding encoding/json applies when it matches struct fields
// ("s" and "ſ" collide, as do "σ" and "ς"). RFC 9396 does not require that
// second rule: it is ToolHive policy, because a consumer that matches names
// case-insensitively would otherwise read a different value than one that
// matches exactly.
func validArray(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	s := &arrayScanner{frames: make([]scanFrame, 0, 8)}

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return s.done
		}
		if err != nil || s.done || !s.accept(token) {
			return false
		}
	}
}

// accept consumes one token and reports whether the array is still valid.
func (s *arrayScanner) accept(token json.Token) bool {
	delim, isDelim := token.(json.Delim)
	if !s.started {
		if !isDelim || delim != '[' {
			return false
		}
		s.started = true
	}

	// Token enforces the JSON syntax: bracket pairing, string keys in key
	// position and separators. Only duplicates, depth and the single root
	// array remain to be checked here.
	if n := len(s.frames); n > 0 && s.frames[n-1].object && s.frames[n-1].expectKey && !isDelim {
		return s.frames[n-1].addKey(token)
	}
	return s.acceptValue(delim, isDelim)
}

// acceptValue handles a delimiter or scalar outside key position.
func (s *arrayScanner) acceptValue(delim json.Delim, isDelim bool) bool {
	if isDelim && (delim == '{' || delim == '[') {
		if len(s.frames) == maxDepth {
			return false
		}
		s.frames = append(s.frames, newScanFrame(delim == '{'))
		return true
	}
	if isDelim {
		s.frames = s.frames[:len(s.frames)-1]
	}

	// A scalar or a whole container just ended.
	if n := len(s.frames); n == 0 {
		s.done = true
	} else if s.frames[n-1].object {
		s.frames[n-1].expectKey = true
	}
	return true
}

// addKey records an object member name and reports false on a duplicate.
func (f *scanFrame) addKey(token json.Token) bool {
	// Decoder.Token only returns strings in key position, so the assertion holds.
	key, _ := token.(string)
	// The folded set alone catches exact and case-variant duplicates.
	folded := foldKey(key)
	if _, dup := f.folded[folded]; dup {
		return false
	}
	f.folded[folded] = struct{}{}
	f.expectKey = false
	return true
}

func newScanFrame(object bool) scanFrame {
	frame := scanFrame{object: object}
	if object {
		frame.expectKey = true
		frame.folded = make(map[string]struct{})
	}
	return frame
}

// foldKey maps each rune to the smallest rune in its unicode.SimpleFold orbit.
// SimpleFold returns the next larger rune in the orbit and wraps to the
// smallest one, so the loop stops at the orbit minimum.
func foldKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		for {
			next := unicode.SimpleFold(r)
			if next <= r {
				r = next
				break
			}
			r = next
		}
		b.WriteRune(r)
	}
	return b.String()
}
