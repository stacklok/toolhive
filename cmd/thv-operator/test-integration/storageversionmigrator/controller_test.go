// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storageversionmigrator

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive/cmd/thv-operator/controllers"
)

const (
	toolhiveGroup = controllers.ToolhiveGroup
	migrateLabel  = controllers.AutoMigrateLabel
	migrateValue  = controllers.AutoMigrateValue
)

// crdSpec describes a test CRD fixture.
type crdSpec struct {
	Name              string
	Group             string
	Kind              string
	ListKind          string
	Plural            string
	Singular          string
	Versions          []versionSpec
	Labelled          bool
	HasStatusOnStored bool
}

type versionSpec struct {
	Name    string
	Served  bool
	Storage bool
}

func buildCRD(s crdSpec) *apiextensionsv1.CustomResourceDefinition {
	versions := make([]apiextensionsv1.CustomResourceDefinitionVersion, 0, len(s.Versions))
	for _, v := range s.Versions {
		cdv := apiextensionsv1.CustomResourceDefinitionVersion{
			Name:    v.Name,
			Served:  v.Served,
			Storage: v.Storage,
			Schema: &apiextensionsv1.CustomResourceValidation{
				OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"spec": {
							Type:                   "object",
							XPreserveUnknownFields: ptrBool(true),
						},
						"status": {
							Type:                   "object",
							XPreserveUnknownFields: ptrBool(true),
						},
					},
				},
			},
		}
		if v.Storage && s.HasStatusOnStored {
			cdv.Subresources = &apiextensionsv1.CustomResourceSubresources{
				Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
			}
		}
		versions = append(versions, cdv)
	}

	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: s.Name},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: s.Group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     s.Kind,
				ListKind: s.ListKind,
				Plural:   s.Plural,
				Singular: s.Singular,
			},
			Scope:    apiextensionsv1.NamespaceScoped,
			Versions: versions,
		},
	}
	if s.Labelled {
		crd.Labels = map[string]string{migrateLabel: migrateValue}
	}
	return crd
}

func ptrBool(b bool) *bool { return &b }

// installCRD creates a CRD and waits for the apiserver to publish it so
// unstructured CR creates of that kind will succeed.
func installCRD(c crdSpec) {
	crd := buildCRD(c)
	Expect(k8sClient.Create(ctx, crd)).To(Succeed())

	Eventually(func() bool {
		live := &apiextensionsv1.CustomResourceDefinition{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: c.Name}, live); err != nil {
			return false
		}
		for _, cond := range live.Status.Conditions {
			if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
				return true
			}
		}
		return false
	}, time.Second*10, time.Millisecond*200).Should(BeTrue(), "CRD %s never became Established", c.Name)
}

func deleteCRD(name string) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, crd); err != nil {
		if apierrors.IsNotFound(err) {
			return
		}
		Fail(fmt.Sprintf("get CRD %s before delete: %v", name, err))
	}
	Expect(k8sClient.Delete(ctx, crd)).To(Succeed())
	Eventually(func() bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: name}, &apiextensionsv1.CustomResourceDefinition{}))
	}, time.Second*30, time.Millisecond*200).Should(BeTrue(), "CRD %s never fully deleted", name)
}

// setStoredVersions overwrites status.storedVersions, simulating a historical
// state where objects were stored at earlier versions.
func setStoredVersions(crdName string, versions []string) {
	Eventually(func() error {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: crdName}, crd); err != nil {
			return err
		}
		orig := crd.DeepCopy()
		crd.Status.StoredVersions = versions
		return k8sClient.Status().Patch(ctx, crd, client.MergeFrom(orig))
	}, time.Second*5, time.Millisecond*100).Should(Succeed())
}

func getStoredVersions(crdName string) []string {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: crdName}, crd)).To(Succeed())
	return append([]string{}, crd.Status.StoredVersions...)
}

// createCRs creates count CRs in the default namespace with the given kind
// and a name derived from basename. Returns the created objects so tests can
// assert on them post-reconcile.
func createCRs(gvk schema.GroupVersionKind, basename string, count int) []*unstructured.Unstructured {
	out := make([]*unstructured.Unstructured, 0, count)
	for i := 0; i < count; i++ {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		u.SetNamespace("default")
		u.SetName(fmt.Sprintf("%s-%d", basename, i))
		Expect(unstructured.SetNestedField(u.Object, "placeholder", "spec", "marker")).To(Succeed())
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		out = append(out, u)
	}
	return out
}

