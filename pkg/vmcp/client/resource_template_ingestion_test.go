// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

// TestNewCapabilityListFromMCP_ResourceTemplateBounds pins the ingestion bound
// on backend-supplied URI templates. The URI template library compiles a
// prefix modifier such as {a:9999} into ~70,000 program ops and allocates
// matcher state proportional to that on every Match, so an unbounded template
// from a backend lets one backend exhaust vMCP's memory (GHSA-2f8h-v47r-j3c3).
func TestNewCapabilityListFromMCP_ResourceTemplateBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template string
		accepted bool
	}{
		{name: "plain variable", template: "file:///{path}", accepted: true},
		{name: "reserved expansion", template: "file:///{+path}", accepted: true},
		{name: "query expansion", template: "search://{?q,page,limit}", accepted: true},
		{name: "colon in literal is not a prefix modifier", template: "urn:x:{id}", accepted: true},
		{name: "small prefix modifier", template: "repo://{owner:32}/{name:64}", accepted: true},
		{name: "prefix budget exactly at the limit", template: fmt.Sprintf("x{a:%d}", maxTemplatePrefixLength), accepted: true},
		{name: "prefix modifier over the limit", template: fmt.Sprintf("x{a:%d}", maxTemplatePrefixLength+1), accepted: false},
		{name: "maximum RFC 6570 prefix modifier", template: "x{a:9999}", accepted: false},
		{name: "many small prefixes exceed the budget together", template: "x" + strings.Repeat("{a:64}", 5), accepted: false},
		{name: "the reported repeated-prefix payload", template: "x{" + strings.Repeat("a:9999,", 999) + "a:9999}", accepted: false},
		{name: "variables exactly at the limit", template: "x{" + strings.Repeat("v,", maxTemplateVariables-1) + "v}", accepted: true},
		{name: "one variable over the limit", template: "x{+" + strings.Repeat("v,", maxTemplateVariables) + "v}", accepted: false},
		{name: "variables spread across expressions", template: "x" + strings.Repeat("{+a}", maxTemplateVariables+1), accepted: false},
		{name: "many variables with no prefix modifiers", template: "x{+" + strings.Repeat("v,", 224) + "v}", accepted: false},
		{name: "template longer than the limit", template: "x" + strings.Repeat("y", maxResourceTemplateLength), accepted: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// A second, well-formed template proves the offender is dropped
			// alone rather than taking the backend's other templates with it.
			templates := []mcp.ResourceTemplate{
				{Name: "offender", URITemplate: tt.template},
				{Name: "bystander", URITemplate: "file:///{path}"},
			}

			caps := newCapabilityListFromMCP("backend-1", nil, nil, templates, nil)

			names := make([]string, 0, len(caps.ResourceTemplates))
			for _, got := range caps.ResourceTemplates {
				names = append(names, got.Name)
				assert.Equal(t, "backend-1", got.BackendID)
			}
			assert.Contains(t, names, "bystander")
			if tt.accepted {
				assert.Contains(t, names, "offender")
			} else {
				assert.NotContains(t, names, "offender")
			}
		})
	}
}
