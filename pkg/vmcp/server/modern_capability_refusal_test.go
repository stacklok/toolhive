// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestModernClientDeclaredCapability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		meta       map[string]any
		capability string
		want       bool
	}{
		{
			name:       "nil meta",
			meta:       nil,
			capability: capabilityElicitation,
			want:       false,
		},
		{
			name:       "no clientCapabilities key",
			meta:       map[string]any{"other": "x"},
			capability: capabilityElicitation,
			want:       false,
		},
		{
			name:       "clientCapabilities not an object",
			meta:       map[string]any{modernClientCapabilitiesKey: "elicitation"},
			capability: capabilityElicitation,
			want:       false,
		},
		{
			name:       "empty declaration",
			meta:       map[string]any{modernClientCapabilitiesKey: map[string]any{}},
			capability: capabilityElicitation,
			want:       false,
		},
		{
			name: "declared with empty options object",
			meta: map[string]any{modernClientCapabilitiesKey: map[string]any{
				capabilityElicitation: map[string]any{},
			}},
			capability: capabilityElicitation,
			want:       true,
		},
		{
			name: "different capability declared",
			meta: map[string]any{modernClientCapabilitiesKey: map[string]any{
				capabilitySampling: map[string]any{},
			}},
			capability: capabilityElicitation,
			want:       false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, modernClientDeclaredCapability(tc.meta, tc.capability))
		})
	}
}

func TestCapabilityRefusalRecorder(t *testing.T) {
	t.Parallel()

	t.Run("no recorder in context is a no-op", func(t *testing.T) {
		t.Parallel()
		// Must not panic; there is simply nothing to observe.
		recordCapabilityRefusal(context.Background(), capabilityElicitation)
	})

	t.Run("records and reads back", func(t *testing.T) {
		t.Parallel()
		ctx, rec := withCapabilityRefusalRecorder(context.Background())
		assert.Empty(t, rec.refused())
		recordCapabilityRefusal(ctx, capabilitySampling)
		assert.Equal(t, capabilitySampling, rec.refused())
	})

	t.Run("first recorded capability wins", func(t *testing.T) {
		t.Parallel()
		ctx, rec := withCapabilityRefusalRecorder(context.Background())
		recordCapabilityRefusal(ctx, capabilityElicitation)
		recordCapabilityRefusal(ctx, capabilitySampling)
		assert.Equal(t, capabilityElicitation, rec.refused())
	})
}

// TestSDKAdapters_RecordCapabilityRefusal verifies both SDK requester adapters
// record the refusal into a context recorder exactly when the mcpcompat SDK
// refuses for lack of a session (ErrNoActiveSession) — and never for any other
// error, which would misclassify e.g. a client decline as a capability gap.
func TestSDKAdapters_RecordCapabilityRefusal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		sdkErr       error
		wantRecorded bool // per-adapter capability name asserted below
	}{
		{
			name:         "no active session records the capability",
			sdkErr:       server.ErrNoActiveSession,
			wantRecorded: true,
		},
		{
			name:         "wrapped no active session records too",
			sdkErr:       errors.Join(errors.New("outer"), server.ErrNoActiveSession),
			wantRecorded: true,
		},
		{
			name:         "other errors do not record",
			sdkErr:       errors.New("client declined"),
			wantRecorded: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			elicit := &sdkElicitationAdapter{mcpServer: &fakeSDKElicitationRequester{err: tc.sdkErr}}
			ctx, rec := withCapabilityRefusalRecorder(context.Background())
			_, err := elicit.RequestElicitation(ctx, vmcp.ElicitationRequest{Message: "m"})
			require.Error(t, err, "the SDK error must still propagate unchanged")
			if tc.wantRecorded {
				assert.Equal(t, capabilityElicitation, rec.refused())
			} else {
				assert.Empty(t, rec.refused())
			}

			sample := &sdkSamplingAdapter{mcpServer: &fakeSDKSamplingRequester{err: tc.sdkErr}}
			ctx, rec = withCapabilityRefusalRecorder(context.Background())
			_, err = sample.RequestSampling(ctx, vmcp.SamplingRequest{})
			require.Error(t, err, "the SDK error must still propagate unchanged")
			if tc.wantRecorded {
				assert.Equal(t, capabilitySampling, rec.refused())
			} else {
				assert.Empty(t, rec.refused())
			}
		})
	}
}