// Note on "did the re-store actually fire" verification:
//
// The controller now does a Get + Update on /status (or the main resource as a
// fallback). An unconditional Update always writes the object back to etcd, so
// the object's resourceVersion bumps on every successful re-store — that's the
// observable proof a re-encode happened. The HappyPath test snapshots each
// CR's resourceVersion before reconcile and asserts it has increased after
// reconcile completes.
//
// The pagination test additionally verifies the continue-token loop via a
// list-call counter, and the partial-failure test asserts storedVersions is
// not trimmed when any CR re-store fails.

// newReconciler constructs a StorageVersionMigratorReconciler for a single
// test. Every test has its own instance so the migration cache doesn't leak
// between tests and state is fully explicit.
func newReconciler() *controllers.StorageVersionMigratorReconciler {
	return &controllers.StorageVersionMigratorReconciler{
		Client:    k8sClient,
		APIReader: k8sClient,
		Scheme:    k8sClient.Scheme(),
		Recorder:  &noopRecorder{},
	}
}

// reconcile invokes the reconciler once for the given CRD and returns the
// result and error directly — tests assert on both.
func reconcile(r *controllers.StorageVersionMigratorReconciler, crdName string) (ctrl.Result, error) {
	return r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: crdName}})
}

var crdCounter int

func uniqueSuffix() string {
	crdCounter++
	return fmt.Sprintf("t%d", crdCounter)
}

// ------------------------------------------------------------------
// Tests
// ------------------------------------------------------------------

