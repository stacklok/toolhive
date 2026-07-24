// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"net/http"
	"testing"

	nameref "github.com/google/go-containerregistry/pkg/name"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	regmocks "github.com/stacklok/toolhive/pkg/registry/mocks"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

// dispatchProbe records which leaf dispatchSource routed to.
type dispatchProbe struct {
	gitURL    string
	ociRef    string
	registry  *registryResolveResult
	plainName string
}

// probeOps returns ops whose leaves record the routing into probe and
// return the given leaf results.
func probeOps(probe *dispatchProbe, ociErr error) sourceOps[string] {
	return sourceOps[string]{
		git: func(_ context.Context, gitURL string) (string, error) {
			probe.gitURL = gitURL
			return "git", nil
		},
		oci: func(_ context.Context, ref nameref.Reference) (string, error) {
			probe.ociRef = ref.String()
			if ociErr != nil {
				return "", ociErr
			}
			return "oci", nil
		},
		registry: func(_ context.Context, resolved *registryResolveResult) (string, error) {
			probe.registry = resolved
			return "registry", nil
		},
		plainName: func(_ context.Context, name string) (string, error) {
			probe.plainName = name
			return "plain", nil
		},
	}
}

func newDispatchTestService(t *testing.T, lookup *regmocks.MockProvider) *service {
	t.Helper()
	opts := []Option{}
	if lookup != nil {
		opts = append(opts, WithSkillLookup(lookup))
	}
	svc := New(storemocks.NewMockSkillStore(gomock.NewController(t)), opts...)
	return svc.(*service) //nolint:forcetypeassert // white-box test in the same package
}

func TestDispatchSourceRouting(t *testing.T) {
	t.Parallel()

	t.Run("git reference routes to git", func(t *testing.T) {
		t.Parallel()
		svc := newDispatchTestService(t, nil)
		var probe dispatchProbe
		got, err := dispatchSource(t.Context(), svc, "git://github.com/org/skill@main", probeOps(&probe, nil))
		require.NoError(t, err)
		assert.Equal(t, "git", got)
		assert.Equal(t, "git://github.com/org/skill@main", probe.gitURL)
	})

	t.Run("OCI reference routes to oci", func(t *testing.T) {
		t.Parallel()
		svc := newDispatchTestService(t, nil)
		var probe dispatchProbe
		got, err := dispatchSource(t.Context(), svc, "ghcr.io/org/skill:v1", probeOps(&probe, nil))
		require.NoError(t, err)
		assert.Equal(t, "oci", got)
		assert.Equal(t, "ghcr.io/org/skill:v1", probe.ociRef)
	})

	t.Run("plain name routes to plainName leaf when set", func(t *testing.T) {
		t.Parallel()
		svc := newDispatchTestService(t, nil)
		var probe dispatchProbe
		got, err := dispatchSource(t.Context(), svc, "my-skill", probeOps(&probe, nil))
		require.NoError(t, err)
		assert.Equal(t, "plain", got)
		assert.Equal(t, "my-skill", probe.plainName)
	})

	t.Run("plain name without plainName leaf resolves via registry", func(t *testing.T) {
		t.Parallel()
		lookup := regmocks.NewMockProvider(gomock.NewController(t))
		lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
			{Name: "my-skill", Packages: []regtypes.SkillPackage{
				{RegistryType: "oci", Identifier: "ghcr.io/test/my-skill:v1"},
			}},
		}, nil)
		svc := newDispatchTestService(t, lookup)
		var probe dispatchProbe
		ops := probeOps(&probe, nil)
		ops.plainName = nil
		got, err := dispatchSource(t.Context(), svc, "my-skill", ops)
		require.NoError(t, err)
		assert.Equal(t, "registry", got)
		require.NotNil(t, probe.registry)
	})

	t.Run("invalid plain name is rejected", func(t *testing.T) {
		t.Parallel()
		svc := newDispatchTestService(t, nil)
		var probe dispatchProbe
		_, err := dispatchSource(t.Context(), svc, "Not_A_Name", probeOps(&probe, nil))
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})
}

// TestDispatchSourceOCIFallback pins the ambiguity-fallback rules whose
// hand-copied divergence caused the upgrade-path majors: a failed direct
// OCI operation falls back to the registry catalogue only for ambiguous
// names, and the error precedence is fixed (registry error wins over the
// OCI error; a registry miss returns the original OCI error).
func TestDispatchSourceOCIFallback(t *testing.T) {
	t.Parallel()
	ociFailure := errors.New("name unknown")

	t.Run("ambiguous name falls back to registry on OCI failure", func(t *testing.T) {
		t.Parallel()
		lookup := regmocks.NewMockProvider(gomock.NewController(t))
		lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
			{Namespace: "io.github.test", Name: "my-skill", Packages: []regtypes.SkillPackage{
				{RegistryType: "oci", Identifier: "ghcr.io/test/my-skill:v2"},
			}},
		}, nil)
		svc := newDispatchTestService(t, lookup)
		var probe dispatchProbe
		got, err := dispatchSource(t.Context(), svc, "io.github.test/my-skill", probeOps(&probe, ociFailure))
		require.NoError(t, err)
		assert.Equal(t, "registry", got)
		require.NotNil(t, probe.registry)
		assert.NotNil(t, probe.registry.OCIRef)
	})

	t.Run("unambiguous reference never falls back", func(t *testing.T) {
		t.Parallel()
		svc := newDispatchTestService(t, nil) // no lookup: a fallback attempt would nil-panic expectations
		var probe dispatchProbe
		_, err := dispatchSource(t.Context(), svc, "ghcr.io/org/skill:v1", probeOps(&probe, ociFailure))
		require.ErrorIs(t, err, ociFailure)
	})

	t.Run("registry miss returns the original OCI error", func(t *testing.T) {
		t.Parallel()
		lookup := regmocks.NewMockProvider(gomock.NewController(t))
		lookup.EXPECT().SearchSkills("my-skill").Return(nil, nil)
		svc := newDispatchTestService(t, lookup)
		var probe dispatchProbe
		_, err := dispatchSource(t.Context(), svc, "io.github.test/my-skill", probeOps(&probe, ociFailure))
		require.ErrorIs(t, err, ociFailure)
	})
}
