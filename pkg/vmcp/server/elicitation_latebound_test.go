// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// fakeVMCPElicitationRequester is a vmcp.ElicitationRequester that records the
// forwarded request and returns a canned result/error, so the late-bound wrapper's
// delegation can be asserted. Not safe for concurrent use — single-goroutine tests
// only (the concurrency test below binds a stateless requester instead).
type fakeVMCPElicitationRequester struct {
	calls      int
	gotRequest vmcp.ElicitationRequest
	result     *vmcp.ElicitationResult
	err        error
}

func (f *fakeVMCPElicitationRequester) RequestElicitation(
	_ context.Context, req vmcp.ElicitationRequest,
) (*vmcp.ElicitationResult, error) {
	f.calls++
	f.gotRequest = req
	return f.result, f.err
}

// nopElicitationRequester is a stateless vmcp.ElicitationRequester for the
// concurrency test, where a recording fake would itself race.
type nopElicitationRequester struct{}

func (nopElicitationRequester) RequestElicitation(
	context.Context, vmcp.ElicitationRequest,
) (*vmcp.ElicitationResult, error) {
	return &vmcp.ElicitationResult{Action: "accept"}, nil
}

// TestLateBoundElicitationRequester_RequestBeforeBind covers the guard path: an
// elicitation fired before bind (i.e. during construction rather than at request
// time) returns an error and does not panic dereferencing the nil target.
func TestLateBoundElicitationRequester_RequestBeforeBind(t *testing.T) {
	t.Parallel()

	l := NewLateBoundElicitationRequester()
	res, err := l.RequestElicitation(context.Background(), vmcp.ElicitationRequest{Message: "hi"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "before the SDK server was bound")
}

// TestLateBoundElicitationRequester_DelegatesAfterBind asserts that, once bound, the
// wrapper forwards the request verbatim to the bound requester and returns its result
// and error unchanged — the contract the core relies on at workflow-elicitation time.
func TestLateBoundElicitationRequester_DelegatesAfterBind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		result  *vmcp.ElicitationResult
		err     error
		wantErr bool
	}{
		{
			"accept result forwarded",
			&vmcp.ElicitationResult{Action: "accept", Content: map[string]any{"k": "v"}}, nil, false,
		},
		{"delegated error forwarded", nil, errors.New("transport closed"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			target := &fakeVMCPElicitationRequester{result: tc.result, err: tc.err}
			l := NewLateBoundElicitationRequester()
			l.Bind(target)

			req := vmcp.ElicitationRequest{Message: "approve?", RequestedSchema: map[string]any{"type": "object"}}
			res, err := l.RequestElicitation(context.Background(), req)

			assert.Equal(t, 1, target.calls, "request must reach the bound requester exactly once")
			assert.Equal(t, req, target.gotRequest, "request must be forwarded verbatim")
			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.Same(t, tc.result, res, "the bound requester's result must be returned unchanged")
		})
	}
}

// TestLateBoundElicitationRequester_ConcurrentBindAndRequest exercises the RWMutex
// guarding the target field: readers calling RequestElicitation concurrently with the
// one-time bind must not race. Run under -race to detect unsynchronized access; the
// returned value is intentionally unasserted since it depends on bind ordering.
func TestLateBoundElicitationRequester_ConcurrentBindAndRequest(t *testing.T) {
	t.Parallel()

	l := NewLateBoundElicitationRequester()

	const readers = 16
	var wg sync.WaitGroup
	wg.Add(readers)
	for range readers {
		go func() {
			defer wg.Done()
			_, _ = l.RequestElicitation(context.Background(), vmcp.ElicitationRequest{Message: "x"})
		}()
	}
	l.Bind(nopElicitationRequester{}) // the one-time write, concurrent with the readers above

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for concurrent elicitation readers")
	}
}
