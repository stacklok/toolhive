// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// conditionTypeValid is the condition type used across config controller tests.
const conditionTypeValid = mcpv1alpha1.ConditionTypeValid

// podTemplateSpecToRawExtension is a test helper to convert PodTemplateSpec to RawExtension
func podTemplateSpecToRawExtension(t *testing.T, pts *corev1.PodTemplateSpec) *runtime.RawExtension {
	t.Helper()
	if pts == nil {
		return nil
	}
	raw, err := json.Marshal(pts)
	require.NoError(t, err, "Failed to marshal PodTemplateSpec")
	return &runtime.RawExtension{Raw: raw}
}
