// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// Public contract for the StorageVersionMigrator controller.

// AutoMigrateLabel identifies CRDs that opt in to storage-version migration.
// Applied via a kubebuilder marker on each root type in api/v1beta1/.
const AutoMigrateLabel = "toolhive.stacklok.dev/auto-migrate-storage-version"

// AutoMigrateValue is the label value that enables migration for a CRD.
const AutoMigrateValue = "true"

// ToolhiveGroup is the API group the controller is scoped to (belt-and-braces
// filter in addition to the opt-in label).
const ToolhiveGroup = "toolhive.stacklok.dev"

// EventReasonMigrationSucceeded and EventReasonMigrationFailed are the event
// reasons emitted on the owning CRD when a migration completes or fails.
const (
	EventReasonMigrationSucceeded = "StorageVersionMigrationSucceeded"
	EventReasonMigrationFailed    = "StorageVersionMigrationFailed"
)

const (
	defaultMigrationCacheTTL = 1 * time.Hour
	defaultListPageSize      = 500
	defaultCacheGCInterval   = 10 * time.Minute
)

// errMigrationRetriedDueToConflicts is returned by restoreCRs when at least one
// CR re-store hit a typed Conflict (and no other errors occurred). The caller
// must NOT trim CRD.status.storedVersions in this case: the post-conflict state
// of the affected object is unverified, so reasoning about whether the storage
// re-encode actually happened is unsafe. The next reconcile retries cleanly.
var errMigrationRetriedDueToConflicts = errors.New(
	"storage version migration retried due to concurrent writes; storedVersions left unchanged")

// The wildcard CR RBAC below is intentional. The set of opted-in CRDs isn't
// known at codegen time — it's a per-CRD runtime label decision — so the
// kubebuilder marker can't enumerate kinds. The runtime gate is the
// isManagedCRD check inside Reconcile, which requires both the toolhive
// API group AND the opt-in label. Wildcard RBAC plus isManagedCRD form the
// defence in depth: RBAC bounds the controller to a single API group, and
// the label gate further restricts it to opted-in CRDs.

//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions/status,verbs=update;patch
//+kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=*,verbs=get;list;update
//+kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=*/status,verbs=update

// StorageVersionMigratorReconciler reconciles CustomResourceDefinition objects
// in the toolhive.stacklok.dev group that carry the opt-in
// AutoMigrateLabel=AutoMigrateValue. For each such CRD it re-stores every CR
// at the current storage version by doing a Get + Update on the live object
// while toggling the MigrationTimestampAnnotation to force a real content
// diff. The annotation toggle is required because the apiserver elides no-op
// writes (an Update with bytes equal to what's already stored does not bump
// resourceVersion and does not re-encode) — without a real diff the migration
// would be a hollow operation. After all CRs have been re-stored it patches
// CRD.status.storedVersions down to [<currentStorageVersion>] so a future
// release can drop deprecated versions from spec.versions without orphaning
// etcd objects.
//
// Webhook interaction: the Update goes through the main resource, so
// validating/mutating admission webhooks on the kind DO see this write.
// Webhooks that need to ignore migration-only writes should branch on the
// presence of MigrationTimestampAnnotation in the diff.
//
// Enabled by default. Opt out operator-wide via
// operator.features.storageVersionMigrator (ENABLE_STORAGE_VERSION_MIGRATOR=false)
// for admins who prefer to run kube-storage-version-migrator externally.
// Per-kind escape hatch: remove the label from the CRD (emergency only — will
// be re-applied by GitOps / helm upgrade).
type StorageVersionMigratorReconciler struct {
	// used for CR Update writes and the CRD /status storedVersions patch;
	// reads go through APIReader to bypass the informer cache.
	client.Client
	APIReader       client.Reader        // live reads for CRDs and CR list pages (bypasses informer)
	Scheme          *runtime.Scheme      // kubebuilder reconciler convention
	Recorder        record.EventRecorder // MigrationSucceeded / MigrationFailed events on the CRD
	PageSize        int64                // overrideable for tests; defaults to defaultListPageSize
	CacheGCInterval time.Duration        // overrideable for tests; defaults to defaultCacheGCInterval
	cache           *migrationCache
}

