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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// migrationCache

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

// TestMigrationCache_KeyIsolation exercises both key-isolation axes: two
// distinct CRDs sharing a UID must not collide, and two distinct UIDs under
// the same CRD must not collide. UID re-use across CRDs is impossible in
// practice (apiserver UIDs are globally unique) but the cache must defend
// against it anyway.
func TestMigrationCache_KeyIsolation(t *testing.T) {
	t.Parallel()
	c, _ := newCache(t, time.Hour)

	// Cross-CRD, same UID.
	c.add("crd-a", "uid-shared", "rv-100")
	c.add("crd-b", "uid-shared", "rv-200")
	assert.True(t, c.has("crd-a", "uid-shared", "rv-100"))
	assert.True(t, c.has("crd-b", "uid-shared", "rv-200"))
	assert.False(t, c.has("crd-a", "uid-shared", "rv-200"))
	assert.False(t, c.has("crd-b", "uid-shared", "rv-100"))

	// Same CRD, cross UID.
	c.add("crd-c", "uid-1", "rv-300")
	c.add("crd-c", "uid-2", "rv-400")
	assert.True(t, c.has("crd-c", "uid-1", "rv-300"))
	assert.True(t, c.has("crd-c", "uid-2", "rv-400"))
	assert.False(t, c.has("crd-c", "uid-1", "rv-400"))
	assert.False(t, c.has("crd-c", "uid-2", "rv-300"))
}

// ensureInitialized

func TestEnsureInitialized_AppliesDefaultsOnZeroValues(t *testing.T) {
	t.Parallel()
	r := &StorageVersionMigratorReconciler{}
	r.ensureInitialized()
	assert.Equal(t, int64(defaultListPageSize), r.PageSize)
	assert.Equal(t, defaultCacheGCInterval, r.CacheGCInterval)
	require.NotNil(t, r.cache)
}

func TestEnsureInitialized_PreservesExplicitValuesAndIsIdempotent(t *testing.T) {
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

	// Idempotent: a second call must not change any field.
	r.ensureInitialized()
	assert.Equal(t, int64(7), r.PageSize)
	assert.Same(t, customCache, r.cache)
}

// Reconcile early-return paths. Per-CR work paths are covered by the envtest
// suite, which can simulate real apiserver semantics the fake client cannot
// (storage-encoder elision, optimistic-lock 409).

// reconcileCRD builds a CRD for Reconcile early-return tests.
func reconcileCRD(name, group string, labels map[string]string, versions []apiextensionsv1.CustomResourceDefinitionVersion, stored []string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, ResourceVersion: "1"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind: "MCPServer", ListKind: "MCPServerList", Plural: "mcpservers", Singular: "mcpserver",
			},
			Versions: versions,
		},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{StoredVersions: stored},
	}
}

func TestReconcile_EarlyReturns(t *testing.T) {
	t.Parallel()

	const mcpName = "mcpservers.toolhive.stacklok.dev"
	optIn := map[string]string{AutoMigrateLabel: AutoMigrateValue}
	servedV1Beta1 := []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1beta1", Storage: true, Served: true}}

	tests := []struct {
		name    string
		crd     *apiextensionsv1.CustomResourceDefinition // nil ⇒ CRD absent, expect IsNotFound branch
		crdName string
	}{
		{
			name:    "CRD not found returns nil without error",
			crd:     nil,
			crdName: "missing.toolhive.stacklok.dev",
		},
		{
			name: "foreign group is skipped",
			crd: reconcileCRD("widgets.example.com", "example.com", optIn,
				[]apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1", Storage: true, Served: true}},
				[]string{"v1alpha1", "v1"}),
			crdName: "widgets.example.com",
		},
		{
			name:    "missing opt-in label is skipped",
			crd:     reconcileCRD(mcpName, ToolhiveGroup, nil, servedV1Beta1, []string{"v1alpha1", "v1beta1"}),
			crdName: mcpName,
		},
		{
			// Pathological CRD — every version has Storage: false. The apiserver
			// would normally reject this at CRD-create time, so envtest can't
			// reach this branch. Reconcile must return nil rather than panic.
			name: "no storage version is skipped",
			crd: reconcileCRD(mcpName, ToolhiveGroup, optIn,
				[]apiextensionsv1.CustomResourceDefinitionVersion{
					{Name: "v1alpha1", Storage: false, Served: true},
					{Name: "v1beta1", Storage: false, Served: true},
				},
				[]string{"v1alpha1", "v1beta1"}),
			crdName: mcpName,
		},
		{
			name:    "already-clean storedVersions returns early",
			crd:     reconcileCRD(mcpName, ToolhiveGroup, optIn, servedV1Beta1, []string{"v1beta1"}),
			crdName: mcpName,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var initial []client.Object
			if tc.crd != nil {
				initial = []client.Object{tc.crd}
			}
			r := buildFakeReconciler(t, initial, nil)
			res, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: apitypes.NamespacedName{Name: tc.crdName},
			})
			require.NoError(t, err, "early-return branch must not surface as a reconcile error")
			assert.Equal(t, ctrl.Result{}, res)
		})
	}
}

