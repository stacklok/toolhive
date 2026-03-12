// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"sync"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

// AnnotationCache stores tool annotations indexed by tool name.
// It is safe for concurrent use and nil-safe: calling methods on a nil
// *AnnotationCache is a no-op that returns zero values.
//
// The cache is populated from tools/list responses (via SetFromToolsList)
// and read during tools/call authorization (via Get). This bridges the gap
// between the two separate HTTP requests: annotations are visible in the
// tools/list response but absent from the tools/call request body.
type AnnotationCache struct {
	mu    sync.RWMutex
	tools map[string]*authorizers.ToolAnnotations
}

// NewAnnotationCache creates a new empty annotation cache.
func NewAnnotationCache() *AnnotationCache {
	return &AnnotationCache{
		tools: make(map[string]*authorizers.ToolAnnotations),
	}
}

// Get retrieves annotations for a tool by name. Returns nil if the tool
// is not cached or if the cache itself is nil.
func (c *AnnotationCache) Get(toolName string) *authorizers.ToolAnnotations {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tools[toolName]
}

// Set stores annotations for a single tool. It is a no-op if the cache
// is nil.
func (c *AnnotationCache) Set(toolName string, annotations *authorizers.ToolAnnotations) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tools[toolName] = annotations
}

// SetFromToolsList extracts annotations from a tools/list response and
// replaces the entire cache contents. The full replacement ensures that
// tools whose annotations were removed in a subsequent tools/list response
// do not retain stale cached entries.
//
// Only tools that have at least one non-nil annotation hint are cached;
// tools with all-nil hints (the zero value) are skipped to avoid
// unnecessary memory consumption.
//
// This method is a no-op if the cache is nil.
func (c *AnnotationCache) SetFromToolsList(tools []mcp.Tool) {
	if c == nil {
		return
	}
	newTools := make(map[string]*authorizers.ToolAnnotations, len(tools))
	for i := range tools {
		ann := &tools[i].Annotations
		if !hasAnyHint(ann) {
			continue
		}
		newTools[tools[i].Name] = convertMCPAnnotation(ann)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tools = newTools
}

// hasAnyHint reports whether the MCP tool annotation has at least one
// non-nil hint field.
func hasAnyHint(ann *mcp.ToolAnnotation) bool {
	return ann.ReadOnlyHint != nil ||
		ann.DestructiveHint != nil ||
		ann.IdempotentHint != nil ||
		ann.OpenWorldHint != nil
}

// convertMCPAnnotation converts an mcp-go ToolAnnotation to the authz
// ToolAnnotations type used by authorizers. Only hint fields are copied;
// the Title field is intentionally omitted because authorizers only use
// hints for policy decisions.
func convertMCPAnnotation(ann *mcp.ToolAnnotation) *authorizers.ToolAnnotations {
	return &authorizers.ToolAnnotations{
		ReadOnlyHint:    ann.ReadOnlyHint,
		DestructiveHint: ann.DestructiveHint,
		IdempotentHint:  ann.IdempotentHint,
		OpenWorldHint:   ann.OpenWorldHint,
	}
}
