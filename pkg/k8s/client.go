// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewClient creates a new standard Kubernetes clientset using the default config loading.
// It tries in-cluster config first, then falls back to out-of-cluster config.
// Use this when you only need to work with standard Kubernetes resources.
func NewClient() (kubernetes.Interface, *rest.Config, error) {
	config, err := GetConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create kubernetes config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return clientset, config, nil
}

// NewClientWithConfig creates a new standard Kubernetes clientset from the provided config.
// Use this when you have an existing config and only need standard Kubernetes resources.
func NewClientWithConfig(config *rest.Config) (kubernetes.Interface, error) {
	if config == nil {
		return nil, fmt.Errorf("failed to create kubernetes client: config cannot be nil")
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return clientset, nil
}

// NewControllerRuntimeClient creates a new controller-runtime client with a custom scheme.
// This is useful for working with Custom Resource Definitions (CRDs) alongside standard resources.
// The scheme should have all required types registered before calling this function.
//
// Example:
//
//	scheme := runtime.NewScheme()
//	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
//	utilruntime.Must(mycrds.AddToScheme(scheme))
//	k8sClient, err := k8s.NewControllerRuntimeClient(scheme)
func NewControllerRuntimeClient(scheme *runtime.Scheme) (client.Client, error) {
	config, err := GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kubernetes config: %w", err)
	}

	return newControllerRuntimeClientWithConfig(config, scheme)
}

// newControllerRuntimeClientWithConfig is the internal implementation for creating a controller-runtime client
func newControllerRuntimeClientWithConfig(config *rest.Config, scheme *runtime.Scheme) (client.Client, error) {
	if scheme == nil {
		return nil, fmt.Errorf("failed to create controller-runtime client: scheme cannot be nil")
	}

	k8sClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create controller-runtime client: %w", err)
	}

	return k8sClient, nil
}

// NewDynamicClient creates a new dynamic client for working with arbitrary resources.
// Use this when you need to work with resources without compile-time type information,
// such as discovering resources at runtime or working with unstructured data.
func NewDynamicClient() (dynamic.Interface, error) {
	config, err := GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kubernetes config: %w", err)
	}

	return newDynamicClientWithConfig(config)
}

// newDynamicClientWithConfig is the internal implementation for creating a dynamic client
func newDynamicClientWithConfig(config *rest.Config) (dynamic.Interface, error) {
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return dynamicClient, nil
}

// IsAvailable checks if Kubernetes is available by attempting to create a client
// and verifying connectivity.
func IsAvailable() bool {
	_, _, err := NewClient()
	return err == nil
}
