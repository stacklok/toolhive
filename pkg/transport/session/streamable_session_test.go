// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamableSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		action           func(t *testing.T, s *StreamableSession)
		wantDisconnected bool
	}{
		{
			name:             "new session is not disconnected",
			action:           func(_ *testing.T, _ *StreamableSession) {},
			wantDisconnected: false,
		},
		{
			name: "Disconnect marks session as disconnected",
			action: func(_ *testing.T, s *StreamableSession) {
				s.Disconnect()
			},
			wantDisconnected: true,
		},
		{
			name: "double Disconnect does not panic",
			action: func(_ *testing.T, s *StreamableSession) {
				s.Disconnect()
				s.Disconnect()
			},
			wantDisconnected: true,
		},
		{
			name: "GetData returns nil",
			action: func(t *testing.T, s *StreamableSession) {
				t.Helper()
				assert.Nil(t, s.GetData())
			},
			wantDisconnected: false,
		},
		{
			name: "Type returns SessionTypeStreamable",
			action: func(t *testing.T, s *StreamableSession) {
				t.Helper()
				assert.Equal(t, SessionTypeStreamable, s.Type())
			},
			wantDisconnected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sess := NewStreamableSession("test-" + tt.name)
			s, ok := sess.(*StreamableSession)
			require.True(t, ok)

			tt.action(t, s)

			assert.Equal(t, tt.wantDisconnected, s.disconnected)
		})
	}
}
