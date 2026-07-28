// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// fakeSDKSamplingRequester captures the translated mcp-go request and returns a
// canned response/error, letting us assert the adapter's domain <-> mcp-go mapping.
type fakeSDKSamplingRequester struct {
	gotRequest mcp.CreateMessageRequest
	result     *mcp.CreateMessageResult
	err        error
}

func (f *fakeSDKSamplingRequester) RequestSampling(
	_ context.Context, request mcp.CreateMessageRequest,
) (*mcp.CreateMessageResult, error) {
	f.gotRequest = request
	return f.result, f.err
}

func TestSDKSamplingAdapter_RequestSampling(t *testing.T) {
	t.Parallel()

	t.Run("round-trips request and result", func(t *testing.T) {
		t.Parallel()

		fake := &fakeSDKSamplingRequester{
			result: &mcp.CreateMessageResult{
				SamplingMessage: mcp.SamplingMessage{
					Role:    mcp.Role("assistant"),
					Content: map[string]any{"type": "text", "text": "hi"},
				},
				Model:      "test-model",
				StopReason: "endTurn",
			},
		}
		adapter := &sdkSamplingAdapter{mcpServer: fake}

		req := vmcp.SamplingRequest{
			Messages: []vmcp.SamplingMessage{
				{Role: "user", Content: map[string]any{"type": "text", "text": "hello"}},
			},
			ModelPreferences: &vmcp.ModelPreferences{
				Hints:        []vmcp.ModelHint{{Name: "claude"}},
				CostPriority: 0.5,
			},
			SystemPrompt:  "be brief",
			MaxTokens:     100,
			Temperature:   0.7,
			StopSequences: []string{"STOP"},
		}

		res, err := adapter.RequestSampling(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, res)

		// Request translation: domain fields map onto mcp.CreateMessageParams.
		require.Len(t, fake.gotRequest.Messages, 1)
		assert.Equal(t, mcp.Role("user"), fake.gotRequest.Messages[0].Role)
		assert.Equal(t, "be brief", fake.gotRequest.SystemPrompt)
		assert.Equal(t, 100, fake.gotRequest.MaxTokens)
		assert.Equal(t, []string{"STOP"}, fake.gotRequest.StopSequences)
		require.NotNil(t, fake.gotRequest.ModelPreferences)
		require.Len(t, fake.gotRequest.ModelPreferences.Hints, 1)
		assert.Equal(t, "claude", fake.gotRequest.ModelPreferences.Hints[0].Name)

		// Result translation: mcp fields map back onto domain SamplingResult.
		assert.Equal(t, "assistant", res.Role)
		assert.Equal(t, "test-model", res.Model)
		assert.Equal(t, "endTurn", res.StopReason)
		assert.Equal(t, map[string]any{"type": "text", "text": "hi"}, res.Content)
	})

	t.Run("nil model preferences stays nil", func(t *testing.T) {
		t.Parallel()

		fake := &fakeSDKSamplingRequester{
			result: &mcp.CreateMessageResult{SamplingMessage: mcp.SamplingMessage{Role: mcp.Role("assistant")}},
		}
		adapter := &sdkSamplingAdapter{mcpServer: fake}

		_, err := adapter.RequestSampling(context.Background(), vmcp.SamplingRequest{MaxTokens: 10})
		require.NoError(t, err)
		assert.Nil(t, fake.gotRequest.ModelPreferences)
	})

	t.Run("SDK error is propagated", func(t *testing.T) {
		t.Parallel()

		fake := &fakeSDKSamplingRequester{err: errors.New("no active session")}
		adapter := &sdkSamplingAdapter{mcpServer: fake}

		res, err := adapter.RequestSampling(context.Background(), vmcp.SamplingRequest{MaxTokens: 10})
		require.Error(t, err)
		assert.Nil(t, res)
	})
}

func TestSDKSamplingAdapter_Integration(t *testing.T) {
	t.Parallel()

	mcpServer := server.NewMCPServer("test", "1.0.0")
	adapter := NewSDKSamplingAdapter(mcpServer)
	assert.NotNil(t, adapter)
}
