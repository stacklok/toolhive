package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp/auth/mocks"
)

type testContextKey struct{}

var testKey = testContextKey{}

func TestDefaultOutgoingAuthenticator_RegisterStrategy(t *testing.T) {
	t.Parallel()
	t.Run("register valid strategy succeeds", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		auth := NewDefaultOutgoingAuthenticator()
		strategy := mocks.NewMockStrategy(ctrl)
		strategy.EXPECT().Name().Return("bearer").AnyTimes()

		err := auth.RegisterStrategy("bearer", strategy)

		require.NoError(t, err)
		// Verify strategy was registered
		retrieved, err := auth.GetStrategy("bearer")
		require.NoError(t, err)
		assert.Equal(t, strategy, retrieved)
	})

	t.Run("register empty name fails", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		auth := NewDefaultOutgoingAuthenticator()
		strategy := mocks.NewMockStrategy(ctrl)

		err := auth.RegisterStrategy("", strategy)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "strategy name cannot be empty")
	})

	t.Run("register nil strategy fails", func(t *testing.T) {
		t.Parallel()
		auth := NewDefaultOutgoingAuthenticator()

		err := auth.RegisterStrategy("bearer", nil)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "strategy cannot be nil")
	})

	t.Run("register duplicate name fails", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		auth := NewDefaultOutgoingAuthenticator()
		strategy1 := mocks.NewMockStrategy(ctrl)
		strategy1.EXPECT().Name().Return("bearer").AnyTimes()
		strategy2 := mocks.NewMockStrategy(ctrl)

		err := auth.RegisterStrategy("bearer", strategy1)
		require.NoError(t, err)

		err = auth.RegisterStrategy("bearer", strategy2)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already registered")
		assert.Contains(t, err.Error(), "bearer")
	})

	t.Run("register multiple different strategies succeeds", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		auth := NewDefaultOutgoingAuthenticator()
		bearer := mocks.NewMockStrategy(ctrl)
		bearer.EXPECT().Name().Return("bearer").AnyTimes()
		basic := mocks.NewMockStrategy(ctrl)
		basic.EXPECT().Name().Return("basic").AnyTimes()
		apiKey := mocks.NewMockStrategy(ctrl)
		apiKey.EXPECT().Name().Return("api-key").AnyTimes()

		require.NoError(t, auth.RegisterStrategy("bearer", bearer))
		require.NoError(t, auth.RegisterStrategy("basic", basic))
		require.NoError(t, auth.RegisterStrategy("api-key", apiKey))

		// Verify all strategies are registered
		s1, err := auth.GetStrategy("bearer")
		require.NoError(t, err)
		assert.Equal(t, bearer, s1)

		s2, err := auth.GetStrategy("basic")
		require.NoError(t, err)
		assert.Equal(t, basic, s2)

		s3, err := auth.GetStrategy("api-key")
		require.NoError(t, err)
		assert.Equal(t, apiKey, s3)
	})
}

func TestDefaultOutgoingAuthenticator_GetStrategy(t *testing.T) {
	t.Parallel()
	t.Run("get existing strategy succeeds", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		auth := NewDefaultOutgoingAuthenticator()
		strategy := mocks.NewMockStrategy(ctrl)
		strategy.EXPECT().Name().Return("bearer").AnyTimes()
		require.NoError(t, auth.RegisterStrategy("bearer", strategy))

		retrieved, err := auth.GetStrategy("bearer")

		require.NoError(t, err)
		assert.Equal(t, strategy, retrieved)
	})

	t.Run("get non-existent strategy fails", func(t *testing.T) {
		t.Parallel()
		auth := NewDefaultOutgoingAuthenticator()

		retrieved, err := auth.GetStrategy("non-existent")

		assert.Error(t, err)
		assert.Nil(t, retrieved)
		assert.Contains(t, err.Error(), "not found")
		assert.Contains(t, err.Error(), "non-existent")
	})

	t.Run("get from empty registry fails", func(t *testing.T) {
		t.Parallel()
		auth := NewDefaultOutgoingAuthenticator()

		retrieved, err := auth.GetStrategy("bearer")

		assert.Error(t, err)
		assert.Nil(t, retrieved)
	})
}

