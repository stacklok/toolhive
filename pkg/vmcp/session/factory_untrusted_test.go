// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/session/binding"
	internalbk "github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
	"github.com/stacklok/toolhive/pkg/vmcp/session/untrusted"
)

// captureResolver records the SessionRef it was invoked with and rewrites
// nothing (passes backends through). newPodFor, when non-empty, fires the
// onNewPod callback for that backend ID (simulating a fresh pod create).
type captureResolver struct {
	called    int
	sessRefs  []untrusted.SessionRef
	newPodFor string
}

func (r *captureResolver) ResolveTargets(
	_ context.Context, sess untrusted.SessionRef, backends []*vmcp.Backend, onNewPod func(string),
) []*vmcp.Backend {
	r.called++
	r.sessRefs = append(r.sessRefs, sess)
	if r.newPodFor != "" && onNewPod != nil {
		onNewPod(r.newPodFor)
	}
	return backends
}

//nolint:unparam // connector signature fixed by backendConnector
func passThroughConnector(
	_ context.Context, _ *vmcp.BackendTarget, _ *auth.Identity, _ string,
) (internalbk.Session, *vmcp.CapabilityList, error) {
	return &mockConnectedBackend{sessID: "bs-1"}, &vmcp.CapabilityList{}, nil
}

// identityWithIssSub builds an *auth.Identity carrying the (iss, sub) claims
// the untrusted resolver reads. (identityWithClaims already exists in this
// package's test files with a different shape.)
func identityWithIssSub(iss, sub string) *auth.Identity {
	return &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"iss": iss, "sub": sub}},
		Token:         "bearer-token",
	}
}

//nolint:unparam // id kept as a parameter for readability; callers consistently use "b1"
func testBackend(id string) *vmcp.Backend {
	return &vmcp.Backend{
		ID:            id,
		Name:          id,
		BaseURL:       "http://svc:8080/mcp",
		TransportType: "streamable-http",
		Metadata:      map[string]string{},
	}
}