var _ = Describe("StorageVersionMigrator", func() {
	Describe("Reconcile", func() {

		It("is a noop when storedVersions is already [storageVersion] and only one version is served", func() {
			suf := uniqueSuffix()
			spec := crdSpec{
				Name:              "noops" + suf + "." + toolhiveGroup,
				Group:             toolhiveGroup,
				Kind:              "Noop" + suf,
				ListKind:          "Noop" + suf + "List",
				Plural:            "noops" + suf,
				Singular:          "noop" + suf,
				Labelled:          true,
				HasStatusOnStored: true,
				Versions: []versionSpec{
					{Name: "v1beta1", Served: true, Storage: true},
				},
			}
			installCRD(spec)
			DeferCleanup(func() { deleteCRD(spec.Name) })

			// envtest leaves storedVersions empty until a write happens.
			// Seed it explicitly so the isMigrationNeeded check sees the
			// "clean" state we want to exercise.
			setStoredVersions(spec.Name, []string{"v1beta1"})

			_, err := reconcile(newReconciler(), spec.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(getStoredVersions(spec.Name)).To(Equal([]string{"v1beta1"}))
		})

		It("migrates storedVersions from [v1alpha1,v1beta1] to [v1beta1] and re-stores all CRs", func() {
			suf := uniqueSuffix()
			spec := crdSpec{
				Name:              "happies" + suf + "." + toolhiveGroup,
				Group:             toolhiveGroup,
				Kind:              "Happy" + suf,
				ListKind:          "Happy" + suf + "List",
				Plural:            "happies" + suf,
				Singular:          "happy" + suf,
				Labelled:          true,
				HasStatusOnStored: true,
				Versions: []versionSpec{
					{Name: "v1alpha1", Served: true, Storage: false},
					{Name: "v1beta1", Served: true, Storage: true},
				},
			}
			installCRD(spec)
			DeferCleanup(func() { deleteCRD(spec.Name) })

			crs := createCRs(
				schema.GroupVersionKind{Group: spec.Group, Version: "v1beta1", Kind: spec.Kind},
				"obj-"+suf, 3,
			)

			// Snapshot pre-reconcile resourceVersions. The controller does
			// Get + Update on each CR, so a successful re-store must bump RV.
			// Asserting the bump is the empirical proof the re-encode happened
			// (an empty SSA would not have bumped RV — that was the bug).
			preRVs := make(map[string]string, len(crs))
			for _, cr := range crs {
				live := &unstructured.Unstructured{}
				live.SetGroupVersionKind(cr.GroupVersionKind())
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), live)).To(Succeed())
				preRVs[cr.GetName()] = live.GetResourceVersion()
			}

			setStoredVersions(spec.Name, []string{"v1alpha1", "v1beta1"})

			_, err := reconcile(newReconciler(), spec.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(getStoredVersions(spec.Name)).To(Equal([]string{"v1beta1"}))

			// Verify every CR still exists AND its RV bumped, proving the
			// /status Update actually wrote to etcd.
			for _, cr := range crs {
				live := &unstructured.Unstructured{}
				live.SetGroupVersionKind(cr.GroupVersionKind())
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), live)).To(Succeed())
				Expect(live.GetResourceVersion()).NotTo(Equal(preRVs[cr.GetName()]),
					"CR %s/%s resourceVersion did not bump — re-store did not write to etcd",
					cr.GetNamespace(), cr.GetName())
			}
		})

		It("skips CRDs in foreign API groups", func() {
			suf := uniqueSuffix()
			spec := crdSpec{
				Name:              "outsiders" + suf + ".example.com",
				Group:             "example.com",
				Kind:              "Outsider" + suf,
				ListKind:          "Outsider" + suf + "List",
				Plural:            "outsiders" + suf,
				Singular:          "outsider" + suf,
				Labelled:          true,
				HasStatusOnStored: true,
				Versions: []versionSpec{
					{Name: "v1alpha1", Served: true, Storage: false},
					{Name: "v1beta1", Served: true, Storage: true},
				},
			}
			installCRD(spec)
			DeferCleanup(func() { deleteCRD(spec.Name) })

			setStoredVersions(spec.Name, []string{"v1alpha1", "v1beta1"})

			_, err := reconcile(newReconciler(), spec.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(getStoredVersions(spec.Name)).To(Equal([]string{"v1alpha1", "v1beta1"}),
				"storedVersions must be untouched for foreign-group CRDs")
		})

		It("skips toolhive CRDs missing the opt-in label", func() {
			suf := uniqueSuffix()
			spec := crdSpec{
				Name:              "unlabelled" + suf + "." + toolhiveGroup,
				Group:             toolhiveGroup,
				Kind:              "Unlabelled" + suf,
				ListKind:          "Unlabelled" + suf + "List",
				Plural:            "unlabelled" + suf,
				Singular:          "unlabelled" + suf,
				Labelled:          false,
				HasStatusOnStored: true,
				Versions: []versionSpec{
					{Name: "v1alpha1", Served: true, Storage: false},
					{Name: "v1beta1", Served: true, Storage: true},
				},
			}
			installCRD(spec)
			DeferCleanup(func() { deleteCRD(spec.Name) })

			setStoredVersions(spec.Name, []string{"v1alpha1", "v1beta1"})

			_, err := reconcile(newReconciler(), spec.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(getStoredVersions(spec.Name)).To(Equal([]string{"v1alpha1", "v1beta1"}),
				"storedVersions must be untouched for unlabelled CRDs")
		})

		It("falls back to main-resource SSA when the storage version has no /status subresource", func() {
			suf := uniqueSuffix()
			spec := crdSpec{
				Name:              "nostatus" + suf + "." + toolhiveGroup,
				Group:             toolhiveGroup,
				Kind:              "NoStatus" + suf,
				ListKind:          "NoStatus" + suf + "List",
				Plural:            "nostatus" + suf,
				Singular:          "nostatus" + suf,
				Labelled:          true,
				HasStatusOnStored: false,
				Versions: []versionSpec{
					{Name: "v1alpha1", Served: true, Storage: false},
					{Name: "v1beta1", Served: true, Storage: true},
				},
			}
			installCRD(spec)
			DeferCleanup(func() { deleteCRD(spec.Name) })

			cr := createCRs(
				schema.GroupVersionKind{Group: spec.Group, Version: "v1beta1", Kind: spec.Kind},
				"obj-"+suf, 1,
			)[0]
			setStoredVersions(spec.Name, []string{"v1alpha1", "v1beta1"})

			_, err := reconcile(newReconciler(), spec.Name)
			Expect(err).NotTo(HaveOccurred(),
				"reconcile must succeed via main-resource SSA when no /status subresource exists")
			Expect(getStoredVersions(spec.Name)).To(Equal([]string{"v1beta1"}))

			// CR still exists (migrator doesn't delete).
			live := &unstructured.Unstructured{}
			live.SetGroupVersionKind(cr.GroupVersionKind())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), live)).To(Succeed())
		})

		It("handles pagination across multiple list pages", func() {
			suf := uniqueSuffix()
			spec := crdSpec{
				Name:              "paginated" + suf + "." + toolhiveGroup,
				Group:             toolhiveGroup,
				Kind:              "Paginated" + suf,
				ListKind:          "Paginated" + suf + "List",
				Plural:            "paginated" + suf,
				Singular:          "paginated" + suf,
				Labelled:          true,
				HasStatusOnStored: true,
				Versions: []versionSpec{
					{Name: "v1alpha1", Served: true, Storage: false},
					{Name: "v1beta1", Served: true, Storage: true},
				},
			}
			installCRD(spec)
			DeferCleanup(func() { deleteCRD(spec.Name) })

			// Seven CRs with PageSize=3 forces three pages (3+3+1) and
			// exercises the continue-token loop far more cheaply than 501
			// writes against envtest.
			createCRs(
				schema.GroupVersionKind{Group: spec.Group, Version: "v1beta1", Kind: spec.Kind},
				"obj-"+suf, 7,
			)
			setStoredVersions(spec.Name, []string{"v1alpha1", "v1beta1"})

			// Wrap APIReader to count List calls for this kind. This is the
			// only direct proof that the continue-token loop actually ran —
			// metadata-only SSAs don't leave a managedFields fingerprint.
			counting := &countingAPIReader{Reader: k8sClient, kind: spec.Kind}
			r := &controllers.StorageVersionMigratorReconciler{
				Client:    k8sClient,
				APIReader: counting,
				Scheme:    k8sClient.Scheme(),
				Recorder:  &noopRecorder{},
				PageSize:  3,
			}
			_, err := reconcile(r, spec.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(getStoredVersions(spec.Name)).To(Equal([]string{"v1beta1"}))

			// 7 CRs with PageSize=3 ⇒ 3 list calls (pages of 3+3+1).
			Expect(counting.listCalls).To(BeNumerically(">=", 3),
				"pagination should have triggered at least 3 list calls for 7 CRs at pageSize=3; got %d",
				counting.listCalls)
		})

		It("does not touch storedVersions when a CR re-store fails", func() {
			suf := uniqueSuffix()
			spec := crdSpec{
				Name:              "failures" + suf + "." + toolhiveGroup,
				Group:             toolhiveGroup,
				Kind:              "Failure" + suf,
				ListKind:          "Failure" + suf + "List",
				Plural:            "failures" + suf,
				Singular:          "failure" + suf,
				Labelled:          true,
				HasStatusOnStored: true,
				Versions: []versionSpec{
					{Name: "v1alpha1", Served: true, Storage: false},
					{Name: "v1beta1", Served: true, Storage: true},
				},
			}
			installCRD(spec)
			DeferCleanup(func() { deleteCRD(spec.Name) })

			crs := createCRs(
				schema.GroupVersionKind{Group: spec.Group, Version: "v1beta1", Kind: spec.Kind},
				"obj-"+suf, 3,
			)
			setStoredVersions(spec.Name, []string{"v1alpha1", "v1beta1"})

			failureTarget := client.ObjectKeyFromObject(crs[0])
			failing := &failingUpdateClient{
				Client: k8sClient,
				errFn: func(key client.ObjectKey) error {
					if key == failureTarget {
						return fmt.Errorf("injected update failure for %s", key)
					}
					return nil
				},
			}
			r := &controllers.StorageVersionMigratorReconciler{
				Client:    failing,
				APIReader: k8sClient,
				Scheme:    k8sClient.Scheme(),
				Recorder:  &noopRecorder{},
			}
			_, err := reconcile(r, spec.Name)
			Expect(err).To(HaveOccurred(), "reconcile should surface the injected failure")
			Expect(err.Error()).To(ContainSubstring(failureTarget.Name))

			// Critical contract: storedVersions must NOT be trimmed when any
			// CR re-store failed. Otherwise the next release's v1alpha1
			// removal would orphan the un-migrated object in etcd.
			Expect(getStoredVersions(spec.Name)).To(Equal([]string{"v1alpha1", "v1beta1"}))
		})

		It("leaves storedVersions untouched when a CR re-store hits a Conflict, then trims on retry", func() {
			suf := uniqueSuffix()
			spec := crdSpec{
				Name:              "conflicts" + suf + "." + toolhiveGroup,
				Group:             toolhiveGroup,
				Kind:              "Conflict" + suf,
				ListKind:          "Conflict" + suf + "List",
				Plural:            "conflicts" + suf,
				Singular:          "conflict" + suf,
				Labelled:          true,
				HasStatusOnStored: true,
				Versions: []versionSpec{
					{Name: "v1alpha1", Served: true, Storage: false},
					{Name: "v1beta1", Served: true, Storage: true},
				},
			}
			installCRD(spec)
			DeferCleanup(func() { deleteCRD(spec.Name) })

			crs := createCRs(
				schema.GroupVersionKind{Group: spec.Group, Version: "v1beta1", Kind: spec.Kind},
				"obj-"+suf, 2,
			)
			setStoredVersions(spec.Name, []string{"v1alpha1", "v1beta1"})

			conflictTarget := client.ObjectKeyFromObject(crs[0])
			injectConflict := true
			gr := schema.GroupResource{Group: spec.Group, Resource: spec.Plural}
			conflicting := &failingUpdateClient{
				Client: k8sClient,
				errFn: func(key client.ObjectKey) error {
					if injectConflict && key == conflictTarget {
						return apierrors.NewConflict(gr, key.Name,
							fmt.Errorf("injected conflict"))
					}
					return nil
				},
			}
			r := &controllers.StorageVersionMigratorReconciler{
				Client:    conflicting,
				APIReader: k8sClient,
				Scheme:    k8sClient.Scheme(),
				Recorder:  &noopRecorder{},
			}

			// First pass: conflict swallowed at the per-CR level, but the
			// function-level conflict counter trips errMigrationRetriedDueToConflicts
			// so storedVersions is left untouched.
			_, err := reconcile(r, spec.Name)
			Expect(err).To(HaveOccurred(),
				"reconcile must return an error when a Conflict was swallowed")
			Expect(getStoredVersions(spec.Name)).To(Equal([]string{"v1alpha1", "v1beta1"}),
				"storedVersions must not be trimmed on a pass with any swallowed Conflict")

			// Drop the injection and retry. The cache may have absorbed the
			// non-conflicting CR's RV from the first pass — that's fine, the
			// conflicting one was never recorded in the cache so it'll be
			// re-attempted, succeed, and let the storedVersions patch fire.
			injectConflict = false
			_, err = reconcile(r, spec.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(getStoredVersions(spec.Name)).To(Equal([]string{"v1beta1"}))
		})
	})
})

