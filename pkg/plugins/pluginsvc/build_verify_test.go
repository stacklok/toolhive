// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/container/signer"
	"github.com/stacklok/toolhive-core/httperr"
	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	ocimocks "github.com/stacklok/toolhive-core/oci/plugins/mocks"
	"github.com/stacklok/toolhive/pkg/plugins"
	// The mock is generated from toolhive-core's signer.Signer, which both
	// services sign through — reused rather than regenerated per package,
	// matching how pluginsvc already reuses skills/verifier/mocks.
	signermocks "github.com/stacklok/toolhive/pkg/skills/skillsvc/mocks"
)

// newPushFixture builds an OCI store with a manifest tagged "my-tag" and a
// registry mock, mirroring TestPush's setup.
func newPushFixture(t *testing.T) (*ocimocks.MockRegistryClient, *ociplugins.Store, string) {
	t.Helper()
	ctrl := gomock.NewController(t)
	ociStore, err := ociplugins.NewStore(t.TempDir())
	require.NoError(t, err)
	d := putTestManifest(t, ociStore)
	require.NoError(t, ociStore.Tag(t.Context(), d, "my-tag"))
	return ocimocks.NewMockRegistryClient(ctrl), ociStore, d.String()
}

// TestPushValidatesSigningInputs guards the RFC invariant that pushes are
// signed by default: exactly one of a key, an identity token, or an explicit
// no_sign must be given, before anything is pushed.
func TestPushValidatesSigningInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts plugins.PushOptions
	}{
		{name: "neither key, identity_token, nor no_sign", opts: plugins.PushOptions{}},
		{
			name: "both key and identity_token",
			opts: plugins.PushOptions{Key: "/tmp/cosign.key", IdentityToken: "tok"},
		},
		{
			name: "no_sign combined with key",
			opts: plugins.PushOptions{NoSign: true, Key: "/tmp/cosign.key"},
		},
		{
			name: "no_sign combined with identity_token",
			opts: plugins.PushOptions{NoSign: true, IdentityToken: "tok"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, ociStore, _ := newPushFixture(t)
			svc := New(WithRegistryClient(reg), WithOCIStore(ociStore))

			tc.opts.Reference = "my-tag"
			err := svc.Push(t.Context(), tc.opts)
			require.Error(t, err)
			assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
		})
	}
}

// TestPushSignsAfterPushing proves the pushed artifact is signed with the
// provided key, pinned to the digest that was pushed.
func TestPushSignsAfterPushing(t *testing.T) {
	t.Parallel()
	reg, ociStore, digest := newPushFixture(t)

	ms := signermocks.NewMockSigner(gomock.NewController(t))
	ms.EXPECT().SignOCI(gomock.Any(), "my-tag", digest, signer.Options{Key: "/tmp/cosign.key"}).
		Return(&signer.Result{Bundle: []byte(`{"bundle":true}`)}, nil)
	reg.EXPECT().Push(gomock.Any(), gomock.Any(), gomock.Any(), "my-tag").Return(nil)

	svc := New(WithRegistryClient(reg), WithOCIStore(ociStore), WithSigner(ms))
	err := svc.Push(t.Context(), plugins.PushOptions{Reference: "my-tag", Key: "/tmp/cosign.key"})
	require.NoError(t, err)
}

// TestPushSignsKeylessWithIdentityToken proves an identity token pushes
// through keyless signing instead of a key, and that the Fulcio/Rekor URL
// env overrides reach core's signer.Options — the E2E/staging escape hatch.
// Not run in parallel: t.Setenv forbids it.
func TestPushSignsKeylessWithIdentityToken(t *testing.T) {
	reg, ociStore, digest := newPushFixture(t)
	t.Setenv(envFulcioURL, "https://fulcio.example.test")
	t.Setenv(envRekorURL, "https://rekor.example.test")

	ms := signermocks.NewMockSigner(gomock.NewController(t))
	ms.EXPECT().SignOCI(gomock.Any(), "my-tag", digest, signer.Options{
		IdentityToken: "a.b.c",
		FulcioURL:     "https://fulcio.example.test",
		RekorURL:      "https://rekor.example.test",
	}).Return(&signer.Result{Bundle: []byte(`{"bundle":true}`)}, nil)
	reg.EXPECT().Push(gomock.Any(), gomock.Any(), gomock.Any(), "my-tag").Return(nil)

	svc := New(WithRegistryClient(reg), WithOCIStore(ociStore), WithSigner(ms))
	err := svc.Push(t.Context(), plugins.PushOptions{Reference: "my-tag", IdentityToken: "a.b.c"})
	require.NoError(t, err)
}

// TestPushSigningFailurePropagates: a failed signing is a failed push — the
// artifact must not be silently published unsigned.
func TestPushSigningFailurePropagates(t *testing.T) {
	t.Parallel()
	reg, ociStore, _ := newPushFixture(t)

	ms := signermocks.NewMockSigner(gomock.NewController(t))
	ms.EXPECT().SignOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, signer.ErrKeyRequired)
	reg.EXPECT().Push(gomock.Any(), gomock.Any(), gomock.Any(), "my-tag").Return(nil)

	svc := New(WithRegistryClient(reg), WithOCIStore(ociStore), WithSigner(ms))
	err := svc.Push(t.Context(), plugins.PushOptions{Reference: "my-tag", Key: "/bad/key"})
	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
}

// TestPushNoSignSkipsSigner: the explicit opt-out must reach the registry
// without ever calling the signer (the mock has no expectations).
func TestPushNoSignSkipsSigner(t *testing.T) {
	t.Parallel()
	reg, ociStore, _ := newPushFixture(t)
	reg.EXPECT().Push(gomock.Any(), gomock.Any(), gomock.Any(), "my-tag").Return(nil)

	ms := signermocks.NewMockSigner(gomock.NewController(t)) // no expectations
	svc := New(WithRegistryClient(reg), WithOCIStore(ociStore), WithSigner(ms))
	require.NoError(t, svc.Push(t.Context(), plugins.PushOptions{Reference: "my-tag", NoSign: true}))
}