// isManagedCRD

func TestIsManagedCRD(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		group  string
		labels map[string]string
		want   bool
	}{
		{"toolhive group with opt-in label is managed", ToolhiveGroup, map[string]string{AutoMigrateLabel: AutoMigrateValue}, true},
		{"toolhive group with wrong label value is not managed", ToolhiveGroup, map[string]string{AutoMigrateLabel: "false"}, false},
		{"toolhive group with unrelated label is not managed", ToolhiveGroup, map[string]string{"unrelated.example.com/key": "true"}, false},
		{"toolhive group with nil labels map is not managed", ToolhiveGroup, nil, false},
		{"non-toolhive group with opt-in label is not managed", "example.com", map[string]string{AutoMigrateLabel: AutoMigrateValue}, false},
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

// isToolhiveCRDName

func TestIsToolhiveCRDName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		crdName  string
		expected bool
	}{
		{"well-formed toolhive CRD name matches", "mcpservers.toolhive.stacklok.dev", true},
		{"empty string does not match", "", false},
		{"group string with no plural prefix does not match", "toolhive.stacklok.dev", false},
		{"group as bare suffix without leading dot does not match", "footoolhive.stacklok.dev", false},
		{"foreign group does not match", "widgets.example.com", false},
		{"toolhive suffix in the middle of the name does not match", "foo.toolhive.stacklok.dev.example.com", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, isToolhiveCRDName(tc.crdName))
		})
	}
}

// findStorageVersion

func TestFindStorageVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		versions []apiextensionsv1.CustomResourceDefinitionVersion
		wantName string
		wantOK   bool
	}{
		{
			"single storage version returns its name",
			[]apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1", Storage: true}},
			"v1", true,
		},
		{
			"multiple versions returns the storage entry",
			[]apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1alpha1", Storage: false},
				{Name: "v1beta1", Storage: true},
				{Name: "v1beta2", Storage: false},
			},
			"v1beta1", true,
		},
		{
			"no storage version returns empty and false",
			[]apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1alpha1", Storage: false},
				{Name: "v1beta1", Storage: false},
			},
			"", false,
		},
		{"empty versions list returns empty and false", nil, "", false},
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

// isMigrationNeeded

func TestIsMigrationNeeded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		storedVersions []string
		storageVersion string
		want           bool
	}{
		{"single matching entry is not needed", []string{"v1beta1"}, "v1beta1", false},
		{"single mismatching entry is needed", []string{"v1alpha1"}, "v1beta1", true},
		{"two entries including target is needed", []string{"v1alpha1", "v1beta1"}, "v1beta1", true},
		{"empty list is needed", nil, "v1beta1", true},
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

// Shared helpers for restoreCRs / restoreOne / patchStoredVersions tests.

const (
	testCRGroup    = ToolhiveGroup
	testCRVersion  = "v1beta1"
	testCRKind     = "TestKind"
	testCRListKind = "TestKindList"
	testCRPlural   = "testkinds"
	testCRSingular = "testkind"
	testCRDName    = testCRPlural + "." + testCRGroup
)

// testCRGVK is the singular GVK for the synthetic CR kind.
func testCRGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: testCRGroup, Version: testCRVersion, Kind: testCRKind}
}

// schemeForCRD builds a runtime scheme with apiextensions/v1 plus the
// synthetic CR's singular and list kinds registered as unstructured (the
// fake client requires the kind to be in the scheme on List/Get/Update).
func schemeForCRD(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(scheme))
	gvk := testCRGVK()
	listGVK := gvk
	listGVK.Kind = testCRListKind
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
	return scheme
}

// makeTestCRD builds a CRD wired to the synthetic CR kind with the supplied
// status.storedVersions and the opt-in label set.
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

