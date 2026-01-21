// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package templates

import (
	"sort"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractReferences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tmplStr  string
		expected []string
	}{
		{
			name:     "simple field",
			tmplStr:  "{{ .data.foo }}",
			expected: []string{".data.foo"},
		},
		{
			name:     "multiple fields",
			tmplStr:  "{{ .user.name }} - {{ .user.email }}",
			expected: []string{".user.email", ".user.name"},
		},
		{
			name:     "conditional with field",
			tmplStr:  "{{ if .admin }}Admin{{ end }}",
			expected: []string{".admin"},
		},
		{
			name:     "range with inner reference",
			tmplStr:  "{{ range .items }}{{ .id }}{{ end }}",
			expected: []string{".id", ".items"},
		},
		{
			name:     "nested fields",
			tmplStr:  "{{ .steps.step1.output.data }}",
			expected: []string{".steps.step1.output.data"},
		},
		{
			name:     "function with field argument",
			tmplStr:  `{{ eq .steps.step1.status "completed" }}`,
			expected: []string{".steps.step1.status"},
		},
		{
			name:     "complex template",
			tmplStr:  `{{ if eq .steps.step1.status "ok" }}{{ .steps.step1.data }}{{ else }}{{ .params.default }}{{ end }}`,
			expected: []string{".params.default", ".steps.step1.data", ".steps.step1.status"},
		},
		{
			name:     "no references",
			tmplStr:  "static text",
			expected: []string{},
		},
		{
			name:     "only params",
			tmplStr:  "{{ .params.message }}",
			expected: []string{".params.message"},
		},
		{
			name:     "deduplicate same reference",
			tmplStr:  "{{ .steps.step1.a }} {{ .steps.step1.a }}",
			expected: []string{".steps.step1.a"},
		},
		{
			name:     "with node",
			tmplStr:  "{{ with .context }}{{ .value }}{{ end }}",
			expected: []string{".context", ".value"},
		},
		{
			name:     "if else chain",
			tmplStr:  "{{ if .a }}A{{ else if .b }}B{{ else }}{{ .c }}{{ end }}",
			expected: []string{".a", ".b", ".c"},
		},
		{
			name:     "pipe with variable declaration",
			tmplStr:  "{{ $x := .source.value }}{{ $x }}",
			expected: []string{".source.value"},
		},
		{
			name:     "range with else",
			tmplStr:  "{{ range .items }}{{ .id }}{{ else }}{{ .fallback }}{{ end }}",
			expected: []string{".fallback", ".id", ".items"},
		},
		{
			name:     "with else",
			tmplStr:  "{{ with .data }}{{ .value }}{{ else }}{{ .default }}{{ end }}",
			expected: []string{".data", ".default", ".value"},
		},
		{
			name:     "chain node from function result",
			tmplStr:  "{{ (index .mapping .key).nested }}",
			expected: []string{".key", ".mapping", ".nested"},
		},
		{
			name:     "multiple variable declarations",
			tmplStr:  "{{ $a := .first }}{{ $b := .second }}{{ $a }}{{ $b }}",
			expected: []string{".first", ".second"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			refs, err := ExtractReferences(tt.tmplStr)
			require.NoError(t, err)

			sort.Strings(refs)
			sort.Strings(tt.expected)
			assert.Equal(t, tt.expected, refs)
		})
	}
}

func TestExtractReferences_InvalidTemplate(t *testing.T) {
	t.Parallel()

	_, err := ExtractReferences("{{ .unclosed")
	assert.Error(t, err)
}

func TestExtractReferencesFromTemplate(t *testing.T) {
	t.Parallel()

	// Test that it works with pre-parsed templates
	tmpl, err := template.New("test").Parse("{{ .foo.bar }}")
	require.NoError(t, err)

	refs := ExtractReferencesFromTemplate(tmpl)
	assert.Equal(t, []string{".foo.bar"}, refs)
}

func TestExtractReferencesFromTemplate_NilTree(t *testing.T) {
	t.Parallel()

	// Create an empty template
	tmpl := template.New("empty")

	refs := ExtractReferencesFromTemplate(tmpl)
	assert.Empty(t, refs)
}
