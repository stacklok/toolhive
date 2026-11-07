// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	thvregistry "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/registry/api"
	"github.com/stacklok/toolhive/pkg/registry/auth"
)

// skillsListPayload is the wire format served by the test registry for the
// skills list endpoint. Field names mirror skillsListResponse in
// pkg/registry/api/skills_client.go so the client decodes them cleanly.
type skillsListPayload struct {
	Skills   []*thvregistry.Skill `json:"skills"`
	Metadata struct {
		Count      int    `json:"count"`
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

// newSkillsStatusServer returns an httptest.Server whose skills list endpoint
// responds with status.Load() when it is non-zero, and a normal page of skills
// otherwise. The servers list endpoint always answers an empty list so the
// constructor's validation probe succeeds.
func newSkillsStatusServer(t *testing.T, skills []*thvregistry.Skill, status *atomic.Int32) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/v0.1/x/dev.toolhive/skills", func(w http.ResponseWriter, _ *http.Request) {
		if s := status.Load(); s != 0 {
			w.WriteHeader(int(s))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		payload := skillsListPayload{Skills: skills}
		payload.Metadata.Count = len(skills)
		require.NoError(t, json.NewEncoder(w).Encode(payload))
	})
	mux.HandleFunc("/v0.1/servers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[],"metadata":{"next_cursor":""}}`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestCachedProvider_SkillsAuthErrorWithWarmCache verifies that a 401 from the
// registry is propagated even when a warm cache could otherwise mask it. A
// revoked token must not silently serve stale skills.
func TestCachedProvider_SkillsAuthErrorWithWarmCache(t *testing.T) {
	t.Parallel()

	skills := []*thvregistry.Skill{
		{Namespace: "io.github.stacklok", Name: "code-reviewer", Version: "1.0.0"},
	}
	var status atomic.Int32 // 0 = healthy
	srv := newSkillsStatusServer(t, skills, &status)

	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, nil)
	require.NoError(t, err)

	// Warm the cache.
	got, err := provider.ListAvailableSkills()
	require.NoError(t, err)
	require.Len(t, got, 1)

	// Flip the registry to 401 and expire the cache.
	status.Store(http.StatusUnauthorized)
	provider.skillsMu.Lock()
	provider.skillsTime = provider.skillsTime.Add(-defaultCacheTTL - 1)
	provider.skillsMu.Unlock()

	got2, err := provider.ListAvailableSkills()
	require.Error(t, err, "401 must propagate, not be masked by stale cache")
	require.Nil(t, got2)
	require.True(t,
		errors.Is(err, api.ErrRegistryUnauthorized),
		"expected errors.Is(err, ErrRegistryUnauthorized); got: %v", err)
}

// TestCachedProvider_SkillsTransient500WithWarmCache verifies that a transient
// 500 with a warm cache degrades to stale data (the auth path is the exception,
// not the rule).
func TestCachedProvider_SkillsTransient500WithWarmCache(t *testing.T) {
	t.Parallel()

	skills := []*thvregistry.Skill{
		{Namespace: "io.github.stacklok", Name: "code-reviewer", Version: "1.0.0"},
	}
	var status atomic.Int32
	srv := newSkillsStatusServer(t, skills, &status)

	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, nil)
	require.NoError(t, err)

	// Warm the cache.
	got, err := provider.ListAvailableSkills()
	require.NoError(t, err)
	require.Len(t, got, 1)

	// Flip to 500 and expire the cache.
	status.Store(http.StatusInternalServerError)
	provider.skillsMu.Lock()
	provider.skillsTime = provider.skillsTime.Add(-defaultCacheTTL - 1)
	provider.skillsMu.Unlock()

	got2, err := provider.ListAvailableSkills()
	require.NoError(t, err, "transient 500 with warm cache should serve stale data")
	require.Len(t, got2, 1)
	require.Equal(t, "code-reviewer", got2[0].Name)
}

// TestCachedProvider_SkillsTransient500WithColdCache verifies that a transient
// 500 with no cache returns the error rather than (nil, nil), so the v0.1
// registry route does not answer 200 [] on a real failure.
func TestCachedProvider_SkillsTransient500WithColdCache(t *testing.T) {
	t.Parallel()

	var status atomic.Int32
	status.Store(http.StatusInternalServerError)
	srv := newSkillsStatusServer(t, nil, &status)

	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, nil)
	require.NoError(t, err)

	got, err := provider.ListAvailableSkills()
	require.Error(t, err, "transient 500 with cold cache must return the error, not nil,nil")
	require.Nil(t, got)
}

