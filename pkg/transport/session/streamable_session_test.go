// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

func TestStreamableSessionLazyChannels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		action          func(t *testing.T, s *StreamableSession)
		wantMsgCh       bool
		wantRespCh      bool
		wantDisconnected bool
	}{
		{
			name:       "channels are nil after construction",
			action:     func(_ *testing.T, _ *StreamableSession) {},
			wantMsgCh:  false,
			wantRespCh: false,
		},
		{
			name: "SendMessage allocates both channels",
			action: func(t *testing.T, s *StreamableSession) {
				t.Helper()
				msg := makeTestRequest(t, "test", 1)
				require.NoError(t, s.SendMessage(msg))
			},
			wantMsgCh:  true,
			wantRespCh: true,
		},
		{
			name: "SendResponse allocates both channels",
			action: func(t *testing.T, s *StreamableSession) {
				t.Helper()
				msg := makeTestRequest(t, "test", 2)
				require.NoError(t, s.SendResponse(msg))
			},
			wantMsgCh:  true,
			wantRespCh: true,
		},
		{
			name: "Disconnect without channel use does not panic",
			action: func(_ *testing.T, s *StreamableSession) {
				s.Disconnect()
			},
			wantMsgCh:        false,
			wantRespCh:       false,
			wantDisconnected: true,
		},
		{
			name: "Disconnect after SendMessage closes channels",
			action: func(t *testing.T, s *StreamableSession) {
				t.Helper()
				msg := makeTestRequest(t, "test", 3)
				require.NoError(t, s.SendMessage(msg))
				s.Disconnect()
			},
			wantMsgCh:        true,
			wantRespCh:       true,
			wantDisconnected: true,
		},
		{
			name: "SendMessage after Disconnect returns error",
			action: func(t *testing.T, s *StreamableSession) {
				t.Helper()
				s.Disconnect()
				msg := makeTestRequest(t, "test", 4)
				err := s.SendMessage(msg)
				assert.Error(t, err)
				assert.Equal(t, "session disconnected", err.Error())
			},
			wantMsgCh:        false,
			wantRespCh:       false,
			wantDisconnected: true,
		},
		{
			name: "SendResponse after Disconnect returns error",
			action: func(t *testing.T, s *StreamableSession) {
				t.Helper()
				s.Disconnect()
				msg := makeTestRequest(t, "test", 5)
				err := s.SendResponse(msg)
				assert.Error(t, err)
				assert.Equal(t, "session disconnected", err.Error())
			},
			wantMsgCh:        false,
			wantRespCh:       false,
			wantDisconnected: true,
		},
		{
			name: "GetData returns zeros when channels are nil",
			action: func(t *testing.T, s *StreamableSession) {
				t.Helper()
				data, ok := s.GetData().(map[string]int)
				require.True(t, ok)
				assert.Equal(t, 0, data["message_buffer"])
				assert.Equal(t, 0, data["response_buffer"])
			},
			wantMsgCh:  false,
			wantRespCh: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sess := NewStreamableSession("test-" + tt.name)
			s, ok := sess.(*StreamableSession)
			require.True(t, ok)

			tt.action(t, s)

			if tt.wantMsgCh {
				assert.NotNil(t, s.MessageCh, "MessageCh should be allocated")
			} else {
				assert.Nil(t, s.MessageCh, "MessageCh should remain nil")
			}
			if tt.wantRespCh {
				assert.NotNil(t, s.ResponseCh, "ResponseCh should be allocated")
			} else {
				assert.Nil(t, s.ResponseCh, "ResponseCh should remain nil")
			}
			assert.Equal(t, tt.wantDisconnected, s.disconnected)
		})
	}
}

// makeTestRequest creates a minimal JSON-RPC request for testing.
func makeTestRequest(t *testing.T, method string, id int) jsonrpc2.Message {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	})
	require.NoError(t, err)
	msg, err := jsonrpc2.DecodeMessage(raw)
	require.NoError(t, err)
	return msg
}
