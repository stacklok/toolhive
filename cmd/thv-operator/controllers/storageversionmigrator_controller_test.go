// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
)

// ------------------------------------------------------------------
// migrationCache
// ------------------------------------------------------------------

// fakeClock is an injectable clock for migrationCache TTL tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newCache builds a migrationCache wired to a fake clock so tests control time.
func newCache(t *testing.T, ttl time.Duration) (*migrationCache, *fakeClock) {
	t.Helper()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c := &migrationCache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
		now:     clock.Now,
	}
	return c, clock
}

func TestMigrationCache_HasReturnsFalseOnEmpty(t *testing.T) {
	t.Parallel()
	c, _ := newCache(t, time.Hour)
	assert.False(t, c.has("crd-a", "uid-1", "rv-100"))
}

func TestMigrationCache_AddThenHasReturnsTrue(t *testing.T) {
	t.Parallel()
	c, _ := newCache(t, time.Hour)
	c.add("crd-a", "uid-1", "rv-100")
	assert.True(t, c.has("crd-a", "uid-1", "rv-100"))
}

func TestMigrationCache_HasReturnsFalseOnRVMismatch(t *testing.T) {
	t.Parallel()
	c, _ := newCache(t, time.Hour)
	c.add("crd-a", "uid-1", "rv-100")

	// Same CRD and UID but a different RV — caller's CR was updated by some
	// other writer after our last cache add. Must be treated as a miss so the
	// CR gets re-Updated.
	assert.False(t, c.has("crd-a", "uid-1", "rv-200"))
	// Entry was a fresh-RV miss, not an expiry, so it must remain in the map.
	assert.Len(t, c.entries, 1)
}

func TestMigrationCache_TTLExpiryLazilyEvictsInHas(t *testing.T) {
	t.Parallel()
	c, clock := newCache(t, time.Hour)
	c.add("crd-a", "uid-1", "rv-100")
	require.True(t, c.has("crd-a", "uid-1", "rv-100"))

	clock.Advance(2 * time.Hour)

	assert.False(t, c.has("crd-a", "uid-1", "rv-100"),
		"has must return false once the entry's TTL has elapsed")
	assert.Empty(t, c.entries,
		"expired entries must be removed from the map on lookup, not left to leak")
}

func TestMigrationCache_AddOverwritesExistingEntry(t *testing.T) {
	t.Parallel()
	c, clock := newCache(t, time.Hour)
	c.add("crd-a", "uid-1", "rv-100")

	// Advance halfway through the TTL, then re-add with a new RV.
	clock.Advance(30 * time.Minute)
	c.add("crd-a", "uid-1", "rv-200")

	// The new RV should be the cache's record (RV-100 must not match anymore).
	assert.False(t, c.has("crd-a", "uid-1", "rv-100"))
	assert.True(t, c.has("crd-a", "uid-1", "rv-200"))

	// The expiry should have been refreshed by the second add — going another
	// 40 minutes forward (total 70m) must NOT expire the entry, because the
	// re-add reset the clock to the 0-of-60m point.
	clock.Advance(40 * time.Minute)
	assert.True(t, c.has("crd-a", "uid-1", "rv-200"),
		"add must refresh expiresAt, otherwise an in-flight CR would be re-walked at exactly the wrong moment")
}

func TestMigrationCache_GCEvictsOnlyExpiredEntries(t *testing.T) {
	t.Parallel()
	c, clock := newCache(t, time.Hour)

	c.add("crd-a", "uid-1", "rv-100")
	clock.Advance(30 * time.Minute)
	c.add("crd-b", "uid-2", "rv-200")

	// Advance so the first entry is expired (90m total) but the second is not
	// (60m since its own add, which is exactly TTL — strictly After is false).
	clock.Advance(60 * time.Minute)

	c.gc()

	// uid-1 expired and must be evicted.
	assert.False(t, c.has("crd-a", "uid-1", "rv-100"))
	// uid-2 still inside its TTL window and must survive.
	assert.True(t, c.has("crd-b", "uid-2", "rv-200"))
	assert.Len(t, c.entries, 1)
}

func TestMigrationCache_GCNoopOnEmpty(t *testing.T) {
	t.Parallel()
	c, _ := newCache(t, time.Hour)

	// Must not panic on an empty map and must leave the map empty.
	require.NotPanics(t, c.gc)
	assert.Empty(t, c.entries)
}

