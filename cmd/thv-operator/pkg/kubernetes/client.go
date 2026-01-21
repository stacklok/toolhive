// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes/configmaps"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes/secrets"
)

// Client provides a unified interface for Kubernetes resource operations.
// It composes domain-specific clients for different resource types.
type Client struct {
	// Secrets provides operations for Kubernetes Secrets.
	Secrets *secrets.Client
	// ConfigMaps provides operations for Kubernetes ConfigMaps.
	ConfigMaps *configmaps.Client
}

// NewClient creates a new Kubernetes Client with all sub-clients initialized.
func NewClient(c client.Client, scheme *runtime.Scheme) *Client {
	return &Client{
		Secrets:    secrets.NewClient(c, scheme),
		ConfigMaps: configmaps.NewClient(c, scheme),
	}
}
