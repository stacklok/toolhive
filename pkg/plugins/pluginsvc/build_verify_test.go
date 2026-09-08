// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/opencontainers/go-digest"
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

// testPushRef is the reference push fixtures tag locally and publish under.
// A full registry reference rather than a bare tag because a signed push
// derives the digest-pinned staging reference from it, and a bare name has no
// registry or repository to pin against. Real pushes always carry one: core's
// registry client parses the reference the same way.
const testPushRef = "ghcr.io/example/plugin:v1"

// newPushFixture builds an OCI store with a manifest tagged testPushRef and a
// registry mock, mirroring TestPush's setup. Returns the artifact digest and
// the digest-pinned staging reference a signed push publishes under first.
func newPushFixture(t *testing.T) (*ocimocks.MockRegistryClient, *ociplugins.Store, string, string) {
	t.Helper()
	ctrl := gomock.NewController(t)
	ociStore, err := ociplugins.NewStore(t.TempDir())
	require.NoError(t, err)
	d := putTestManifest(t, ociStore)
	require.NoError(t, ociStore.Tag(t.Context(), d, testPushRef))
	staged, err := stagingReference(testPushRef, d)
	require.NoError(t, err)
	return ocimocks.NewMockRegistryClient(ctrl), ociStore, d.String(), staged
}

// TestPushValidatesSigningInputs guards the RFC invariant that pushes are
// signed by default: an identity token or an explicit no_sign must be given,
// before anything is pushed.
func TestPushValidatesSigningInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts plugins.PushOptions
	}{
		{name: "neither identity_token nor no_sign", opts: plugins.PushOptions{}},
		{
			name: "no_sign combined with identity_token",
			opts: plugins.PushOptions{NoSign: true, IdentityToken: "tok"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, ociStore, _, _ := newPushFixture(t)
			svc := New(WithRegistryClient(reg), WithOCIStore(ociStore))

			tc.opts.Reference = testPushRef
			err := svc.Push(t.Context(), tc.opts)
			require.Error(t, err)
			assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
		})
	}
}

// TestPushOptionsCarriesNoKeyField pins the keyless-only contract at the type
// level. Plugin signing is keyless until install-time key verification exists
// (#6442), and a settable Key had no single answer for what it meant: the
// in-process service rejected it while the HTTP client dropped it and
// published unsigned. Omitting the field is what makes those two agree, so
// the absence is the invariant worth pinning — a reintroduced Key (for
// instance by restoring the skills.PushOptions alias) fails here rather than
// silently reopening the divergence.
func TestPushOptionsCarriesNoKeyField(t *testing.T) {
	t.Parallel()
	_, ok := reflect.TypeOf(plugins.PushOptions{}).FieldByName("Key")
	assert.False(t, ok,
		"plugins.PushOptions must not carry a Key field: plugin signing is keyless-only (#6442)")
}

// TestPushSignsKeylessWithIdentityToken proves an identity token signs the
// pushed artifact pinned to the digest that was pushed, and that the
// Fulcio/Rekor URL env overrides reach core's signer.Options — the
// E2E/staging escape hatch.
// Not run in parallel: t.Setenv forbids it.
func TestPushSignsKeylessWithIdentityToken(t *testing.T) {
	reg, ociStore, artifactDigest, staged := newPushFixture(t)
	t.Setenv(envFulcioURL, "https://fulcio.example.test")
	t.Setenv(envRekorURL, "https://rekor.example.test")

	ms := signermocks.NewMockSigner(gomock.NewController(t))
	// The full published sequence, in order: stage under the digest, sign that
	// digest reference, then promote the requested tag. gomock.InOrder is what
	// pins the ordering — the tag must not exist before the signature does.
	gomock.InOrder(
		reg.EXPECT().Push(gomock.Any(), gomock.Any(), gomock.Any(), staged).Return(nil),
		ms.EXPECT().SignOCI(gomock.Any(), staged, artifactDigest, signer.Options{
			IdentityToken: "a.b.c",
			FulcioURL:     "https://fulcio.example.test",
			RekorURL:      "https://rekor.example.test",
		}).Return(&signer.Result{Bundle: []byte(`{"bundle":true}`)}, nil),
		reg.EXPECT().Push(gomock.Any(), gomock.Any(), gomock.Any(), testPushRef).Return(nil),
	)

	svc := New(WithRegistryClient(reg), WithOCIStore(ociStore), WithSigner(ms))
	err := svc.Push(t.Context(), plugins.PushOptions{Reference: testPushRef, IdentityToken: "a.b.c"})
	require.NoError(t, err)
}

