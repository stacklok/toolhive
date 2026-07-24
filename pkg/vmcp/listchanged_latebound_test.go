// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// recordingListChangedNotifier records every NotifyBackendListChanged call.
type recordingListChangedNotifier struct {
	calls []struct {
		backendID string
		kind      ListChangedKind
	}
}

func (r *recordingListChangedNotifier) NotifyBackendListChanged(backendID string, kind ListChangedKind) {
	r.calls = append(r.calls, struct {
		backendID string
		kind      ListChangedKind
	}{backendID, kind})
}

// TestLateBoundListChangedNotifier_UnboundIsNoop verifies that a notification
// delivered before Bind is silently dropped rather than panicking.
func TestLateBoundListChangedNotifier_UnboundIsNoop(t *testing.T) {
	t.Parallel()

	l := NewLateBoundListChangedNotifier()
	assert.NotPanics(t, func() {
		l.NotifyBackendListChanged("backend-1", ListChangedTools)
	})
}

// TestLateBoundListChangedNotifier_BoundForwards verifies that once Bind is
// called, notifications are forwarded to the bound target with the exact
// arguments.
func TestLateBoundListChangedNotifier_BoundForwards(t *testing.T) {
	t.Parallel()

	l := NewLateBoundListChangedNotifier()
	target := &recordingListChangedNotifier{}
	l.Bind(target)

	l.NotifyBackendListChanged("backend-1", ListChangedResources)
	l.NotifyBackendListChanged("backend-2", ListChangedPrompts)

	require := assert.New(t)
	require.Len(target.calls, 2)
	require.Equal("backend-1", target.calls[0].backendID)
	require.Equal(ListChangedResources, target.calls[0].kind)
	require.Equal("backend-2", target.calls[1].backendID)
	require.Equal(ListChangedPrompts, target.calls[1].kind)
}