// newSkillsPaginationServer returns an httptest.Server whose skills list
// endpoint serves one page of skills carrying a nextCursor, and answers 404 on
// every later page when afterPage404 is set. The servers list endpoint always
// answers an empty list so the constructor's validation probe succeeds.
func newSkillsPaginationServer(t *testing.T, skills []*thvregistry.Skill, afterPage404 bool) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/v0.1/x/dev.toolhive/skills", func(w http.ResponseWriter, r *http.Request) {
		continues := r.URL.Query().Get("cursor") != ""
		if continues && afterPage404 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		payload := skillsListPayload{}
		if !continues {
			payload.Skills = skills
			payload.Metadata.Count = len(skills)
			// ask the client to come back for a second page
			payload.Metadata.NextCursor = "page-2"
		}
		require.NoError(t, json.NewEncoder(w).Encode(payload))
	})
	mux.HandleFunc("/v0.1/servers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[],"metadata":{"next_cursor":""}}`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestCachedProvider_SkillsEndpointAbsent verifies that a registry without a
// skills endpoint (404, e.g. a servers-only registry) yields an empty skill
// list and no error, preserving the pre-skills behavior rather than turning
// the probe into a route-level 500.
func TestCachedProvider_SkillsEndpointAbsent(t *testing.T) {
	t.Parallel()

	var status atomic.Int32
	status.Store(http.StatusNotFound)
	srv := newSkillsStatusServer(t, nil, &status)

	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, nil)
	require.NoError(t, err)

	got, err := provider.ListAvailableSkills()
	require.NoError(t, err, "a missing skills endpoint must not fail the route")
	require.Empty(t, got)
}

// TestCachedProvider_SkillsTokenFailurePropagates verifies that a token
// acquisition failure (wrapped with auth.ErrRegistryAuthRequired) propagates
// like a 401/403, even with a warm cache: a revoked or missing token must not
// silently serve stale skills.
func TestCachedProvider_SkillsTokenFailurePropagates(t *testing.T) {
	t.Parallel()

	skills := []*thvregistry.Skill{
		{Namespace: "io.github.stacklok", Name: "code-reviewer", Version: "1.0.0"},
	}
	var status atomic.Int32 // 0 = healthy
	srv := newSkillsStatusServer(t, skills, &status)

	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, nil)
	require.NoError(t, err)

	// Warm the cache.
	got, err := provider.ListAvailableSkills()
	require.NoError(t, err)
	require.Len(t, got, 1)

	// Swap in a failing token source and expire the cache.
	var failing failingTokenSource
	failing.fail.Store(true)
	provider.tokenSource = &failing
	provider.skillsMu.Lock()
	provider.skillsTime = provider.skillsTime.Add(-defaultCacheTTL - 1)
	provider.skillsMu.Unlock()

	got2, err := provider.ListAvailableSkills()
	require.Error(t, err, "token acquisition failure must propagate, not be masked by stale cache")
	require.Nil(t, got2)
	require.True(t,
		errors.Is(err, auth.ErrRegistryAuthRequired),
		"expected errors.Is(err, ErrRegistryAuthRequired); got: %v", err)
}

// TestCachedProvider_SkillsLaterPage404WithColdCache verifies that a 404 on a
// page after the first is a real failure, not a missing endpoint: the endpoint
// answered the first page, so what the caller gets is not "this registry has no
// skills". With no cache the error is surfaced rather than reported as success.
func TestCachedProvider_SkillsLaterPage404WithColdCache(t *testing.T) {
	t.Parallel()

	skills := []*thvregistry.Skill{
		{Namespace: "io.github.stacklok", Name: "code-reviewer", Version: "1.0.0"},
	}
	srv := newSkillsPaginationServer(t, skills, true)

	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, nil)
	require.NoError(t, err)

	got, err := provider.ListAvailableSkills()
	require.Error(t, err, "a 404 after the first page must not be reported as an empty skill list")
	require.Nil(t, got)
	require.False(t,
		errors.Is(err, api.ErrSkillsEndpointNotServed),
		"a 404 on a later page is not a missing endpoint: %v", err)
}

// TestCachedProvider_SkillsLaterPage404WithWarmCache verifies that a 404 on a
// later page with an expired warm cache degrades to the stale skills, as any
// other non-auth failure does, instead of reporting an empty list.
func TestCachedProvider_SkillsLaterPage404WithWarmCache(t *testing.T) {
	t.Parallel()

	healthy := newSkillsPaginationServer(t, []*thvregistry.Skill{
		{Namespace: "io.github.stacklok", Name: "code-reviewer", Version: "1.0.0"},
	}, false)

	provider, err := NewCachedAPIRegistryProvider(healthy.URL, true, false, nil)
	require.NoError(t, err)

	got, err := provider.ListAvailableSkills()
	require.NoError(t, err)
	require.Len(t, got, 1)

	// Point the provider at a registry that 404s after the first page and let
	// the cache expire.
	broken := newSkillsPaginationServer(t, nil, true)
	provider.apiURL = broken.URL
	provider.skillsMu.Lock()
	provider.skillsTime = provider.skillsTime.Add(-defaultCacheTTL - 1)
	provider.skillsMu.Unlock()

	got2, err := provider.ListAvailableSkills()
	require.NoError(t, err, "a non-auth failure with a warm cache should serve stale data")
	require.Len(t, got2, 1)
	require.Equal(t, "code-reviewer", got2[0].Name)
}
