// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// ogen-client-wrapper renames ogen's generated constructor so the SDK can provide its safe public wrapper.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: ogen-client-wrapper <oas_client_gen.go>")
		os.Exit(2)
	}
	if err := renameConstructor(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func renameConstructor(path string) error {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse generated client: %w", err)
	}

	var constructors int
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Name.Name != "NewClient" {
			continue
		}
		function.Name.Name = "NewUnsafeClient"
		if function.Doc != nil {
			for _, comment := range function.Doc.List {
				comment.Text = strings.Replace(comment.Text, "NewClient", "NewUnsafeClient", 1)
			}
		}
		constructors++
	}
	if constructors != 1 {
		return fmt.Errorf("expected exactly one generated NewClient constructor, found %d", constructors)
	}

	var output bytes.Buffer
	if err := format.Node(&output, fileSet, file); err != nil {
		return fmt.Errorf("format generated client: %w", err)
	}
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write generated client: %w", err)
	}
	return nil
}
