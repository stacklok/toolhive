// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package v1alpha1 contains the deprecated v1alpha1 API types for the
// toolhive.stacklok.dev group. These types exist solely to enable seamless
// CRD graduation from v1alpha1 → v1beta1: the CRD serves both versions
// (with conversion strategy "None"), so existing v1alpha1 resources continue
// to work while users migrate their manifests to v1beta1.
//
// All Spec and Status types are imported from v1beta1 — the schemas are
// identical. Only the root resource types and their List companions are
// defined here so that controller-gen produces a multi-version CRD.
//
// This package will be removed in a future release once the v1alpha1
// deprecation period ends.
//
// +kubebuilder:object:generate=true
// +groupName=toolhive.stacklok.dev
package v1alpha1
