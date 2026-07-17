// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

const (
	// podTemplateSpecHashAnnotation tracks the SHA256 hash of a user-provided PodTemplateSpec.
	// It is used by workload reconcilers to detect changes without comparing full rendered
	// templates, which may include Kubernetes-defaulted fields.
	podTemplateSpecHashAnnotation = "toolhive.stacklok.io/podtemplatespec-hash"
)
