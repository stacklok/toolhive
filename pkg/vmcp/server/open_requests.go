// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"sync"
)

// errSessionTerminated is the cancellation cause of a request whose session the
// client terminated with DELETE.
var errSessionTerminated = errors.New("session terminated by the client")

// openRequests tracks the Legacy requests still running on each session, so
// that a client's DELETE can cancel them.
//
// The transport answers DELETE by closing the session, and go-sdk's session
// close waits for in-flight requests to finish without cancelling them. A
// tools/call waiting on a slow backend would otherwise hold the DELETE until
// the backend request timeout, and then for the backend client's own DELETE.
// Cancelling first lets the handlers return at once.
//
// The zero value is ready to use.
type openRequests struct {
	mu sync.Mutex
	// sessions maps a session ID to the cancel functions of its open
	// requests. ended holds the sessions whose DELETE is in progress: a
	// request that starts on one of them is cancelled at once.
	sessions map[string]map[*context.CancelCauseFunc]struct{}
	ended    map[string]struct{}
}

// begin records a request on sessionID and returns its context, which end
// cancels, and end, which the caller defers. A request without a session is
// not tracked.
func (o *openRequests) begin(ctx context.Context, sessionID string) (context.Context, func()) {
	if sessionID == "" {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancelCause(ctx)

	o.mu.Lock()
	if _, ended := o.ended[sessionID]; ended {
		o.mu.Unlock()
		cancel(errSessionTerminated)
		return ctx, func() {}
	}
	if o.sessions == nil {
		o.sessions = make(map[string]map[*context.CancelCauseFunc]struct{})
	}
	reqs := o.sessions[sessionID]
	if reqs == nil {
		reqs = make(map[*context.CancelCauseFunc]struct{})
		o.sessions[sessionID] = reqs
	}
	reqs[&cancel] = struct{}{}
	o.mu.Unlock()

	return ctx, func() {
		o.mu.Lock()
		if reqs := o.sessions[sessionID]; reqs != nil {
			delete(reqs, &cancel)
			if len(reqs) == 0 {
				delete(o.sessions, sessionID)
			}
		}
		o.mu.Unlock()
		cancel(nil)
	}
}

// endSession cancels every open request on sessionID, and every request that
// starts on it until the returned function is called. The caller calls that
// function once the session is closed.
func (o *openRequests) endSession(sessionID string) func() {
	o.mu.Lock()
	reqs := o.sessions[sessionID]
	delete(o.sessions, sessionID)
	if o.ended == nil {
		o.ended = make(map[string]struct{})
	}
	o.ended[sessionID] = struct{}{}
	o.mu.Unlock()

	for cancel := range reqs {
		(*cancel)(errSessionTerminated)
	}
	return func() {
		o.mu.Lock()
		delete(o.ended, sessionID)
		o.mu.Unlock()
	}
}