// Reconcile runs for each opted-in toolhive.stacklok.dev CRD event. See the
// package-level docs on StorageVersionMigratorReconciler for the full flow.
// Returns a non-nil error to trigger exponential backoff; the CRD watch
// re-enqueues on any status change, so explicit requeue intervals are not used.
func (r *StorageVersionMigratorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("crd", req.Name)

	r.ensureInitialized()

	// Live-read the CRD. Informer cache may lag label or storedVersions updates.
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := r.APIReader.Get(ctx, req.NamespacedName, crd); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get CRD %s: %w", req.Name, err)
	}

	// Re-verify the filter against live state; watch predicate could have
	// fired on stale informer data.
	if !isManagedCRD(crd) {
		return ctrl.Result{}, nil
	}

	storageVersion, ok := findStorageVersion(crd)
	if !ok {
		// CRDs without a storage version are malformed from our perspective;
		// log and skip rather than fail (the API server would have rejected
		// a CRD without a storage version, so this is unreachable in practice).
		logger.Info("CRD has no storage version, skipping", "spec.versions", crd.Spec.Versions)
		return ctrl.Result{}, nil
	}

	if !isMigrationNeeded(crd, storageVersion) {
		return ctrl.Result{}, nil
	}

	logger.Info("migrating storage versions",
		"storageVersion", storageVersion,
		"storedVersions", crd.Status.StoredVersions,
	)

	if err := r.restoreCRs(ctx, crd, storageVersion); err != nil {
		r.Recorder.Eventf(crd, "Warning", EventReasonMigrationFailed,
			"storage version migration failed: %v", err)
		return ctrl.Result{}, fmt.Errorf("re-store CRs for %s: %w", crd.Name, err)
	}

	if err := r.patchStoredVersions(ctx, crd, storageVersion); err != nil {
		r.Recorder.Eventf(crd, "Warning", EventReasonMigrationFailed,
			"storedVersions patch failed: %v", err)
		return ctrl.Result{}, fmt.Errorf("patch storedVersions for %s: %w", crd.Name, err)
	}

	r.Recorder.Eventf(crd, "Normal", EventReasonMigrationSucceeded,
		"storage version migrated to %s", storageVersion)
	logger.Info("storage version migration complete", "storageVersion", storageVersion)
	return ctrl.Result{}, nil
}

// SetupWithManager wires the reconciler to watch CRDs using PartialObjectMetadata
// (no full-object cache), filtered on the opt-in label and the toolhive.stacklok.dev
// group. The filter is evaluated twice — once on informer events here, and again
// inside Reconcile after the live APIReader read — because label removals can
// briefly race the informer.
//
// It also registers a Runnable that periodically sweeps expired entries from
// the migration cache so deleted CRs (whose UIDs never recur in subsequent
// list pages and therefore never trigger lazy eviction in has()) don't grow
// the map without bound on long-running operators with high CR churn.
func (r *StorageVersionMigratorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.ensureInitialized()

	labelSelector, err := labels.Parse(AutoMigrateLabel + "=" + AutoMigrateValue)
	if err != nil {
		return fmt.Errorf("parse label selector: %w", err)
	}

	if err := ctrl.NewControllerManagedBy(mgr).
		Named("storageversionmigrator").
		For(
			&apiextensionsv1.CustomResourceDefinition{},
			builder.OnlyMetadata,
			builder.WithPredicates(
				predicate.NewPredicateFuncs(func(obj client.Object) bool {
					return labelSelector.Matches(labels.Set(obj.GetLabels())) &&
						isToolhiveCRDName(obj.GetName())
				}),
				predicate.ResourceVersionChangedPredicate{},
			),
		).
		Complete(r); err != nil {
		return err
	}

	// Periodic cache GC. Registered after Complete so the controller is fully
	// wired when the runnable starts.
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		ticker := time.NewTicker(r.CacheGCInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				r.cache.gc()
			}
		}
	}))
}

// ------------------------------------------------------------------
// Private implementation below.
// ------------------------------------------------------------------

func (r *StorageVersionMigratorReconciler) ensureInitialized() {
	if r.PageSize == 0 {
		r.PageSize = defaultListPageSize
	}
	if r.CacheGCInterval == 0 {
		r.CacheGCInterval = defaultCacheGCInterval
	}
	if r.cache == nil {
		r.cache = newMigrationCache(defaultMigrationCacheTTL)
	}
}

