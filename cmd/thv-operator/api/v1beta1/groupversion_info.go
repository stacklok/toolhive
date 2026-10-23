// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package v1beta1 contains API Schema definitions for the toolhive v1beta1 API group
// +kubebuilder:object:generate=true
// +groupName=toolhive.stacklok.dev
package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "toolhive.stacklok.dev", Version: "v1beta1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme
	SchemeBuilder = newSchemeBuilder(GroupVersion)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

type schemeBuilder struct {
	runtime.SchemeBuilder
	GroupVersion schema.GroupVersion
}

func newSchemeBuilder(groupVersion schema.GroupVersion) *schemeBuilder {
	builder := &schemeBuilder{GroupVersion: groupVersion}
	builder.SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		metav1.AddToGroupVersion(scheme, groupVersion)
		return nil
	})
	return builder
}

func (builder *schemeBuilder) Register(objects ...runtime.Object) *schemeBuilder {
	builder.SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(builder.GroupVersion, objects...)
		return nil
	})
	return builder
}

func (builder *schemeBuilder) Build() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	return scheme, builder.AddToScheme(scheme)
}
