// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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
// ensureInitialized
// ------------------------------------------------------------------

func TestEnsureInitialized_AppliesDefaultsOnZeroValues(t *testing.T) {
	t.Parallel()
	r := &StorageVersionMigratorReconciler{}
	r.ensureInitialized()

	assert.Equal(t, int64(defaultListPageSize), r.PageSize)
	assert.Equal(t, defaultCacheGCInterval, r.CacheGCInterval)
	require.NotNil(t, r.cache)
}

func TestEnsureInitialized_PreservesExplicitValues(t *testing.T) {
	t.Parallel()

	customCache := newMigrationCache(time.Minute)
	r := &StorageVersionMigratorReconciler{
		PageSize:        7,
		CacheGCInterval: 42 * time.Second,
		cache:           customCache,
	}
	r.ensureInitialized()

	assert.Equal(t, int64(7), r.PageSize, "non-zero PageSize must not be overwritten by defaults")
	assert.Equal(t, 42*time.Second, r.CacheGCInterval, "non-zero CacheGCInterval must not be overwritten")
	assert.Same(t, customCache, r.cache, "an existing cache must not be replaced")
}

func TestEnsureInitialized_IsIdempotent(t *testing.T) {
	t.Parallel()
	r := &StorageVersionMigratorReconciler{}
	r.ensureInitialized()
	firstCache := r.cache
	firstPageSize := r.PageSize

	// Calling again must not allocate a new cache or change any field.
	r.ensureInitialized()
	assert.Same(t, firstCache, r.cache)
	assert.Equal(t, firstPageSize, r.PageSize)
}

// ------------------------------------------------------------------
// Reconcile — early-return paths
//
// These tests use a fake client to exercise the branches of Reconcile that
// return before any per-CR work happens. The interesting per-CR work is
// covered by the envtest suite (which can simulate real apiserver semantics
// like storage-encoder elision and optimistic-lock 409). A fake client can't
// simulate those, so anything past the early-return points would be
// testing the mock.
// ------------------------------------------------------------------

// newReconcileTestScheme builds a runtime scheme with apiextensions/v1
// registered so the fake client can serialize CRDs.
func newReconcileTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(scheme))
	return scheme
}

// reconcileWithFake runs one Reconcile against a fake client and returns the
// result + error. The fake client is constructed from the supplied initial
// objects (typically zero or one CRD). The CRD's name in the Request is taken
// from the supplied crdName.
func reconcileWithFake(
	t *testing.T,
	crdName string,
	initialObjects ...client.Object,
) (ctrl.Result, error) {
	t.Helper()
	scheme := newReconcileTestScheme(t)
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(initialObjects...).
		Build()

	r := &StorageVersionMigratorReconciler{
		Client:    cli,
		APIReader: cli,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(8),
	}
	return r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: apitypes.NamespacedName{Name: crdName},
	})
}

func TestReconcile_ReturnsNilWhenCRDIsNotFound(t *testing.T) {
	t.Parallel()
	// No initial objects → APIReader.Get returns IsNotFound.
	res, err := reconcileWithFake(t, "missing.toolhive.stacklok.dev")
	require.NoError(t, err, "IsNotFound on the target CRD must not surface as a reconcile error")
	assert.Equal(t, ctrl.Result{}, res)
}

func TestReconcile_SkipsCRDInForeignGroup(t *testing.T) {
	t.Parallel()
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "widgets.example.com",
			Labels: map[string]string{AutoMigrateLabel: AutoMigrateValue},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.com",
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1", Storage: true, Served: true},
			},
		},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			StoredVersions: []string{"v1alpha1", "v1"},
		},
	}
	res, err := reconcileWithFake(t, crd.Name, crd)
	require.NoError(t, err, "non-toolhive group must short-circuit without error")
	assert.Equal(t, ctrl.Result{}, res)
}

func TestReconcile_SkipsCRDMissingOptInLabel(t *testing.T) {
	t.Parallel()
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "mcpservers.toolhive.stacklok.dev",
			Labels: nil, // explicitly no opt-in label
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: ToolhiveGroup,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1beta1", Storage: true, Served: true},
			},
		},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			StoredVersions: []string{"v1alpha1", "v1beta1"},
		},
	}
	res, err := reconcileWithFake(t, crd.Name, crd)
	require.NoError(t, err, "missing opt-in label must short-circuit without error")
	assert.Equal(t, ctrl.Result{}, res)
}