// restoreCRs lists all CRs of the CRD's served kind (served version = storageVersion)
// and issues a main-resource Update on each one, forcing the apiserver to
// re-encode the object at the current storage version.
//
// Per-CR error handling:
//   - IsNotFound: silently skipped (object deleted between list and update —
//     it can't be at the old storage version anymore).
//   - IsConflict: silently skipped at the per-CR level, but a function-level
//     counter is incremented. After the loop, if any conflicts occurred and no
//     other errors did, errMigrationRetriedDueToConflicts is returned so the
//     caller leaves storedVersions untouched (the post-conflict state of the
//     conflicting object is unverified).
//   - All other errors are aggregated and returned.
func (r *StorageVersionMigratorReconciler) restoreCRs(
	ctx context.Context,
	crd *apiextensionsv1.CustomResourceDefinition,
	storageVersion string,
) error {
	logger := log.FromContext(ctx)
	gvk := schema.GroupVersionKind{
		Group:   crd.Spec.Group,
		Version: storageVersion,
		Kind:    crd.Spec.Names.Kind,
	}

	listGVK := gvk
	listGVK.Kind = crd.Spec.Names.ListKind

	var errs []error
	conflicts := 0
	var continueToken string
	for {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(listGVK)
		listOpts := []client.ListOption{client.Limit(r.PageSize)}
		if continueToken != "" {
			listOpts = append(listOpts, client.Continue(continueToken))
		}
		if err := r.APIReader.List(ctx, list, listOpts...); err != nil {
			return fmt.Errorf("list %s: %w", listGVK.String(), err)
		}

		if err := meta.EachListItem(list, func(obj runtime.Object) error {
			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				errs = append(errs, fmt.Errorf("unexpected list item type %T", obj))
				return nil
			}
			if r.cache.has(crd.Name, u.GetUID(), u.GetResourceVersion()) {
				return nil
			}
			restored, err := r.restoreOne(ctx, gvk, u)
			if err != nil {
				switch {
				case apierrors.IsNotFound(err):
					logger.V(1).Info("skip CR — deleted",
						"object", client.ObjectKeyFromObject(u), "err", err)
				case apierrors.IsConflict(err):
					conflicts++
					logger.V(1).Info("skip CR — concurrent write conflict",
						"object", client.ObjectKeyFromObject(u), "err", err)
				default:
					errs = append(errs, fmt.Errorf("re-store %s/%s: %w",
						u.GetNamespace(), u.GetName(), err))
				}
				return nil
			}
			r.cache.add(crd.Name, restored.GetUID(), restored.GetResourceVersion())
			return nil
		}); err != nil {
			errs = append(errs, err)
		}

		continueToken = list.GetContinue()
		if continueToken == "" {
			break
		}
	}

	if len(errs) == 0 && conflicts > 0 {
		return errMigrationRetriedDueToConflicts
	}
	return kerrors.NewAggregate(errs)
}

// MigrationTimestampAnnotation is set on each CR by the migrator to force a
// real content diff on Update. The apiserver's storage layer elides no-op
// writes (an Update with bytes equal to what's already in etcd does not
// re-encode and does not bump resourceVersion), so a Get + Update on the live
// object is not enough on its own — there must be at least one byte of
// difference. Mutating this annotation guarantees the apiserver re-writes the
// object, which is exactly the storage-version re-encode we need. The value
// is the RFC3339Nano timestamp of the most recent successful migration.
//
// This matches the upstream kube-storage-version-migrator's approach (which
// also uses an annotation toggle) and is part of the public contract: do not
// remove or rename this constant across releases without a migration plan.
const MigrationTimestampAnnotation = "toolhive.stacklok.dev/storage-version-migrated-at"

// restoreOne issues a Get + Update on the live CR, mutating the migration
// timestamp annotation to force a real content diff so the apiserver actually
// re-encodes the object at the current storage version. The Update goes
// through the main resource (not /status), so any validating/mutating
// admission webhooks on the kind WILL see this write. CRDs that need to avoid
// webhook side effects from this controller should configure their webhooks
// to ignore writes that change only this annotation. Returns the live object
// after the update so the caller can record its post-update resourceVersion
// in the cache.
func (r *StorageVersionMigratorReconciler) restoreOne(
	ctx context.Context,
	gvk schema.GroupVersionKind,
	original *unstructured.Unstructured,
) (*unstructured.Unstructured, error) {
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(gvk)
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(original), live); err != nil {
		// IsNotFound is propagated to the caller, which handles it.
		return nil, err
	}
	annotations := live.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[MigrationTimestampAnnotation] = time.Now().UTC().Format(time.RFC3339Nano)
	live.SetAnnotations(annotations)
	if err := r.Update(ctx, live); err != nil {
		return nil, err
	}
	return live, nil
}