func TestDefaultOutgoingAuthenticator_AuthenticateRequest(t *testing.T) {
	t.Parallel()
	t.Run("authenticates with valid strategy", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		auth := NewDefaultOutgoingAuthenticator()
		strategy := mocks.NewMockStrategy(ctrl)
		strategy.EXPECT().Name().Return("bearer").AnyTimes()
		strategy.EXPECT().Validate(gomock.Any()).Return(nil)
		strategy.EXPECT().Authenticate(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, req *http.Request, _ map[string]any) error {
				// Add a header to verify the request was modified
				req.Header.Set("Authorization", "Bearer token123")
				return nil
			},
		)
		require.NoError(t, auth.RegisterStrategy("bearer", strategy))

		req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
		metadata := map[string]any{"token": "token123"}
		err := auth.AuthenticateRequest(context.Background(), req, "bearer", metadata)

		require.NoError(t, err)
		assert.Equal(t, "Bearer token123", req.Header.Get("Authorization"))
	})

	t.Run("fails with non-existent strategy", func(t *testing.T) {
		t.Parallel()
		auth := NewDefaultOutgoingAuthenticator()
		req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)

		err := auth.AuthenticateRequest(context.Background(), req, "non-existent", nil)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("returns error from strategy", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		auth := NewDefaultOutgoingAuthenticator()
		strategyErr := errors.New("authentication failed")
		strategy := mocks.NewMockStrategy(ctrl)
		strategy.EXPECT().Name().Return("bearer").AnyTimes()
		strategy.EXPECT().Validate(gomock.Any()).Return(nil)
		strategy.EXPECT().Authenticate(gomock.Any(), gomock.Any(), gomock.Any()).Return(strategyErr)
		require.NoError(t, auth.RegisterStrategy("bearer", strategy))

		req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
		err := auth.AuthenticateRequest(context.Background(), req, "bearer", nil)

		assert.Error(t, err)
		assert.Equal(t, strategyErr, err)
	})

	t.Run("passes context and metadata to strategy", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		auth := NewDefaultOutgoingAuthenticator()

		var receivedCtx context.Context
		var receivedMetadata map[string]any

		strategy := mocks.NewMockStrategy(ctrl)
		strategy.EXPECT().Name().Return("bearer").AnyTimes()
		strategy.EXPECT().Validate(gomock.Any()).Return(nil)
		strategy.EXPECT().Authenticate(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, _ *http.Request, metadata map[string]any) error {
				receivedCtx = ctx
				receivedMetadata = metadata
				return nil
			},
		)
		require.NoError(t, auth.RegisterStrategy("bearer", strategy))

		ctx := context.WithValue(context.Background(), testKey, "test-value")
		req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
		metadata := map[string]any{
			"token":  "abc123",
			"scopes": []string{"read", "write"},
		}

		err := auth.AuthenticateRequest(ctx, req, "bearer", metadata)

		require.NoError(t, err)
		assert.NotNil(t, receivedCtx)
		assert.Equal(t, "test-value", receivedCtx.Value(testKey))
		assert.Equal(t, metadata, receivedMetadata)
	})

	t.Run("validates metadata before authentication", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		auth := NewDefaultOutgoingAuthenticator()
		strategy := mocks.NewMockStrategy(ctrl)
		strategy.EXPECT().Name().Return("test-strategy").AnyTimes()

		require.NoError(t, auth.RegisterStrategy("test-strategy", strategy))

		req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
		metadata := map[string]any{"invalid": "data"}

		// Expect Validate to be called and return error
		strategy.EXPECT().
			Validate(metadata).
			Return(errors.New("invalid metadata"))

		// Authenticate should NOT be called if validation fails
		// (no EXPECT for Authenticate)

		err := auth.AuthenticateRequest(context.Background(), req, "test-strategy", metadata)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid metadata for strategy")
		assert.Contains(t, err.Error(), "test-strategy")
	})
}

