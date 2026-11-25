// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	// maxResourceTemplateLength bounds the byte length of a backend-supplied
	// RFC 6570 URI template. Real templates are a few dozen bytes.
	maxResourceTemplateLength = 1024

	// maxTemplatePrefixLength bounds the sum of every prefix modifier
	// ({var:N}) in one template. The URI template library unrolls each
	// modifier into roughly 7*N program ops and sizes its matcher state from
	// the program, so this sum is the dominant term in the memory and CPU cost
	// of matching. RFC 6570 allows N up to 9999 per variable.
	maxTemplatePrefixLength = 256

	// maxTemplateVariables bounds the total number of variables across every
	// expression in one template. The matcher copies capture state for every
	// live thread at every input character, so its cost grows faster than
	// linearly with the variable count even when the template is short and has
	// no prefix modifiers. Real templates have a handful of variables; with
	// maxResourceURILength this keeps one Match to tens of MiB.
	maxTemplateVariables = 8
)

var (
	// templateExpressionRE matches one {...} expression. RFC 6570 forbids raw
	// braces in literals, so prefix modifiers can only appear inside these.
	templateExpressionRE = regexp.MustCompile(`\{[^}]*\}`)

	// prefixModifierRE matches the :N prefix modifier within an expression.
	// Variable names cannot contain a colon, so it is unambiguous there.
	prefixModifierRE = regexp.MustCompile(`:(\d+)`)
)

// validateResourceTemplate reports whether a backend-supplied URI template is
// within the length, prefix-modifier, and variable-count bounds vMCP is willing
// to compile and match. It does not
// check RFC 6570 syntax: a template that fails to parse is already skipped,
// and logged, when the router is built, and it is never compiled.
//
// Only prefix modifiers are read, so a colon in a literal such as
// "urn:x:{id}" is not counted.
func validateResourceTemplate(tmpl string) error {
	if len(tmpl) > maxResourceTemplateLength {
		return fmt.Errorf("template is %d bytes, limit is %d", len(tmpl), maxResourceTemplateLength)
	}

	prefixTotal := 0
	variables := 0
	for _, expr := range templateExpressionRE.FindAllString(tmpl, -1) {
		// Variables in one expression are comma-separated; the operator, if
		// any, is a single leading character that never contains a comma.
		variables += strings.Count(expr, ",") + 1
		if variables > maxTemplateVariables {
			return fmt.Errorf("template has more than %d variables", maxTemplateVariables)
		}
		for _, m := range prefixModifierRE.FindAllStringSubmatch(expr, -1) {
			// An unparsable count is an overflowing digit run, so over the limit.
			n, err := strconv.Atoi(m[1])
			if err != nil || n > maxTemplatePrefixLength {
				return fmt.Errorf("prefix modifier exceeds %d characters", maxTemplatePrefixLength)
			}
			prefixTotal += n
		}
	}
	if prefixTotal > maxTemplatePrefixLength {
		return fmt.Errorf("prefix modifiers total %d characters, limit is %d", prefixTotal, maxTemplatePrefixLength)
	}
	return nil
}
