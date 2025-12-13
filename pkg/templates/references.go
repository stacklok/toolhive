// Package templates provides utilities for parsing and analyzing Go templates.
package templates

import (
	"strings"
	"text/template"
	"text/template/parse"
)

// ExtractReferences finds all field references in a template string.
// Returns the full field paths (e.g., ".steps.step1.output", ".params.message").
func ExtractReferences(tmplStr string) ([]string, error) {
	tmpl, err := template.New("extract").Parse(tmplStr)
	if err != nil {
		return nil, err
	}
	return ExtractReferencesFromTemplate(tmpl), nil
}

// ExtractReferencesFromTemplate finds all field references in a parsed template.
func ExtractReferencesFromTemplate(tmpl *template.Template) []string {
	references := make(map[string]bool)

	for _, t := range tmpl.Templates() {
		if t.Root != nil {
			walkNode(t.Root, references)
		}
	}

	// Convert map to slice
	result := make([]string, 0, len(references))
	for ref := range references {
		result = append(result, ref)
	}
	return result
}

//nolint:gocyclo // walkNode has to reason about many potential node types from the underlying template package.
func walkNode(node parse.Node, refs map[string]bool) {
	if node == nil {
		return
	}

	switch n := node.(type) {
	case *parse.ListNode:
		if n != nil {
			for _, child := range n.Nodes {
				walkNode(child, refs)
			}
		}
	case *parse.ActionNode:
		walkNode(n.Pipe, refs)
	case *parse.PipeNode:
		for _, cmd := range n.Cmds {
			walkNode(cmd, refs)
		}
		// Also walk variable declarations
		for _, variable := range n.Decl {
			walkNode(variable, refs)
		}
	case *parse.CommandNode:
		for _, arg := range n.Args {
			walkNode(arg, refs)
		}
	case *parse.FieldNode:
		// This is something like .foo or .data.foo
		// FieldNode.Ident contains the path segments
		ref := "." + strings.Join(n.Ident, ".")
		refs[ref] = true
	case *parse.ChainNode:
		// ChainNode is for chained field access like (.foo).bar
		walkNode(n.Node, refs)
		if len(n.Field) > 0 {
			ref := "." + strings.Join(n.Field, ".")
			refs[ref] = true
		}
	case *parse.IfNode:
		walkNode(n.Pipe, refs)
		walkNode(n.List, refs)
		if n.ElseList != nil {
			walkNode(n.ElseList, refs)
		}
	case *parse.RangeNode:
		walkNode(n.Pipe, refs)
		walkNode(n.List, refs)
		if n.ElseList != nil {
			walkNode(n.ElseList, refs)
		}
	case *parse.WithNode:
		walkNode(n.Pipe, refs)
		walkNode(n.List, refs)
		if n.ElseList != nil {
			walkNode(n.ElseList, refs)
		}
	case *parse.TemplateNode:
		walkNode(n.Pipe, refs)
	default:
		// The other nodes do not potentially contain field references.
	}
}