// TestPushSigningFailureLeavesTagUnpublished: a failed signing is a failed
// push, and — the part worth pinning — the requested tag is never published.
// The registry mock allows only the staging push, so a promotion call after
// the signing failure fails the test. Signing before promoting is the only
// thing that keeps a signing failure from leaving live unsigned content at
// the reference consumers resolve.
func TestPushSigningFailureLeavesTagUnpublished(t *testing.T) {
	t.Parallel()
	reg, ociStore, _, staged := newPushFixture(t)

	ms := signermocks.NewMockSigner(gomock.NewController(t))
	ms.EXPECT().SignOCI(gomock.Any(), staged, gomock.Any(), gomock.Any()).
		Return(nil, signer.ErrKeyRequired)
	reg.EXPECT().Push(gomock.Any(), gomock.Any(), gomock.Any(), staged).Return(nil)

	svc := New(WithRegistryClient(reg), WithOCIStore(ociStore), WithSigner(ms))
	err := svc.Push(t.Context(), plugins.PushOptions{Reference: testPushRef, IdentityToken: "a.b.c"})
	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
}

// TestPushSignedDigestPinnedReferenceIsRejected: staging and promotion are the
// same operation for a digest-pinned destination, so the staged-then-promoted
// ordering cannot keep the requested reference out of the unsigned window —
// there is no push order that upholds the guarantee. The push is refused
// instead, and the assertion that matters is that it is refused *before*
// anything reaches the registry: neither mock carries an expectation, so any
// push or signing attempt fails the test. A rejection that still published
// would leave exactly the unsigned artifact this is meant to prevent.
func TestPushSignedDigestPinnedReferenceIsRejected(t *testing.T) {
	t.Parallel()
	reg, ociStore, artifactDigest, staged := newPushFixture(t)
	// The reference is resolved in the local store before anything is pushed,
	// so a digest-pinned request has to be tagged that way locally too —
	// otherwise the request would fail as not-found and prove nothing.
	require.NoError(t, ociStore.Tag(t.Context(), digest.Digest(artifactDigest), staged))

	ms := signermocks.NewMockSigner(gomock.NewController(t)) // no expectations
	svc := New(WithRegistryClient(reg), WithOCIStore(ociStore), WithSigner(ms))

	err := svc.Push(t.Context(), plugins.PushOptions{Reference: staged, IdentityToken: "a.b.c"})
	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	assert.Contains(t, err.Error(), "digest-pinned")
}

// TestPushNoSignDigestPinnedReferenceIsAllowed: the rejection is a property of
// the signing guarantee, not of the reference shape. An unsigned push promises
// nothing that publishing unsigned content would break, so it still publishes
// the requested digest reference directly.
func TestPushNoSignDigestPinnedReferenceIsAllowed(t *testing.T) {
	t.Parallel()
	reg, ociStore, artifactDigest, staged := newPushFixture(t)
	require.NoError(t, ociStore.Tag(t.Context(), digest.Digest(artifactDigest), staged))
	reg.EXPECT().Push(gomock.Any(), gomock.Any(), gomock.Any(), staged).Return(nil).Times(1)

	ms := signermocks.NewMockSigner(gomock.NewController(t)) // no expectations
	svc := New(WithRegistryClient(reg), WithOCIStore(ociStore), WithSigner(ms))
	require.NoError(t, svc.Push(t.Context(),
		plugins.PushOptions{Reference: staged, NoSign: true}))
}

// TestPushNoSignSkipsSigner: the explicit opt-out must reach the registry
// without ever calling the signer (the mock has no expectations).
func TestPushNoSignSkipsSigner(t *testing.T) {
	t.Parallel()
	reg, ociStore, _, _ := newPushFixture(t)
	// An unsigned push publishes the requested reference directly: with no
	// signature to order against, there is nothing to stage or promote.
	reg.EXPECT().Push(gomock.Any(), gomock.Any(), gomock.Any(), testPushRef).Return(nil).Times(1)

	ms := signermocks.NewMockSigner(gomock.NewController(t)) // no expectations
	svc := New(WithRegistryClient(reg), WithOCIStore(ociStore), WithSigner(ms))
	require.NoError(t, svc.Push(t.Context(), plugins.PushOptions{Reference: testPushRef, NoSign: true}))
}