func TestFactoryResolverSeam(t *testing.T) {
	t.Parallel()

	t.Run("nil resolver: trusted path unchanged", func(t *testing.T) {
		t.Parallel()
		factory := newSessionFactoryWithConnector(passThroughConnector)
		sess, err := factory.MakeSessionWithID(context.Background(), "sess-1",
			identityWithIssSub("iss", "sub"), []*vmcp.Backend{testBackend("b1")})
		require.NoError(t, err)
		require.NoError(t, sess.Close())
	})

	t.Run("create path: resolver invoked with identity (iss, sub)", func(t *testing.T) {
		t.Parallel()
		resolver := &captureResolver{}
		factory := newSessionFactoryWithConnector(passThroughConnector, WithUntrustedResolver(resolver))

		sess, err := factory.MakeSessionWithID(context.Background(), "sess-1",
			identityWithIssSub("https://issuer", "user-1"), []*vmcp.Backend{testBackend("b1")})
		require.NoError(t, err)
		require.NoError(t, sess.Close())
		require.Equal(t, 1, resolver.called)
		ref := resolver.sessRefs[0]
		assert.Equal(t, "sess-1", ref.SessionID)
		assert.Equal(t, "https://issuer", ref.Issuer)
		assert.Equal(t, "user-1", ref.Subject)
	})

	t.Run("restore path: resolver invoked with parsed binding (iss, sub), not nil identity", func(t *testing.T) {
		t.Parallel()
		resolver := &captureResolver{}
		factory := newSessionFactoryWithConnector(passThroughConnector, WithUntrustedResolver(resolver))

		storedBinding, err := binding.Format("https://issuer", "user-1")
		require.NoError(t, err)
		metadata := map[string]string{
			MetadataKeyBackendIDs:                   "b1",
			sessiontypes.MetadataKeyIdentityBinding: storedBinding,
		}
		sess, err := factory.RestoreSession(context.Background(), "sess-restored", metadata, []*vmcp.Backend{testBackend("b1")})
		require.NoError(t, err)
		require.NoError(t, sess.Close())
		require.Equal(t, 1, resolver.called)
		ref := resolver.sessRefs[0]
		assert.Equal(t, "sess-restored", ref.SessionID)
		assert.Equal(t, "https://issuer", ref.Issuer)
		assert.Equal(t, "user-1", ref.Subject)
	})

	t.Run("restore with stale hint + fresh pod: hint is not sent to the connector", func(t *testing.T) {
		t.Parallel()
		resolver := &captureResolver{newPodFor: "b1"}

		hints := make(chan string, 1)
		connector := func(
			_ context.Context, _ *vmcp.BackendTarget, _ *auth.Identity, hint string,
		) (internalbk.Session, *vmcp.CapabilityList, error) {
			hints <- hint
			return &mockConnectedBackend{sessID: "bs-new"}, &vmcp.CapabilityList{}, nil
		}
		factory := newSessionFactoryWithConnector(connector, WithUntrustedResolver(resolver))

		storedBinding, err := binding.Format("https://issuer", "user-1")
		require.NoError(t, err)
		metadata := map[string]string{
			MetadataKeyBackendIDs:                   "b1",
			MetadataKeyBackendSessionPrefix + "b1":  "stale-hint-from-deleted-pod",
			sessiontypes.MetadataKeyIdentityBinding: storedBinding,
		}
		sess, err := factory.RestoreSession(context.Background(), "sess-restored", metadata, []*vmcp.Backend{testBackend("b1")})
		require.NoError(t, err)
		require.NoError(t, sess.Close())

		select {
		case hint := <-hints:
			assert.Empty(t, hint, "a stale hint must never be sent to a freshly created pod")
		default:
			t.Fatal("connector was not invoked")
		}
	})

	t.Run("restore with hint + adopted pod: hint is preserved and sent", func(t *testing.T) {
		t.Parallel()
		resolver := &captureResolver{} // no newPodFor: the pod was adopted

		hints := make(chan string, 1)
		connector := func(
			_ context.Context, _ *vmcp.BackendTarget, _ *auth.Identity, hint string,
		) (internalbk.Session, *vmcp.CapabilityList, error) {
			hints <- hint
			return &mockConnectedBackend{sessID: "bs-1"}, &vmcp.CapabilityList{}, nil
		}
		factory := newSessionFactoryWithConnector(connector, WithUntrustedResolver(resolver))

		storedBinding, err := binding.Format("https://issuer", "user-1")
		require.NoError(t, err)
		metadata := map[string]string{
			MetadataKeyBackendIDs:                   "b1",
			MetadataKeyBackendSessionPrefix + "b1":  "live-hint",
			sessiontypes.MetadataKeyIdentityBinding: storedBinding,
		}
		sess, err := factory.RestoreSession(context.Background(), "sess-restored", metadata, []*vmcp.Backend{testBackend("b1")})
		require.NoError(t, err)
		require.NoError(t, sess.Close())

		select {
		case hint := <-hints:
			assert.Equal(t, "live-hint", hint, "an adopted pod's hint must be sent so the backend resumes")
		default:
			t.Fatal("connector was not invoked")
		}
	})

	t.Run("restore path: anonymous session yields empty (iss, sub)", func(t *testing.T) {
		t.Parallel()
		resolver := &captureResolver{}
		factory := newSessionFactoryWithConnector(passThroughConnector, WithUntrustedResolver(resolver))

		metadata := map[string]string{
			MetadataKeyBackendIDs:                   "b1",
			sessiontypes.MetadataKeyIdentityBinding: binding.UnauthenticatedSentinel,
		}
		sess, err := factory.RestoreSession(context.Background(), "sess-anon", metadata, []*vmcp.Backend{testBackend("b1")})
		require.NoError(t, err)
		require.NoError(t, sess.Close())
		require.Equal(t, 1, resolver.called)
		ref := resolver.sessRefs[0]
		assert.Empty(t, ref.Issuer)
		assert.Empty(t, ref.Subject)
	})
}
