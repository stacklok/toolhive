// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	ocimocks "github.com/stacklok/toolhive-core/oci/skills/mocks"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/signer"
	signermocks "github.com/stacklok/toolhive/pkg/skills/signer/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
)

// newPushFixture builds an OCI store with a manifest tagged "my-tag" and a
// registry mock, mirroring TestPush's setup.
func newPushFixture(t *testing.T) (*ocimocks.MockRegistryClient, *ociskills.Store, string) {
	t.Helper()
	ctrl := gomock.NewController(t)
	ociStore, err := ociskills.NewStore(t.TempDir())
	require.NoError(t, err)
	d, err := ociStore.PutManifest(t.Context(), []byte(`{"schemaVersion":2}`))
	require.NoError(t, err)
	require.NoError(t, ociStore.Tag(t.Context(), d, "my-tag"))
	return ocimocks.NewMockRegistryClient(ctrl), ociStore, d.String()
}

// TestPushRequiresExplicitSigningDecision guards the RFC invariant that
// pushes are signed by default: no key and no explicit no_sign is a 400,
// before anything is pushed.
func TestPushRequiresExplicitSigningDecision(t *testing.T) {
	t.Parallel()
	reg, ociStore, _ := newPushFixture(t)
	svc := New(&storage.NoopSkillStore{}, WithRegistryClient(reg), WithOCIStore(ociStore))

	err := svc.Push(t.Context(), skills.PushOptions{Reference: "my-tag"})
	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
}

// TestPushSignsAfterPushing proves the pushed artifact is signed with the
// provided key, pinned to the digest that was pushed.
func TestPushSignsAfterPushing(t *testing.T) {
	t.Parallel()
	reg, ociStore, digest := newPushFixture(t)

	ms := signermocks.NewMockSigner(gomock.NewController(t))
	ms.EXPECT().SignOCI(gomock.Any(), "my-tag", digest, signer.Options{Key: "/tmp/cosign.key"}).
		Return([]byte(`{"bundle":true}`), nil)
	reg.EXPECT().Push(gomock.Any(), gomock.Any(), gomock.Any(), "my-tag").Return(nil)

	svc := New(&storage.NoopSkillStore{},
		WithRegistryClient(reg), WithOCIStore(ociStore), WithSigner(ms))
	err := svc.Push(t.Context(), skills.PushOptions{Reference: "my-tag", Key: "/tmp/cosign.key"})
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

	svc := New(&storage.NoopSkillStore{},
		WithRegistryClient(reg), WithOCIStore(ociStore), WithSigner(ms))
	err := svc.Push(t.Context(), skills.PushOptions{Reference: "my-tag", Key: "/bad/key"})
	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
}