// makeTestCR returns an unstructured CR with the synthetic GVK. UID is
// derived from name so tests can assert cache state by name.
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
		Recorder:  noopEventRecorder{},
	}
	r.ensureInitialized()
	return r
}

// restoreOne

func TestRestoreOne_HappyPath(t *testing.T) {
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

func TestRestoreOne_PropagatesErrors(t *testing.T) {
	t.Parallel()

	gr := schema.GroupResource{Group: testCRGroup, Resource: testCRPlural}

	// Per-row: when crPresent is false, the CR is absent from the fake store
	// and Get returns NotFound (no interceptor needed). When crPresent is true,
	// updateErr is returned from an Update interceptor.
	tests := []struct {
		name      string
		crPresent bool
		updateErr error
		check     func(t *testing.T, err error)
	}{
		{
			name:      "Get NotFound propagates",
			crPresent: false,
			check: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.True(t, apierrors.IsNotFound(err),
					"Get NotFound must propagate verbatim so restoreCRs can classify it")
			},
		},
		{
			name:      "Update Conflict propagates",
			crPresent: true,
			updateErr: apierrors.NewConflict(gr, "obj-1", errors.New("injected conflict")),
			check: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.True(t, apierrors.IsConflict(err),
					"Update Conflict must propagate verbatim so restoreCRs can classify it")
			},
		},
		{
			name:      "Update generic error propagates",
			crPresent: true,
			updateErr: errors.New("injected generic failure"),
			check: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.False(t, apierrors.IsConflict(err))
				assert.False(t, apierrors.IsNotFound(err))
				assert.Contains(t, err.Error(), "injected generic failure")
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := makeTestCR("default", "obj-1")
			var initial []client.Object
			if tc.crPresent {
				initial = []client.Object{target}
			}
			var funcs *interceptor.Funcs
			if tc.updateErr != nil {
				updateErr := tc.updateErr
				funcs = &interceptor.Funcs{
					Update: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.UpdateOption) error {
						return updateErr
					},
				}
			}
			r := buildFakeReconciler(t, initial, funcs)
			_, err := r.restoreOne(context.Background(), testCRGVK(), target)
			tc.check(t, err)
		})
	}
}

// restoreCRs

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

// TestRestoreCRs_ErrorClassification covers the per-CR error classification
// logic: IsNotFound is silently skipped, IsConflict is counted and surfaces
// as the conflict sentinel iff no other errors occurred, generic errors are
// aggregated, and mixed conflict + generic errors surface the aggregate
// rather than the sentinel.
func TestRestoreCRs_ErrorClassification(t *testing.T) {
	t.Parallel()

	gr := schema.GroupResource{Group: testCRGroup, Resource: testCRPlural}

	// Per-row: getErrs/updateErrs map a CR name → the error its Get/Update
	// interceptor must return (other names fall through to the fake client).
	// check is run against the (err, reconciler) pair after restoreCRs.
	tests := []struct {
		name       string
		crNames    []string
		getErrs    map[string]error
		updateErrs map[string]error
		check      func(t *testing.T, err error, r *StorageVersionMigratorReconciler)
	}{
		{
			name:    "per-CR Get NotFound is silently skipped",
			crNames: []string{"obj-a", "obj-b"},
			getErrs: map[string]error{"obj-a": apierrors.NewNotFound(gr, "obj-a")},
			check: func(t *testing.T, err error, r *StorageVersionMigratorReconciler) {
				require.NoError(t, err, "IsNotFound on a per-CR Get must not bubble up")
				// Cache must contain only obj-b — NotFound must skip the cache add.
				assert.Len(t, r.cache.entries, 1, "only the surviving CR may be cached")
			},
		},
		{
			name:       "Conflict counted and sentinel returned",
			crNames:    []string{"obj-a", "obj-b"},
			updateErrs: map[string]error{"obj-a": apierrors.NewConflict(gr, "obj-a", errors.New("injected"))},
			check: func(t *testing.T, err error, _ *StorageVersionMigratorReconciler) {
				require.Error(t, err, "a swallowed Conflict must surface as a function-level error")
				assert.ErrorIs(t, err, errMigrationRetriedDueToConflicts,
					"the sentinel must be returned so the caller knows storedVersions is unsafe to trim")
			},
		},
		{
			name:       "generic error is aggregated",
			crNames:    []string{"obj-a"},
			updateErrs: map[string]error{"obj-a": errors.New("injected generic update failure")},
			check: func(t *testing.T, err error, _ *StorageVersionMigratorReconciler) {
				require.Error(t, err)
				assert.NotErrorIs(t, err, errMigrationRetriedDueToConflicts,
					"a generic error must NOT be misclassified as the conflict sentinel")
				assert.Contains(t, err.Error(), "injected generic update failure")
				assert.Contains(t, err.Error(), "obj-a", "aggregated error should name the failing CR")
			},
		},
		{
			name:    "conflicts plus generic errors returns aggregate, not sentinel",
			crNames: []string{"obj-conflict", "obj-failure"},
			updateErrs: map[string]error{
				"obj-conflict": apierrors.NewConflict(gr, "obj-conflict", errors.New("injected conflict")),
				"obj-failure":  errors.New("injected generic failure"),
			},
			check: func(t *testing.T, err error, _ *StorageVersionMigratorReconciler) {
				require.Error(t, err)
				// When there is at least one non-conflict error the aggregate
				// wins — the sentinel is only meaningful in the conflicts-only
				// case.
				assert.NotErrorIs(t, err, errMigrationRetriedDueToConflicts,
					"mixed conflicts+errors must return the aggregate, not the conflict sentinel")
				assert.Contains(t, err.Error(), "injected generic failure")
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			crd := makeTestCRD([]string{"v1alpha1", testCRVersion})
			objs := []client.Object{crd}
			for _, n := range tc.crNames {
				objs = append(objs, makeTestCR("default", n))
			}
			funcs := &interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if e, ok := tc.getErrs[key.Name]; ok {
						return e
					}
					return c.Get(ctx, key, obj, opts...)
				},
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if e, ok := tc.updateErrs[obj.GetName()]; ok {
						return e
					}
					return c.Update(ctx, obj, opts...)
				},
			}
			r := buildFakeReconciler(t, objs, funcs)
			err := r.restoreCRs(context.Background(), crd, testCRVersion)
			tc.check(t, err, r)
		})
	}
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