// ------------------------------------------------------------------
// Test doubles
// ------------------------------------------------------------------

// failingUpdateClient wraps a real client.Client and intercepts Update (and
// Status().Update) for specific object keys. The controller's restoreOne goes
// through Update — so this wrapper is how we inject failures and conflicts.
//
// errFn returns the error to inject for a given key, or nil to let the call
// pass through to the wrapped client. Returning a non-nil error short-circuits
// the call so the underlying object is not modified.
type failingUpdateClient struct {
	client.Client
	errFn func(key client.ObjectKey) error
}

func (f *failingUpdateClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if err := f.errFn(client.ObjectKeyFromObject(obj)); err != nil {
		return err
	}
	return f.Client.Update(ctx, obj, opts...)
}

func (f *failingUpdateClient) Status() client.SubResourceWriter {
	return &failingUpdateStatus{
		inner: f.Client.Status(),
		errFn: f.errFn,
	}
}

type failingUpdateStatus struct {
	inner client.SubResourceWriter
	errFn func(key client.ObjectKey) error
}

func (s *failingUpdateStatus) Create(ctx context.Context, obj client.Object, sub client.Object, opts ...client.SubResourceCreateOption) error {
	return s.inner.Create(ctx, obj, sub, opts...)
}

func (s *failingUpdateStatus) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if err := s.errFn(client.ObjectKeyFromObject(obj)); err != nil {
		return err
	}
	return s.inner.Update(ctx, obj, opts...)
}

func (s *failingUpdateStatus) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return s.inner.Patch(ctx, obj, patch, opts...)
}

func (s *failingUpdateStatus) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	return s.inner.Apply(ctx, obj, opts...)
}

// noopRecorder is a minimal EventRecorder for direct-Reconcile tests.
type noopRecorder struct{}

func (*noopRecorder) Event(_ runtime.Object, _, _, _ string)            {}
func (*noopRecorder) Eventf(_ runtime.Object, _, _, _ string, _ ...any) {}
func (*noopRecorder) AnnotatedEventf(_ runtime.Object, _ map[string]string, _, _, _ string, _ ...any) {
}

// countingAPIReader wraps a client.Reader and records how many List calls
// targeted a given kind. Used by the pagination test to verify the
// continue-token loop ran as expected.
type countingAPIReader struct {
	client.Reader
	kind      string
	listCalls int
}

func (c *countingAPIReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if u, ok := list.(*unstructured.UnstructuredList); ok {
		// ListKind is "<Kind>List"; match on the configured kind prefix.
		if u.GetKind() == c.kind+"List" {
			c.listCalls++
		}
	}
	return c.Reader.List(ctx, list, opts...)
}
