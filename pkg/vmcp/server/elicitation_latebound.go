// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"sync"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// LateBoundElicitationRequester is a vmcp.ElicitationRequester whose backing
// requester is set once, after the mcp-go server is built by Serve.
//
// Experimental: this type and its Bind method are newly exported for embedders that
// assemble vMCP via pkg/vmcp/app. Although pkg/vmcp/server is marked Stable in
// docs/arch/vmcp-library.md, this surface may change as embedder patterns stabilize.
//
// server.New evaluates core.New(deriveCoreConfig(...)) before Serve, but the
// SDK-backed elicitation adapter (NewSDKElicitationAdapter) wraps the *server.MCPServer
// that Serve creates — a construction-order inversion. The core needs a non-nil
// ElicitationRequester at construction (core.New rejects a nil one when a configured
// workflow contains an elicitation step), but the requester is not invoked until a
// composite workflow runs an elicitation step at request time — always after New has
// bound the real adapter and before the server begins serving. So a nil target is
// unreachable in practice; RequestElicitation guards it anyway.
//
// Safe for concurrent use: Bind happens once during construction (before serving),
// RequestElicitation reads under the same lock.
type LateBoundElicitationRequester struct {
	mu     sync.RWMutex
	target vmcp.ElicitationRequester
}

var _ vmcp.ElicitationRequester = (*LateBoundElicitationRequester)(nil)

// NewLateBoundElicitationRequester creates a new late-bound elicitation requester.
// The caller must call Bind with the real SDK-backed adapter after server.Serve returns
// and before the server starts serving, so composite-tool elicitation steps resolve.
func NewLateBoundElicitationRequester() *LateBoundElicitationRequester {
	return &LateBoundElicitationRequester{}
}

// Bind sets the backing requester. New calls it exactly once, after Serve returns
// and before the server starts serving.
func (l *LateBoundElicitationRequester) Bind(target vmcp.ElicitationRequester) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.target = target
}

// RequestElicitation forwards to the bound requester, returning an error if invoked
// before Bind — which would mean an elicitation fired during construction rather than
// at request time.
func (l *LateBoundElicitationRequester) RequestElicitation(
	ctx context.Context, req vmcp.ElicitationRequest,
) (*vmcp.ElicitationResult, error) {
	l.mu.RLock()
	target := l.target
	l.mu.RUnlock()
	if target == nil {
		return nil, fmt.Errorf("elicitation requested before the SDK server was bound")
	}
	return target.RequestElicitation(ctx, req)
}
