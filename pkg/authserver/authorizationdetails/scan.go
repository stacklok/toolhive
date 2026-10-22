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
	frames := make([]scanFrame, 0, 8)
	started := false
	done := false

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return done
		}
		if err != nil || done {
			return false
		}
		delim, isDelim := token.(json.Delim)
		if !started {
			if !isDelim || delim != '[' {
				return false
			}
			started = true
		}

		// Token enforces the JSON syntax: bracket pairing, string keys in key
		// position and separators. Only duplicates, depth and the single root
		// array remain to be checked here.
		if n := len(frames); n > 0 && frames[n-1].object && frames[n-1].expectKey && !isDelim {
			key, _ := token.(string)
			top := &frames[n-1]
			// The folded set alone catches exact and case-variant duplicates.
			folded := foldKey(key)
			if _, dup := top.folded[folded]; dup {
				return false
			}
			top.folded[folded] = struct{}{}
			top.expectKey = false
			continue
		}

		if isDelim && (delim == '{' || delim == '[') {
			if len(frames) == maxDepth {
				return false
			}
			frames = append(frames, newScanFrame(delim == '{'))
			continue
		}
		if isDelim {
			frames = frames[:len(frames)-1]
		}

		// A scalar or a whole container just ended.
		if n := len(frames); n == 0 {
			done = true
		} else if frames[n-1].object {
			frames[n-1].expectKey = true
		}
	}
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