func TestReconcile_SkipsCRDWithNoStorageVersion(t *testing.T) {
	t.Parallel()
	// Pathological CRD — every version has Storage: false. The apiserver
	// would normally reject this at CRD-create time, so envtest can't reach
	// this branch. Reconcile must log + return nil rather than panic or
	// re-enqueue uselessly.
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "mcpservers.toolhive.stacklok.dev",
			Labels: map[string]string{AutoMigrateLabel: AutoMigrateValue},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: ToolhiveGroup,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1alpha1", Storage: false, Served: true},
				{Name: "v1beta1", Storage: false, Served: true},
			},
		},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			StoredVersions: []string{"v1alpha1", "v1beta1"},
		},
	}
	res, err := reconcileWithFake(t, crd.Name, crd)
	require.NoError(t, err, "no-storage-version CRD must short-circuit without error")
	assert.Equal(t, ctrl.Result{}, res)
}

func TestReconcile_ReturnsEarlyWhenStoredVersionsAlreadyClean(t *testing.T) {
	t.Parallel()
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "mcpservers.toolhive.stacklok.dev",
			Labels: map[string]string{AutoMigrateLabel: AutoMigrateValue},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: ToolhiveGroup,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     "MCPServer",
				ListKind: "MCPServerList",
				Plural:   "mcpservers",
				Singular: "mcpserver",
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1beta1", Storage: true, Served: true},
			},
		},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			StoredVersions: []string{"v1beta1"},
		},
	}
	res, err := reconcileWithFake(t, crd.Name, crd)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, res, "already-clean storedVersions must early-return without doing any work")
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

// ------------------------------------------------------------------
// Shared helpers for restoreCRs / restoreOne / patchStoredVersions tests
//
// These tests exercise the controller's orchestration logic against a fake
// client. The fake client supports unstructured objects as long as the kind
// is registered in the runtime scheme.
// ------------------------------------------------------------------

const (
	testCRGroup    = ToolhiveGroup
	testCRVersion  = "v1beta1"
	testCRKind     = "TestKind"
	testCRListKind = "TestKindList"
	testCRPlural   = "testkinds"
	testCRSingular = "testkind"
	testCRDName    = testCRPlural + "." + testCRGroup
)

// testCRGVK is the singular GVK for the synthetic CR kind used by the
// orchestration tests below.
func testCRGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: testCRGroup, Version: testCRVersion, Kind: testCRKind}
}

// schemeForCRD builds a runtime scheme with apiextensions/v1 plus the
// synthetic CR kind registered as unstructured. The fake client looks up
// kinds via the scheme on List, so without this registration List calls
// return "no kind is registered" errors.
func schemeForCRD(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(scheme))

	gvk := testCRGVK()
	listGVK := gvk
	listGVK.Kind = testCRListKind

	// Register both the singular and list kind so the fake client can
	// satisfy both Get/Update and List against the synthetic CR.
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
	return scheme
}

// makeTestCRD builds a CRD wired to the synthetic CR kind. The supplied
// storedVersions become the CRD's status.storedVersions. The CRD is
// labelled for opt-in by default (every restoreCRs/patchStoredVersions
// caller bypasses isManagedCRD, so the label is just for realism).
func makeTestCRD(storedVersions []string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testCRDName,
			Labels:          map[string]string{AutoMigrateLabel: AutoMigrateValue},
			ResourceVersion: "1",
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: testCRGroup,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     testCRKind,
				ListKind: testCRListKind,
				Plural:   testCRPlural,
				Singular: testCRSingular,
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: testCRVersion, Storage: true, Served: true},
			},
		},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			StoredVersions: storedVersions,
		},
	}
}

// makeTestCR returns an unstructured CR with the synthetic GVK and the
// given identity fields. UID is derived from name so tests can assert
// cache state by name.
func makeTestCR(namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(testCRGVK())
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetUID(apitypes.UID(name + "-uid"))
	u.SetResourceVersion("1")
	return u
}

// buildFakeReconciler constructs a fake-backed reconciler with the supplied
// initial objects (CRD + zero-or-more CRs) and optional interceptor funcs.
// The status subresource is registered for the CRD so Status().Patch works.
func buildFakeReconciler(
	t *testing.T,
	initialObjects []client.Object,
	funcs *interceptor.Funcs,
) *StorageVersionMigratorReconciler {
	t.Helper()
	scheme := schemeForCRD(t)
	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(initialObjects...).
		WithStatusSubresource(&apiextensionsv1.CustomResourceDefinition{})
	if funcs != nil {
		builder = builder.WithInterceptorFuncs(*funcs)
	}
	cli := builder.Build()
	r := &StorageVersionMigratorReconciler{
		Client:    cli,
		APIReader: cli,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(8),
	}
	r.ensureInitialized()
	return r
}