func TestDefaultOutgoingAuthenticator_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	t.Run("concurrent GetStrategy calls are thread-safe", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		auth := NewDefaultOutgoingAuthenticator()

		// Register multiple strategies
		strategies := []string{"bearer", "basic", "api-key", "oauth2", "jwt"}
		for _, name := range strategies {
			strategy := mocks.NewMockStrategy(ctrl)
			strategy.EXPECT().Name().Return(name).AnyTimes()
			require.NoError(t, auth.RegisterStrategy(name, strategy))
		}

		// Test concurrent reads with -race detector
		const numGoroutines = 100
		const numOperations = 1000

		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		errs := make(chan error, numGoroutines*numOperations)

		for i := 0; i < numGoroutines; i++ {
			go func(_ int) {
				defer wg.Done()
				for j := 0; j < numOperations; j++ {
					// Rotate through strategies
					strategyName := strategies[j%len(strategies)]
					strategy, err := auth.GetStrategy(strategyName)
					if err != nil {
						errs <- err
						return
					}
					if strategy.Name() != strategyName {
						errs <- errors.New("strategy name mismatch")
						return
					}
				}
			}(i)
		}

		wg.Wait()
		close(errs)

		// Check for errors
		var collectedErrors []error
		for err := range errs {
			collectedErrors = append(collectedErrors, err)
		}

		if len(collectedErrors) > 0 {
			t.Fatalf("concurrent access produced errors: %v", collectedErrors)
		}
	})

	t.Run("concurrent AuthenticateRequest calls are thread-safe", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		auth := NewDefaultOutgoingAuthenticator()

		// Counter to verify all authentications happen
		var authCount int64
		var authMu sync.Mutex

		strategy := mocks.NewMockStrategy(ctrl)
		strategy.EXPECT().Name().Return("bearer").AnyTimes()
		strategy.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
		strategy.EXPECT().Authenticate(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, req *http.Request, _ map[string]any) error {
				authMu.Lock()
				authCount++
				authMu.Unlock()
				req.Header.Set("Authorization", "Bearer test")
				return nil
			},
		).AnyTimes()
		require.NoError(t, auth.RegisterStrategy("bearer", strategy))

		const numGoroutines = 100

		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		errs := make(chan error, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func() {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
				err := auth.AuthenticateRequest(context.Background(), req, "bearer", nil)
				if err != nil {
					errs <- err
				}
			}()
		}

		wg.Wait()
		close(errs)

		// Check for errors
		var collectedErrors []error
		for err := range errs {
			collectedErrors = append(collectedErrors, err)
		}

		if len(collectedErrors) > 0 {
			t.Fatalf("concurrent AuthenticateRequest produced errors: %v", collectedErrors)
		}

		// Verify all authentications completed
		assert.Equal(t, int64(numGoroutines), authCount)
	})

	t.Run("concurrent RegisterStrategy and GetStrategy are thread-safe", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		auth := NewDefaultOutgoingAuthenticator()

		const numRegister = 50
		const numGet = 50

		var wg sync.WaitGroup
		wg.Add(numRegister + numGet)

		errs := make(chan error, numRegister+numGet)

		// Goroutines registering strategies
		for i := 0; i < numRegister; i++ {
			go func(id int) {
				defer wg.Done()
				strategy := mocks.NewMockStrategy(ctrl)
				strategy.EXPECT().Name().Return("strategy").AnyTimes()
				strategyName := "strategy-" + string(rune('A'+id%26)) + string(rune('0'+id/26))
				err := auth.RegisterStrategy(strategyName, strategy)
				if err != nil {
					errs <- err
				}
			}(i)
		}

		// Goroutines reading strategies (will mostly fail, but shouldn't race)
		for i := 0; i < numGet; i++ {
			go func(id int) {
				defer wg.Done()
				strategyName := "strategy-" + string(rune('A'+id%26)) + string(rune('0'+id/26))
				// GetStrategy may return error if not registered yet, that's OK
				_, _ = auth.GetStrategy(strategyName)
			}(i)
		}

		wg.Wait()
		close(errs)

		// Check for unexpected errors (registration errors are not expected)
		var collectedErrors []error
		for err := range errs {
			collectedErrors = append(collectedErrors, err)
		}

		if len(collectedErrors) > 0 {
			t.Fatalf("concurrent RegisterStrategy/GetStrategy produced errors: %v", collectedErrors)
		}
	})
}
