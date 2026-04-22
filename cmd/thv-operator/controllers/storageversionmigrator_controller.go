// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
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

// StorageVersionMigratorFieldManager is the Server-Side Apply field manager
// name used for all re-store writes. It is part of the public contract for
// the controller — do not change it across releases, as SSA ownership is
// permanent.
const StorageVersionMigratorFieldManager = "thv-storage-version-migrator"

// EventReasonMigrationSucceeded and EventReasonMigrationFailed are the event
// reasons emitted on the owning CRD when a migration completes or fails.
const (
	EventReasonMigrationSucceeded = "StorageVersionMigrationSucceeded"
	EventReasonMigrationFailed    = "StorageVersionMigrationFailed"
)

const (
	defaultMigrationCacheTTL = 1 * time.Hour
	defaultListPageSize      = 500
)

//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions/status,verbs=update;patch
//+kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=*,verbs=get;list;patch
//+kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=*/status,verbs=patch

// StorageVersionMigratorReconciler reconciles CustomResourceDefinition objects
// in the toolhive.stacklok.dev group that carry the opt-in
// AutoMigrateLabel=AutoMigrateValue. For each such CRD it re-stores every CR
// at the current storage version by issuing a metadata-only Server-Side Apply
// against the /status subresource (to avoid admission webhooks), then patches
// CRD.status.storedVersions down to [<currentStorageVersion>] so a future
// release can drop deprecated versions from spec.versions without orphaning
// etcd objects.
//
// Enabled by default. Opt out operator-wide via
// operator.features.storageVersionMigrator (ENABLE_STORAGE_VERSION_MIGRATOR=false)
// for admins who prefer to run kube-storage-version-migrator externally.
// Per-kind escape hatch: remove the label from the CRD (emergency only — will
// be re-applied by GitOps / helm upgrade).
type StorageVersionMigratorReconciler struct {
	client.Client                      // cached reads for CRs (unused in this reconciler, kept for kubebuilder convention)
	APIReader     client.Reader        // live reads for CRDs (bypasses informer)
	Scheme        *runtime.Scheme      // kubebuilder reconciler convention
	Recorder      record.EventRecorder // MigrationSucceeded / MigrationFailed events on the CRD
	PageSize      int64                // overrideable for tests; defaults to defaultListPageSize
	cache         *migrationCache
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
func (r *StorageVersionMigratorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.ensureInitialized()

	labelSelector, err := labels.Parse(AutoMigrateLabel + "=" + AutoMigrateValue)
	if err != nil {
		return fmt.Errorf("parse label selector: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
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
		Complete(r)
}

// ------------------------------------------------------------------
// Private implementation below.
// ------------------------------------------------------------------

func (r *StorageVersionMigratorReconciler) ensureInitialized() {
	if r.PageSize == 0 {
		r.PageSize = defaultListPageSize
	}
	if r.cache == nil {
		r.cache = newMigrationCache(defaultMigrationCacheTTL)
	}
}

// restoreCRs lists all CRs of the CRD's served kind (served version = storageVersion)
// and issues a metadata-only Server-Side Apply against /status for each one,
// forcing the API server to re-encode the object at the current storage version.
//
// Swallowed per-CR errors: IsNotFound (object deleted between list and patch)
// and IsConflict (object updated elsewhere — storage is already fresh).
// All other errors are aggregated and returned.
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

	hasStatusSubresource := crdHasStatusSubresource(crd, storageVersion)

	listGVK := gvk
	listGVK.Kind = crd.Spec.Names.ListKind

	var errs []error
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
			if err := r.restoreOne(ctx, gvk, u, hasStatusSubresource); err != nil {
				if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
					logger.V(1).Info("skip CR — deleted or updated elsewhere",
						"object", client.ObjectKeyFromObject(u), "err", err)
					return nil
				}
				errs = append(errs, fmt.Errorf("re-store %s/%s: %w",
					u.GetNamespace(), u.GetName(), err))
				return nil
			}
			r.cache.add(crd.Name, u.GetUID(), u.GetResourceVersion())
			return nil
		}); err != nil {
			errs = append(errs, err)
		}

		continueToken = list.GetContinue()
		if continueToken == "" {
			break
		}
	}

	return kerrors.NewAggregate(errs)
}

// restoreOne issues a metadata-only SSA for a single CR. Prefers the /status
// subresource (to bypass validating/mutating webhooks), falls back to the main
// resource if the CRD has no status subresource.
func (r *StorageVersionMigratorReconciler) restoreOne(
	ctx context.Context,
	gvk schema.GroupVersionKind,
	original *unstructured.Unstructured,
	hasStatusSubresource bool,
) error {
	patch := &unstructured.Unstructured{}
	patch.SetGroupVersionKind(gvk)
	patch.SetName(original.GetName())
	patch.SetNamespace(original.GetNamespace())
	patch.SetUID(original.GetUID())                         // UID mismatch → typed conflict on races
	patch.SetResourceVersion(original.GetResourceVersion()) // RV mismatch → typed conflict on races

	applyConfig := client.ApplyConfigurationFromUnstructured(patch)
	if hasStatusSubresource {
		return r.Client.Status().Apply(ctx, applyConfig,
			client.FieldOwner(StorageVersionMigratorFieldManager),
			client.ForceOwnership,
		)
	}
	return r.Apply(ctx, applyConfig,
		client.FieldOwner(StorageVersionMigratorFieldManager),
		client.ForceOwnership,
	)
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

// isMigrationNeeded returns true if status.storedVersions contains any entry
// other than the current storage version. If storedVersions equals exactly
// [storageVersion] AND only one version is served, no work is needed — this
// second condition avoids spurious reconciles once a deprecated version has
// been fully removed from spec.versions.
func isMigrationNeeded(
	crd *apiextensionsv1.CustomResourceDefinition,
	storageVersion string,
) bool {
	stored := crd.Status.StoredVersions
	if len(stored) != 1 || stored[0] != storageVersion {
		return true
	}
	servedCount := 0
	for _, v := range crd.Spec.Versions {
		if v.Served {
			servedCount++
		}
	}
	return servedCount > 1
}

// crdHasStatusSubresource returns true if the CRD's version declares a status
// subresource. Missing → fall back to main-resource SSA.
func crdHasStatusSubresource(
	crd *apiextensionsv1.CustomResourceDefinition,
	version string,
) bool {
	for _, v := range crd.Spec.Versions {
		if v.Name != version {
			continue
		}
		return v.Subresources != nil && v.Subresources.Status != nil
	}
	return false
}

// ------------------------------------------------------------------
// migrationCache: short-lived de-duplication of re-store writes.
// ------------------------------------------------------------------

// migrationCache records successfully-migrated (UID, resourceVersion) pairs
// so subsequent reconciles within the TTL window skip already-fresh objects.
// It is a correctness optimization only — a cache miss simply issues a
// redundant (but harmless) SSA.
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

func (*migrationCache) key(crdName string, uid apitypes.UID) string {
	return crdName + "|" + string(uid)
}
