// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package callbinding holds the pieces the vMCP uses to bind an access token
// to the one MCP call its authorization_details entry names.
//
// The argument hash is the shared contract between the client that asks for
// a call-bound token and the vMCP that checks it: both compute it over
// params.arguments, so any input on which the two differ makes that call fail.
// Both sides therefore refuse arguments rather than risk disagreeing on them.
package callbinding

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// ErrNotIJSON is returned when params.arguments is not exactly one JSON object
// that is I-JSON (RFC 7493), so it has no argument hash.
var ErrNotIJSON = errors.New("callbinding: arguments are not I-JSON")

// ArgumentsSHA256 returns the argument hash of params.arguments: the lowercase
// hex SHA-256 of its RFC 8785 (JCS) serialization. args is the raw value as
// received, before any routing or rewriting. An absent value (empty, or only
// JSON whitespace) and JSON null both hash as {}.
//
// It returns an error for which errors.Is(err, ErrNotIJSON) holds for
// anything that is not exactly one JSON object, or that is not I-JSON: invalid
// UTF-8, lone or reversed surrogate escapes, duplicate member names (including
// ones equal only after decoding escapes), non-finite numbers, and numbers
// with magnitude above 2^53. The error never quotes the arguments.
//
// Arguments that differ only in what RFC 8785 normalizes hash the same:
// member order, whitespace, string escapes, and number spelling, including
// -0 against 0 and digits beyond what an IEEE 754 double holds. A caller that
// forwards the arguments therefore authorizes every such spelling of them.
func ArgumentsSHA256(args json.RawMessage) (string, error) {
	canonical, err := canonicalArguments(args)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalArguments returns the RFC 8785 form of args, with the refusals of
// ArgumentsSHA256.
func canonicalArguments(args json.RawMessage) ([]byte, error) {
	// Only JSON whitespace counts as absent. Other Unicode white space is not
	// JSON, so it is left to checkIJSONObject to refuse.
	if trimmed := bytes.Trim(args, jsonWhitespace); len(trimmed) == 0 || string(trimmed) == "null" {
		args = json.RawMessage(`{}`)
	}
	if err := checkIJSONObject(args); err != nil {
		return nil, err
	}
	// checkIJSONObject has already refused everything Canonicalize refuses;
	// the check stays so that a difference between the two fails closed.
	v := jsontext.Value(bytes.Clone(args))
	if v.Canonicalize() != nil {
		return nil, ErrNotIJSON
	}
	return v, nil
}

// jsonWhitespace is the insignificant whitespace of RFC 8259 section 2.
const jsonWhitespace = " \t\r\n"

// checkIJSONObject refuses raw unless it is exactly one JSON object that is
// I-JSON (RFC 7493). jsontext's strict decoding refuses invalid UTF-8, lone
// surrogate escapes, and duplicate member names; the number range, which
// Canonicalize does not check, is checked here.
func checkIJSONObject(raw []byte) error {
	dec := jsontext.NewDecoder(bytes.NewReader(raw))
	for first := true; first || dec.StackDepth() > 0; first = false {
		tok, err := dec.ReadToken()
		if err != nil {
			// jsontext's error quotes parts of the input; name the cause
			// without it so a caller that logs the error never logs arguments.
			if errors.Is(err, jsontext.ErrDuplicateName) {
				return fmt.Errorf("%w: duplicate member name", ErrNotIJSON)
			}
			return fmt.Errorf("%w: invalid JSON or UTF-8", ErrNotIJSON)
		}
		switch {
		case first && tok.Kind() != '{':
			return fmt.Errorf("%w: not a JSON object", ErrNotIJSON)
		case tok.Kind() == '0':
			if err := checkNumber(tok.String()); err != nil {
				return err
			}
		}
	}
	if _, err := dec.ReadToken(); err != io.EOF {
		return fmt.Errorf("%w: data after the object", ErrNotIJSON)
	}
	return nil
}

// maxExactInteger is 2^53: every integer of magnitude up to 2^53 is exactly
// an IEEE 754 double, and every double of greater magnitude is an integer.
// RFC 7493 section 2.2 advises integers within ±(2^53-1); the shared contract
// also accepts ±2^53 itself, which is exact.
const maxExactInteger = 1 << 53

// checkNumber refuses a JSON number literal that is not finite as an IEEE 754
// double or whose magnitude is above 2^53, including a literal such as
// 9007199254740992.5 that rounds to 2^53 without denoting it.
func checkNumber(lit string) error {
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return fmt.Errorf("%w: number out of range", ErrNotIJSON)
	}
	if a := math.Abs(f); a > maxExactInteger || (a == maxExactInteger && !isExactly2To53(lit)) {
		return fmt.Errorf("%w: number beyond 2^53", ErrNotIJSON)
	}
	return nil
}

// isExactly2To53 reports whether the decimal literal lit, which rounds to
// ±2^53, denotes exactly ±2^53 rather than a neighbour such as 2^53+1.
func isExactly2To53(lit string) bool {
	lit = strings.TrimPrefix(lit, "-")
	mant, exp := lit, 0
	if i := strings.IndexAny(lit, "eE"); i >= 0 {
		e, err := strconv.Atoi(lit[i+1:])
		if err != nil {
			return false
		}
		mant, exp = lit[:i], e
	}
	intPart, frac, _ := strings.Cut(mant, ".")
	digits := intPart + frac
	point := len(intPart) + exp // digits[:point] is the integer part
	trimmed := strings.TrimLeft(digits, "0")
	point -= len(digits) - len(trimmed)
	return strings.TrimRight(trimmed, "0") == "9007199254740992" && point == 16
}