// ------------------------------------------------------------------
// restoreOne
// ------------------------------------------------------------------

func TestRestoreOne_GetAndUpdateSucceeds(t *testing.T) {
	t.Parallel()
	cr := makeTestCR("default", "obj-1")
	r := buildFakeReconciler(t, []client.Object{cr}, nil)

	restored, err := r.restoreOne(context.Background(), testCRGVK(), cr)

	require.NoError(t, err)
	require.NotNil(t, restored)
	assert.Equal(t, "obj-1", restored.GetName())
	assert.Equal(t, "default", restored.GetNamespace())
	// fake client bumps resourceVersion on Update; assert it changed so the
	// caller has a usable post-update RV to record in the cache.
	assert.NotEqual(t, "1", restored.GetResourceVersion(),
		"fake client must bump RV on Update; if it doesn't, restoreOne's cache add will record a stale RV")
}

func TestRestoreOne_PropagatesGetNotFound(t *testing.T) {
	t.Parallel()
	// CR not present in the fake store → Get returns NotFound.
	missing := makeTestCR("default", "missing")
	r := buildFakeReconciler(t, []client.Object{}, nil)

	_, err := r.restoreOne(context.Background(), testCRGVK(), missing)
	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err),
		"Get NotFound must propagate verbatim so restoreCRs can classify it")
}

func TestRestoreOne_PropagatesUpdateConflict(t *testing.T) {
	t.Parallel()
	cr := makeTestCR("default", "obj-1")

	gr := schema.GroupResource{Group: testCRGroup, Resource: testCRPlural}
	funcs := &interceptor.Funcs{
		Update: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.UpdateOption) error {
			return apierrors.NewConflict(gr, "obj-1", errors.New("injected conflict"))
		},
	}
	r := buildFakeReconciler(t, []client.Object{cr}, funcs)

	_, err := r.restoreOne(context.Background(), testCRGVK(), cr)
	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err),
		"Update Conflict must propagate verbatim so restoreCRs can classify it")
}

func TestRestoreOne_PropagatesUpdateGenericError(t *testing.T) {
	t.Parallel()
	cr := makeTestCR("default", "obj-1")
	funcs := &interceptor.Funcs{
		Update: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.UpdateOption) error {
			return errors.New("injected generic failure")
		},
	}
	r := buildFakeReconciler(t, []client.Object{cr}, funcs)

	_, err := r.restoreOne(context.Background(), testCRGVK(), cr)
	require.Error(t, err)
	assert.False(t, apierrors.IsConflict(err))
	assert.False(t, apierrors.IsNotFound(err))
	assert.Contains(t, err.Error(), "injected generic failure")
}

// ------------------------------------------------------------------
// restoreCRs
// ------------------------------------------------------------------

func TestRestoreCRs_HappyPathAllUpdatedAndCachePopulated(t *testing.T) {
	t.Parallel()
	crd := makeTestCRD([]string{"v1alpha1", testCRVersion})
	crs := []client.Object{
		makeTestCR("default", "obj-a"),
		makeTestCR("default", "obj-b"),
		makeTestCR("default", "obj-c"),
	}
	objs := append([]client.Object{crd}, crs...)
	r := buildFakeReconciler(t, objs, nil)

	err := r.restoreCRs(context.Background(), crd, testCRVersion)
	require.NoError(t, err)

	// Every CR's post-Update RV should now be in the cache.
	for _, obj := range crs {
		u := obj.(*unstructured.Unstructured)
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(testCRGVK())
		require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(u), live))
		assert.True(t, r.cache.has(crd.Name, live.GetUID(), live.GetResourceVersion()),
			"successful restoreOne must populate the cache with the post-Update RV")
	}
}

func TestRestoreCRs_EmptyListIsNoop(t *testing.T) {
	t.Parallel()
	crd := makeTestCRD([]string{"v1alpha1", testCRVersion})
	r := buildFakeReconciler(t, []client.Object{crd}, nil)

	err := r.restoreCRs(context.Background(), crd, testCRVersion)
	require.NoError(t, err)
	assert.Empty(t, r.cache.entries, "no CRs ⇒ no cache adds")
}

