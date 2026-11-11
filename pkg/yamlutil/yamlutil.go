// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package yamlutil provides YAML decoding helpers shared across ToolHive.
package yamlutil

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// Parse parses YAML data into a node tree and rejects duplicate mapping keys
// in linear time, using the same rule as yaml.Unmarshal (same node kind and
// value). It returns nil for an empty document.
//
// yaml.Unmarshal into a Go value checks mapping keys for duplicates pairwise
// and formats an error for every duplicate pair, so a document of repeated
// keys costs quadratic time and memory (minutes for 64KB). Parsing into a
// yaml.Node skips that check.
//
// Decoding the returned node into a Go value still compares every pair of
// keys in each mapping it visits, which stays quadratic in the number of
// distinct keys. Callers handling large untrusted input should read the
// values they need from the node tree instead of decoding it.
func Parse(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 {
		return nil, nil
	}
	if err := checkDuplicateKeys(&doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// Unmarshal decodes YAML data into out like yaml.Unmarshal, but rejects
// duplicate mapping keys in linear time first, so a document of repeated
// keys cannot trigger the yaml package's quadratic duplicate-key reporting.
// See Parse for the cost that remains for many distinct keys.
func Unmarshal(data []byte, out any) error {
	doc, err := Parse(data)
	if err != nil || doc == nil {
		return err
	}
	return doc.Decode(out)
}

// checkDuplicateKeys rejects any mapping in the tree that defines the same
// key twice, matching the yaml package's own rule (same node kind and value).
// Alias nodes are not followed: the node they point to is checked where it is
// defined, and following aliases would revisit shared subtrees.
func checkDuplicateKeys(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		type mapKey struct {
			kind  yaml.Kind
			value string
		}
		seen := make(map[mapKey]int, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			mk := mapKey{kind: k.Kind, value: k.Value}
			if line, ok := seen[mk]; ok {
				return fmt.Errorf("line %d: mapping key %q already defined at line %d", k.Line, k.Value, line)
			}
			seen[mk] = k.Line
		}
	}
	if n.Kind == yaml.AliasNode {
		return nil
	}
	for _, c := range n.Content {
		if err := checkDuplicateKeys(c); err != nil {
			return err
		}
	}
	return nil
}

// MappingValue returns the value stored under key in the mapping n, or nil
// if n is not a mapping or has no such key. A document node is looked
// through to its root, and alias nodes (for n and for the returned value)
// are resolved. Merge keys ("<<") are not expanded.
func MappingValue(n *yaml.Node, key string) *yaml.Node {
	n = resolve(n)
	if n != nil && n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = resolve(n.Content[0])
	}
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return resolve(n.Content[i+1])
		}
	}
	return nil
}

func resolve(n *yaml.Node) *yaml.Node {
	if n != nil && n.Kind == yaml.AliasNode {
		return n.Alias
	}
	return n
}