// TestMigrationCache_ConcurrentAccess exercises the mutex contract — running
// has/add/gc from many goroutines in parallel under -race must not produce a
// data race or panic. The assertions only verify the loop completed; the value
// of the test is the race detector.
func TestMigrationCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	c, _ := newCache(t, time.Hour)

	const goroutines = 16
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	for i := 0; i < goroutines; i++ {
		go func(g int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				c.add("crd-a", apitypes.UID(rune('a'+g)), "rv-1")
			}
		}(i)
		go func(g int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = c.has("crd-a", apitypes.UID(rune('a'+g)), "rv-1")
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				c.gc()
			}
		}()
	}

	// Bounded wait so a deadlock fails fast instead of hanging the suite.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for cache goroutines to finish — possible deadlock in cache mutex")
	}
}

func TestMigrationCache_KeyNoCollisionAcrossCRDs(t *testing.T) {
	t.Parallel()
	c, _ := newCache(t, time.Hour)

	// Two CRDs sharing a UID (in practice impossible — apiserver UIDs are
	// globally unique — but the cache must defend against UID re-use anyway).
	c.add("crd-a", "uid-shared", "rv-100")
	c.add("crd-b", "uid-shared", "rv-200")

	assert.True(t, c.has("crd-a", "uid-shared", "rv-100"))
	assert.True(t, c.has("crd-b", "uid-shared", "rv-200"))
	// Cross-CRD lookup must NOT match the wrong entry.
	assert.False(t, c.has("crd-a", "uid-shared", "rv-200"))
	assert.False(t, c.has("crd-b", "uid-shared", "rv-100"))
}

func TestMigrationCache_KeyNoCollisionAcrossUIDs(t *testing.T) {
	t.Parallel()
	c, _ := newCache(t, time.Hour)

	c.add("crd-a", "uid-1", "rv-100")
	c.add("crd-a", "uid-2", "rv-200")

	assert.True(t, c.has("crd-a", "uid-1", "rv-100"))
	assert.True(t, c.has("crd-a", "uid-2", "rv-200"))
	assert.False(t, c.has("crd-a", "uid-1", "rv-200"))
	assert.False(t, c.has("crd-a", "uid-2", "rv-100"))
}

func TestNewMigrationCache_InitializesUsableInstance(t *testing.T) {
	t.Parallel()
	c := newMigrationCache(5 * time.Minute)

	require.NotNil(t, c)
	require.NotNil(t, c.entries, "entries map must be non-nil to avoid nil-map writes")
	assert.Equal(t, 5*time.Minute, c.ttl)
	require.NotNil(t, c.now, "clock must default to a real time source")

	// Sanity: it should be writable/readable through the public methods.
	c.add("crd-a", "uid-1", "rv-100")
	assert.True(t, c.has("crd-a", "uid-1", "rv-100"))
}

// ------------------------------------------------------------------
// isManagedCRD
// ------------------------------------------------------------------

func TestIsManagedCRD(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		group  string
		labels map[string]string
		want   bool
	}{
		{
			name:   "toolhive group with opt-in label is managed",
			group:  ToolhiveGroup,
			labels: map[string]string{AutoMigrateLabel: AutoMigrateValue},
			want:   true,
		},
		{
			name:   "toolhive group with wrong label value is not managed",
			group:  ToolhiveGroup,
			labels: map[string]string{AutoMigrateLabel: "false"},
			want:   false,
		},
		{
			name:   "toolhive group with empty label value is not managed",
			group:  ToolhiveGroup,
			labels: map[string]string{AutoMigrateLabel: ""},
			want:   false,
		},
		{
			name:   "toolhive group with no opt-in label is not managed",
			group:  ToolhiveGroup,
			labels: map[string]string{"unrelated.example.com/key": "true"},
			want:   false,
		},
		{
			name:   "toolhive group with nil labels map is not managed",
			group:  ToolhiveGroup,
			labels: nil,
			want:   false,
		},
		{
			name:   "non-toolhive group with opt-in label is not managed",
			group:  "example.com",
			labels: map[string]string{AutoMigrateLabel: AutoMigrateValue},
			want:   false,
		},
		{
			name:   "non-toolhive group with no label is not managed",
			group:  "example.com",
			labels: nil,
			want:   false,
		},
		{
			name:   "empty group is not managed",
			group:  "",
			labels: map[string]string{AutoMigrateLabel: AutoMigrateValue},
			want:   false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			crd := &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Labels: tc.labels},
				Spec:       apiextensionsv1.CustomResourceDefinitionSpec{Group: tc.group},
			}
			assert.Equal(t, tc.want, isManagedCRD(crd))
		})
	}
}