func TestRestoreCRs_PerCRNotFoundIsSilentlySkipped(t *testing.T) {
	t.Parallel()
	crd := makeTestCRD([]string{"v1alpha1", testCRVersion})
	crs := []client.Object{
		makeTestCR("default", "obj-a"),
		makeTestCR("default", "obj-b"),
	}
	objs := append([]client.Object{crd}, crs...)

	// Intercept Get inside restoreOne for obj-a only → NotFound. obj-b
	// continues normally.
	gr := schema.GroupResource{Group: testCRGroup, Resource: testCRPlural}
	funcs := &interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name == "obj-a" {
				return apierrors.NewNotFound(gr, "obj-a")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}
	r := buildFakeReconciler(t, objs, funcs)

	err := r.restoreCRs(context.Background(), crd, testCRVersion)
	require.NoError(t, err, "IsNotFound on a per-CR Get must not bubble up")

	// Cache must contain only obj-b — the NotFound path must skip the
	// cache add for obj-a.
	assert.Len(t, r.cache.entries, 1, "only the surviving CR may be cached")
}

func TestRestoreCRs_ConflictCountedAndSentinelReturned(t *testing.T) {
	t.Parallel()
	crd := makeTestCRD([]string{"v1alpha1", testCRVersion})
	crs := []client.Object{
		makeTestCR("default", "obj-a"),
		makeTestCR("default", "obj-b"),
	}
	objs := append([]client.Object{crd}, crs...)

	gr := schema.GroupResource{Group: testCRGroup, Resource: testCRPlural}
	funcs := &interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if obj.GetName() == "obj-a" {
				return apierrors.NewConflict(gr, "obj-a", errors.New("injected"))
			}
			return c.Update(ctx, obj, opts...)
		},
	}
	r := buildFakeReconciler(t, objs, funcs)

	err := r.restoreCRs(context.Background(), crd, testCRVersion)
	require.Error(t, err, "a swallowed Conflict must surface as a function-level error")
	assert.ErrorIs(t, err, errMigrationRetriedDueToConflicts,
		"the sentinel must be returned so the caller knows storedVersions is unsafe to trim")
}

func TestRestoreCRs_GenericErrorAggregated(t *testing.T) {
	t.Parallel()
	crd := makeTestCRD([]string{"v1alpha1", testCRVersion})
	cr := makeTestCR("default", "obj-a")
	funcs := &interceptor.Funcs{
		Update: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.UpdateOption) error {
			return errors.New("injected generic update failure")
		},
	}
	r := buildFakeReconciler(t, []client.Object{crd, cr}, funcs)

	err := r.restoreCRs(context.Background(), crd, testCRVersion)
	require.Error(t, err)
	assert.NotErrorIs(t, err, errMigrationRetriedDueToConflicts,
		"a generic error must NOT be misclassified as the conflict sentinel")
	assert.Contains(t, err.Error(), "injected generic update failure")
	assert.Contains(t, err.Error(), "obj-a", "aggregated error should name the failing CR")
}

func TestRestoreCRs_ConflictsPlusGenericErrorsReturnsAggregateNotSentinel(t *testing.T) {
	t.Parallel()
	crd := makeTestCRD([]string{"v1alpha1", testCRVersion})
	crs := []client.Object{
		makeTestCR("default", "obj-conflict"),
		makeTestCR("default", "obj-failure"),
	}
	objs := append([]client.Object{crd}, crs...)

	gr := schema.GroupResource{Group: testCRGroup, Resource: testCRPlural}
	funcs := &interceptor.Funcs{
		Update: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.UpdateOption) error {
			switch obj.GetName() {
			case "obj-conflict":
				return apierrors.NewConflict(gr, "obj-conflict", errors.New("injected conflict"))
			case "obj-failure":
				return errors.New("injected generic failure")
			}
			return nil
		},
	}
	r := buildFakeReconciler(t, objs, funcs)

	err := r.restoreCRs(context.Background(), crd, testCRVersion)
	require.Error(t, err)
	// When there is at least one non-conflict error, the aggregate wins —
	// the sentinel is only meaningful in the conflicts-only case.
	assert.NotErrorIs(t, err, errMigrationRetriedDueToConflicts,
		"mixed conflicts+errors must return the aggregate, not the conflict sentinel")
	assert.Contains(t, err.Error(), "injected generic failure")
}

