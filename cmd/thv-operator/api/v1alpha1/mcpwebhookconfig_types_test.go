// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive/pkg/webhook"
)

func TestMCPWebhookConfig_Creation(t *testing.T) {
	t.Parallel()

	timeout := metav1.Duration{Duration: 5 * time.Second}

	config := &MCPWebhookConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-webhook-config",
			Namespace: "default",
		},
		Spec: MCPWebhookConfigSpec{
			Validating: []WebhookSpec{
				{
					Name:          "test-validator",
					URL:           "https://example.com/validate",
					Timeout:       &timeout,
					FailurePolicy: webhook.FailurePolicyFail,
					TLSConfig: &WebhookTLSConfig{
						InsecureSkipVerify: true,
					},
				},
			},
			Mutating: []WebhookSpec{
				{
					Name:          "test-mutator",
					URL:           "https://example.com/mutate",
					Timeout:       &timeout,
					FailurePolicy: webhook.FailurePolicyIgnore,
					HMACSecretRef: &SecretKeyRef{
						Name: "hmac-secret",
						Key:  "key",
					},
				},
			},
		},
	}

	assert.NotNil(t, config)
	assert.Equal(t, "test-webhook-config", config.Name)
	assert.Len(t, config.Spec.Validating, 1)
	assert.Len(t, config.Spec.Mutating, 1)

	assert.Equal(t, "test-validator", config.Spec.Validating[0].Name)
	assert.Equal(t, webhook.FailurePolicyFail, config.Spec.Validating[0].FailurePolicy)
	assert.True(t, config.Spec.Validating[0].TLSConfig.InsecureSkipVerify)

	assert.Equal(t, "test-mutator", config.Spec.Mutating[0].Name)
	assert.Equal(t, webhook.FailurePolicyIgnore, config.Spec.Mutating[0].FailurePolicy)
	assert.Equal(t, "hmac-secret", config.Spec.Mutating[0].HMACSecretRef.Name)
}