// ------------------------------------------------------------------
// isToolhiveCRDName
// ------------------------------------------------------------------

func TestIsToolhiveCRDName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		crdName  string
		expected bool
	}{
		{name: "well-formed toolhive CRD name matches", crdName: "mcpservers.toolhive.stacklok.dev", expected: true},
		{name: "different toolhive CRD also matches", crdName: "virtualmcpservers.toolhive.stacklok.dev", expected: true},
		{name: "empty string does not match", crdName: "", expected: false},
		{name: "group string with no plural prefix does not match", crdName: "toolhive.stacklok.dev", expected: false},
		{
			name:     "group as bare suffix without leading dot does not match",
			crdName:  "footoolhive.stacklok.dev",
			expected: false,
		},
		{name: "foreign group does not match", crdName: "widgets.example.com", expected: false},
		{
			name:     "toolhive suffix in the middle of the name does not match",
			crdName:  "foo.toolhive.stacklok.dev.example.com",
			expected: false,
		},
		{
			name:     "similar-looking but distinct group does not match",
			crdName:  "mcpservers.toolhive-stacklok.dev",
			expected: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, isToolhiveCRDName(tc.crdName))
		})
	}
}

// ------------------------------------------------------------------
// findStorageVersion
// ------------------------------------------------------------------

func TestFindStorageVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		versions    []apiextensionsv1.CustomResourceDefinitionVersion
		wantName    string
		wantOK      bool
	}{
		{
			name: "single storage version returns its name",
			versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1", Storage: true},
			},
			wantName: "v1",
			wantOK:   true,
		},
		{
			name: "multiple versions returns the storage entry",
			versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1alpha1", Storage: false},
				{Name: "v1beta1", Storage: true},
				{Name: "v1beta2", Storage: false},
			},
			wantName: "v1beta1",
			wantOK:   true,
		},
		{
			name: "no storage version returns empty and false",
			versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1alpha1", Storage: false},
				{Name: "v1beta1", Storage: false},
			},
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "empty versions list returns empty and false",
			versions: nil,
			wantName: "",
			wantOK:   false,
		},
		{
			name: "storage at first index is returned",
			versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1beta1", Storage: true},
				{Name: "v1alpha1", Storage: false},
			},
			wantName: "v1beta1",
			wantOK:   true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			crd := &apiextensionsv1.CustomResourceDefinition{
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{Versions: tc.versions},
			}
			got, ok := findStorageVersion(crd)
			assert.Equal(t, tc.wantName, got)
			assert.Equal(t, tc.wantOK, ok)
		})
	}
}

// ------------------------------------------------------------------
// isMigrationNeeded
// ------------------------------------------------------------------

func TestIsMigrationNeeded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		storedVersions []string
		storageVersion string
		want           bool
	}{
		{
			name:           "single matching entry is not needed",
			storedVersions: []string{"v1beta1"},
			storageVersion: "v1beta1",
			want:           false,
		},
		{
			name:           "single mismatching entry is needed",
			storedVersions: []string{"v1alpha1"},
			storageVersion: "v1beta1",
			want:           true,
		},
		{
			name:           "two entries including target is needed",
			storedVersions: []string{"v1alpha1", "v1beta1"},
			storageVersion: "v1beta1",
			want:           true,
		},
		{
			name:           "two entries excluding target is needed",
			storedVersions: []string{"v1alpha1", "v1alpha2"},
			storageVersion: "v1beta1",
			want:           true,
		},
		{
			name:           "empty list is needed",
			storedVersions: nil,
			storageVersion: "v1beta1",
			want:           true,
		},
		{
			name:           "single-entry list with target at element 0 only (length==1 happy case) matches when equal",
			storedVersions: []string{"v1beta2"},
			storageVersion: "v1beta2",
			want:           false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			crd := &apiextensionsv1.CustomResourceDefinition{
				Status: apiextensionsv1.CustomResourceDefinitionStatus{StoredVersions: tc.storedVersions},
			}
			assert.Equal(t, tc.want, isMigrationNeeded(crd, tc.storageVersion))
		})
	}
}