// patchStoredVersions overwrites CRD.status.storedVersions to exactly
// [storageVersion], using an optimistic lock on the CRD's resourceVersion so
// a concurrent API-server write rejects the patch and triggers a requeue.
func (r *StorageVersionMigratorReconciler) patchStoredVersions(
	ctx context.Context,
	crd *apiextensionsv1.CustomResourceDefinition,
	storageVersion string,
) error {
	original := crd.DeepCopy()
	crd.Status.StoredVersions = []string{storageVersion}
	return r.Client.Status().Patch(ctx, crd,
		client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}))
}

// isManagedCRD returns true if a CRD is opted in to migration: the group matches
// toolhive.stacklok.dev and the opt-in label is set to the expected value.
func isManagedCRD(crd *apiextensionsv1.CustomResourceDefinition) bool {
	if crd.Spec.Group != ToolhiveGroup {
		return false
	}
	return crd.GetLabels()[AutoMigrateLabel] == AutoMigrateValue
}

// isToolhiveCRDName checks whether a CRD name is of the form <plural>.toolhive.stacklok.dev,
// which is sufficient to filter at watch time. Reconcile re-verifies via the live CRD.
func isToolhiveCRDName(name string) bool {
	return strings.HasSuffix(name, "."+ToolhiveGroup)
}

// findStorageVersion returns the single version marked storage=true in the CRD spec.
func findStorageVersion(crd *apiextensionsv1.CustomResourceDefinition) (string, bool) {
	for _, v := range crd.Spec.Versions {
		if v.Storage {
			return v.Name, true
		}
	}
	return "", false
}

// isMigrationNeeded returns true iff status.storedVersions is anything other
// than exactly [storageVersion]. The set of served versions does not affect
// this check — under spec.conversion.strategy=None with identical schemas,
// normal writers cannot reintroduce stale versions to storedVersions, so a
// defensive re-scan based on servedCount has no scenario to defend against.
func isMigrationNeeded(
	crd *apiextensionsv1.CustomResourceDefinition,
	storageVersion string,
) bool {
	stored := crd.Status.StoredVersions
	return len(stored) != 1 || stored[0] != storageVersion
}

// ------------------------------------------------------------------
// migrationCache: short-lived de-duplication of re-store writes.
// ------------------------------------------------------------------

// migrationCache records successfully-migrated (UID, resourceVersion) pairs
// so subsequent reconciles within the TTL window skip already-fresh objects.
// It is a correctness optimization only — a cache miss simply issues a
// redundant (but harmless) Update.
//
// Eviction: lazy on lookup in has(), plus a periodic sweep via gc() driven
// from a manager.Runnable registered in SetupWithManager. The periodic sweep
// is required because lookups never recur for deleted CRs, so without it
// their entries would persist forever.
type migrationCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	ttl     time.Duration
	now     func() time.Time
}

type cacheEntry struct {
	resourceVersion string
	expiresAt       time.Time
}

func newMigrationCache(ttl time.Duration) *migrationCache {
	return &migrationCache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
		now:     time.Now,
	}
}

func (c *migrationCache) has(crdName string, uid apitypes.UID, resourceVersion string) bool {
	key := c.key(crdName, uid)
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return false
	}
	if c.now().After(entry.expiresAt) {
		delete(c.entries, key)
		return false
	}
	return entry.resourceVersion == resourceVersion
}

func (c *migrationCache) add(crdName string, uid apitypes.UID, resourceVersion string) {
	key := c.key(crdName, uid)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{
		resourceVersion: resourceVersion,
		expiresAt:       c.now().Add(c.ttl),
	}
}

// gc evicts every expired entry from the cache. Called from a periodic
// manager.Runnable so entries for deleted CRs (whose UIDs never recur in
// subsequent list pages) don't accumulate without bound.
func (c *migrationCache) gc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, e := range c.entries {
		if now.After(e.expiresAt) {
			delete(c.entries, k)
		}
	}
}

func (*migrationCache) key(crdName string, uid apitypes.UID) string {
	return crdName + "|" + string(uid)
}
