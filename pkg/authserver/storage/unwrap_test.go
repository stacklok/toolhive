// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unknownWrapper stands in for an external decorator that exposes Unwrap but
// does not implement ProviderIdentitySwapper.
type unknownWrapper struct {
	Storage
}

func (w unknownWrapper) Unwrap() Storage { return w.Storage }

func TestProviderIdentitySwapperFor_PeelsOnlyKnownDecorators(t *testing.T) {
	t.Parallel()

	newBase := func(t *testing.T) *MemoryStorage {
		t.Helper()
		base := NewMemoryStorage()
		t.Cleanup(func() { require.NoError(t, base.Close()) })
		return base
	}
	admit := func(t *testing.T, inner Storage) Storage {
		t.Helper()
		s, err := NewClientAdmissionStorage(inner, nil)
		require.NoError(t, err)
		return s
	}
	cimd := func(t *testing.T, inner Storage) Storage {
		t.Helper()
		s, err := NewCIMDStorageDecorator(inner, CIMDDecoratorConfig{Enabled: true, CacheMaxSize: 4})
		require.NoError(t, err)
		return s
	}
	spiffe := func(t *testing.T, inner Storage) Storage {
		t.Helper()
		s, err := NewSPIFFEStorageDecorator(t.Context(), inner,
			map[string]fosite.Client{"spiffe-client": &fosite.DefaultClient{ID: "spiffe-client"}})
		require.NoError(t, err)
		return s
	}

	tests := []struct {
		name       string
		wrap       func(t *testing.T, base *MemoryStorage) Storage
		wantSwapper bool
	}{
		{"memory store itself", func(_ *testing.T, base *MemoryStorage) Storage { return base }, true},
		{"user-keyed upstream storage", func(t *testing.T, base *MemoryStorage) Storage {
			s, err := NewUserKeyedUpstreamStorage(base)
			require.NoError(t, err)
			return s
		}, true},
		{"client admission storage", func(t *testing.T, base *MemoryStorage) Storage { return admit(t, base) }, true},
		{"CIMD storage decorator", func(t *testing.T, base *MemoryStorage) Storage { return cimd(t, base) }, true},
		{"CIMD shape guard", func(_ *testing.T, base *MemoryStorage) Storage { return NewCIMDShapeGuardStorage(base) }, true},
		{"SPIFFE storage decorator", func(t *testing.T, base *MemoryStorage) Storage { return spiffe(t, base) }, true},
		{"production stack with the shape guard", func(t *testing.T, base *MemoryStorage) Storage {
			return spiffe(t, NewCIMDShapeGuardStorage(admit(t, base)))
		}, true},
		{"production stack with CIMD", func(t *testing.T, base *MemoryStorage) Storage {
			return spiffe(t, cimd(t, admit(t, base)))
		}, true},
		{"unknown wrapper that exposes Unwrap", func(_ *testing.T, base *MemoryStorage) Storage {
			return unknownWrapper{Storage: base}
		}, false},
		{"unknown wrapper beneath a known decorator", func(t *testing.T, base *MemoryStorage) Storage {
			return admit(t, unknownWrapper{Storage: base})
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := newBase(t)
			got := ProviderIdentitySwapperFor(tt.wrap(t, base))
			if !tt.wantSwapper {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Same(t, base, got, "the swapper must be the memory store's own")
		})
	}
}