func TestRestoreCRs_FirstListFailsReturnsImmediately(t *testing.T) {
	t.Parallel()
	crd := makeTestCRD([]string{"v1alpha1", testCRVersion})
	funcs := &interceptor.Funcs{
		List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
			return errors.New("injected list failure")
		},
	}
	r := buildFakeReconciler(t, []client.Object{crd}, funcs)

	err := r.restoreCRs(context.Background(), crd, testCRVersion)
	require.Error(t, err)
	// schema.GroupVersionKind.String() formats as "group/version, Kind=kind".
	assert.Contains(t, err.Error(), "Kind="+testCRListKind,
		"list failures must be wrapped with the list GVK for diagnosability")
	assert.Contains(t, err.Error(), "injected list failure")
}

func TestRestoreCRs_CacheSkipsAlreadyMigratedCRs(t *testing.T) {
	t.Parallel()
	crd := makeTestCRD([]string{"v1alpha1", testCRVersion})
	cr := makeTestCR("default", "obj-a")
	objs := []client.Object{crd, cr}

	// Pre-populate the cache so restoreCRs finds the CR's (UID, RV) on
	// lookup and skips the Update altogether.
	var updateCount int32
	funcs := &interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			atomic.AddInt32(&updateCount, 1)
			return c.Update(ctx, obj, opts...)
		},
	}
	r := buildFakeReconciler(t, objs, funcs)

	// The CR's RV in the fake store is "1" after WithObjects.
	r.cache.add(crd.Name, cr.GetUID(), "1")

	err := r.restoreCRs(context.Background(), crd, testCRVersion)
	require.NoError(t, err)
	assert.Equal(t, int32(0), atomic.LoadInt32(&updateCount),
		"a fresh cache hit must skip the Update entirely")
}

func TestRestoreCRs_PaginationWiresContinueTokenThroughOptions(t *testing.T) {
	t.Parallel()
	crd := makeTestCRD([]string{"v1alpha1", testCRVersion})
	r := buildFakeReconciler(t, []client.Object{crd}, nil)
	r.PageSize = 3

	// Replace APIReader with one that synthesizes pagination so the
	// controller's continue-token plumbing actually has something to thread.
	listCalls := []listCallRecord{}
	r.APIReader = &paginatingFakeReader{
		Reader:  r.Client,
		records: &listCalls,
	}

	err := r.restoreCRs(context.Background(), crd, testCRVersion)
	require.NoError(t, err)

	require.Len(t, listCalls, 3, "PageSize=3, total=7 should yield exactly three list calls (3+3+1)")
	// First call: Limit=3, no Continue token.
	assert.Equal(t, int64(3), listCalls[0].limit)
	assert.Empty(t, listCalls[0].continueToken)
	// Subsequent calls: Limit=3, Continue token from prior page.
	assert.Equal(t, int64(3), listCalls[1].limit)
	assert.Equal(t, "page-2", listCalls[1].continueToken)
	assert.Equal(t, int64(3), listCalls[2].limit)
	assert.Equal(t, "page-3", listCalls[2].continueToken)
}

// ------------------------------------------------------------------
// patchStoredVersions
// ------------------------------------------------------------------

func TestPatchStoredVersions_TrimsStoredVersionsOnSuccess(t *testing.T) {
	t.Parallel()
	crd := makeTestCRD([]string{"v1alpha1", testCRVersion})
	r := buildFakeReconciler(t, []client.Object{crd}, nil)

	err := r.patchStoredVersions(context.Background(), crd, testCRVersion)
	require.NoError(t, err)

	// Re-read from the fake store and verify the trim landed.
	live := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, r.Get(context.Background(), client.ObjectKey{Name: crd.Name}, live))
	assert.Equal(t, []string{testCRVersion}, live.Status.StoredVersions)
}

func TestPatchStoredVersions_TargetsStatusSubresource(t *testing.T) {
	t.Parallel()
	crd := makeTestCRD([]string{"v1alpha1", testCRVersion})

	var mainResourcePatchCalls int32
	var statusSubresourcePatchCalls int32
	funcs := &interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			atomic.AddInt32(&mainResourcePatchCalls, 1)
			return c.Patch(ctx, obj, patch, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if subResourceName == "status" {
				atomic.AddInt32(&statusSubresourcePatchCalls, 1)
			}
			return c.Status().Patch(ctx, obj, patch, opts...)
		},
	}
	r := buildFakeReconciler(t, []client.Object{crd}, funcs)

	require.NoError(t, r.patchStoredVersions(context.Background(), crd, testCRVersion))

	assert.Equal(t, int32(0), atomic.LoadInt32(&mainResourcePatchCalls),
		"patchStoredVersions must NOT hit the main-resource Patch endpoint")
	assert.Equal(t, int32(1), atomic.LoadInt32(&statusSubresourcePatchCalls),
		"patchStoredVersions must hit the /status subresource exactly once")
}