// patchStoredVersions

// TestPatchStoredVersions_SuccessAssertsAllProperties exercises the success
// path and verifies three orthogonal properties in one run:
//   - storedVersions is trimmed to exactly [storageVersion] in the fake store,
//   - the /status subresource endpoint is hit (and the main-resource Patch is NOT),
//   - the patch body carries resourceVersion as a precondition (the marker that
//     MergeFromWithOptimisticLock is in effect; plain MergeFrom omits it).
func TestPatchStoredVersions_SuccessAssertsAllProperties(t *testing.T) {
	t.Parallel()
	crd := makeTestCRD([]string{"v1alpha1", testCRVersion})

	var mainResourcePatchCalls int32
	var statusSubresourcePatchCalls int32
	var capturedPatchBody []byte

	funcs := &interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			atomic.AddInt32(&mainResourcePatchCalls, 1)
			return c.Patch(ctx, obj, patch, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if subResourceName == "status" {
				atomic.AddInt32(&statusSubresourcePatchCalls, 1)
			}
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

	// Subresource routing.
	assert.Equal(t, int32(0), atomic.LoadInt32(&mainResourcePatchCalls),
		"patchStoredVersions must NOT hit the main-resource Patch endpoint")
	assert.Equal(t, int32(1), atomic.LoadInt32(&statusSubresourcePatchCalls),
		"patchStoredVersions must hit the /status subresource exactly once")

	// Patch body carries the optimistic-lock precondition and the trimmed list.
	require.NotEmpty(t, capturedPatchBody, "interceptor never captured the patch body")
	body := string(capturedPatchBody)
	assert.Contains(t, body, `"resourceVersion":"1"`,
		"optimistic-lock patches must include the source resourceVersion as a precondition")
	assert.Contains(t, body, `"storedVersions":["`+testCRVersion+`"]`,
		"patch body must overwrite storedVersions to exactly [storageVersion]")

	// storedVersions actually trimmed in the fake store.
	live := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, r.Get(context.Background(), client.ObjectKey{Name: crd.Name}, live))
	assert.Equal(t, []string{testCRVersion}, live.Status.StoredVersions)
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

// Test doubles used by the orchestration tests above

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

// noopEventRecorder satisfies events.EventRecorder for tests that don't
// assert on emitted events.
type noopEventRecorder struct{}

func (noopEventRecorder) Eventf(_ runtime.Object, _ runtime.Object, _, _, _, _ string, _ ...any) {
}