func TestPatchStoredVersions_UsesOptimisticLock(t *testing.T) {
	t.Parallel()
	crd := makeTestCRD([]string{"v1alpha1", testCRVersion})

	// Capture the patch data so we can confirm it carries resourceVersion
	// (the marker that MergeFromWithOptimisticLock is in effect — plain
	// MergeFrom does NOT include resourceVersion in the patch body).
	var capturedPatchBody []byte
	funcs := &interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			data, err := patch.Data(obj)
			if err != nil {
				return err
			}
			capturedPatchBody = data
			return c.Status().Patch(ctx, obj, patch, opts...)
		},
	}
	r := buildFakeReconciler(t, []client.Object{crd}, funcs)

	require.NoError(t, r.patchStoredVersions(context.Background(), crd, testCRVersion))
	require.NotEmpty(t, capturedPatchBody, "interceptor never captured the patch body")

	body := string(capturedPatchBody)
	assert.Contains(t, body, `"resourceVersion":"1"`,
		"optimistic-lock patches must include the source resourceVersion as a precondition")
	assert.Contains(t, body, `"storedVersions":["`+testCRVersion+`"]`,
		"patch body must overwrite storedVersions to exactly [storageVersion]")
}

func TestPatchStoredVersions_PropagatesError(t *testing.T) {
	t.Parallel()
	crd := makeTestCRD([]string{"v1alpha1", testCRVersion})

	funcs := &interceptor.Funcs{
		SubResourcePatch: func(_ context.Context, _ client.Client, _ string, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
			return errors.New("injected patch failure")
		},
	}
	r := buildFakeReconciler(t, []client.Object{crd}, funcs)

	err := r.patchStoredVersions(context.Background(), crd, testCRVersion)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "injected patch failure")
}

// ------------------------------------------------------------------
// Test doubles used by the orchestration tests above
// ------------------------------------------------------------------

// listCallRecord captures the ListOptions of a single List call so the
// pagination test can verify Limit + Continue threading.
type listCallRecord struct {
	limit         int64
	continueToken string
}

// paginatingFakeReader satisfies client.Reader by synthesizing three pages
// of test CRs (7 items at PageSize=3 yields 3+3+1). It records each List
// call's options so the test can assert continue-token threading.
type paginatingFakeReader struct {
	client.Reader // embedded only so Get satisfies the interface; List is overridden
	records       *[]listCallRecord
}

func (p *paginatingFakeReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	// Synthesize Get responses for the per-CR Get in restoreOne. Any name
	// that looks like one of our synthetic CRs is treated as found.
	if u, ok := obj.(*unstructured.Unstructured); ok && strings.HasPrefix(key.Name, "obj-") {
		u.SetGroupVersionKind(testCRGVK())
		u.SetNamespace(key.Namespace)
		u.SetName(key.Name)
		u.SetUID(apitypes.UID(key.Name + "-uid"))
		u.SetResourceVersion("1")
		return nil
	}
	return p.Reader.Get(ctx, key, obj, opts...)
}

func (p *paginatingFakeReader) List(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
	rec := listCallRecord{}
	listOpts := &client.ListOptions{}
	for _, o := range opts {
		o.ApplyToList(listOpts)
	}
	rec.limit = listOpts.Limit
	rec.continueToken = listOpts.Continue
	*p.records = append(*p.records, rec)

	ul, ok := list.(*unstructured.UnstructuredList)
	if !ok {
		return fmt.Errorf("paginatingFakeReader only supports UnstructuredList, got %T", list)
	}

	// Synthesize one of three pages based on the continue token.
	type page struct {
		names []string
		next  string
	}
	pages := map[string]page{
		"":       {names: []string{"obj-1", "obj-2", "obj-3"}, next: "page-2"},
		"page-2": {names: []string{"obj-4", "obj-5", "obj-6"}, next: "page-3"},
		"page-3": {names: []string{"obj-7"}, next: ""},
	}
	pg, found := pages[rec.continueToken]
	if !found {
		return fmt.Errorf("paginatingFakeReader: unknown continue token %q", rec.continueToken)
	}

	items := make([]unstructured.Unstructured, 0, len(pg.names))
	for _, name := range pg.names {
		u := makeTestCR("default", name)
		items = append(items, *u)
	}
	ul.Items = items
	ul.SetContinue(pg.next)
	return nil
}
