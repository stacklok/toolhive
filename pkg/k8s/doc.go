// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package k8s provides common Kubernetes client utilities for ToolHive.
//
// This package centralizes Kubernetes client creation, configuration loading,
// and namespace detection to avoid duplication across the codebase and prevent
// circular dependencies.
//
// # Configuration Loading
//
// The package uses a fallback strategy for loading Kubernetes configuration:
//
//  1. In-cluster configuration (when running inside a Kubernetes pod)
//     - Reads from /var/run/secrets/kubernetes.io/serviceaccount/
//     - Automatically configured by Kubernetes
//
//  2. Out-of-cluster configuration (when running locally or outside Kubernetes)
//     - Follows standard kubeconfig loading rules:
//     a. KUBECONFIG environment variable (can specify multiple files separated by colons)
//     b. ~/.kube/config file (default location)
//
// # Namespace Detection
//
// The GetCurrentNamespace() function detects the current Kubernetes namespace
// using multiple methods in order of precedence:
//
//  1. Service Account Namespace File
//     - Path: /var/run/secrets/kubernetes.io/serviceaccount/namespace
//     - Available when running inside a Kubernetes pod
//     - Most reliable method for in-cluster deployments
//
//  2. Environment Variable
//     - Variable: POD_NAMESPACE
//     - Commonly set via Kubernetes downward API
//     - Example in pod spec:
//     env:
//     - name: POD_NAMESPACE
//     valueFrom:
//     fieldRef:
//     fieldPath: metadata.namespace
//
//  3. Kubeconfig Context
//     - Reads namespace from the current kubectl context
//     - Uses the same kubeconfig loading rules as configuration
//     - Falls back if namespace is not set in context
//
//  4. Default Namespace
//     - Falls back to "default" if all other methods fail
//
// # Environment Variables
//
// The package respects the following environment variables:
//
//   - KUBECONFIG: Specifies path(s) to kubeconfig files (colon-separated)
//   - POD_NAMESPACE: Explicitly sets the current namespace (used by GetCurrentNamespace)
//
// # Usage Examples
//
// Creating a Kubernetes client:
//
//	import "github.com/stacklok/toolhive/pkg/k8s"
//
//	// Create client with automatic config detection
//	clientset, config, err := k8s.NewClient()
//	if err != nil {
//	    return err
//	}
//
//	// Use the client
//	pods, err := clientset.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
//
// Creating a client from existing config:
//
//	import "github.com/stacklok/toolhive/pkg/k8s"
//
//	// Get config separately
//	config, err := k8s.GetConfig()
//	if err != nil {
//	    return err
//	}
//
//	// Customize config if needed
//	config.Timeout = 30 * time.Second
//
//	// Create client from config
//	clientset, err := k8s.NewClientWithConfig(config)
//	if err != nil {
//	    return err
//	}
//
// Working with Custom Resource Definitions (CRDs):
//
//	import "github.com/stacklok/toolhive/pkg/k8s"
//	import "k8s.io/apimachinery/pkg/runtime"
//	import utilruntime "k8s.io/apimachinery/pkg/util/runtime"
//	import clientgoscheme "k8s.io/client-go/kubernetes/scheme"
//
//	// Create a scheme and register your CRD types
//	scheme := runtime.NewScheme()
//	utilruntime.Must(clientgoscheme.AddToScheme(scheme))        // Standard K8s types
//	utilruntime.Must(mycrdv1alpha1.AddToScheme(scheme))         // Your CRD types
//
//	// Create controller-runtime client
//	k8sClient, err := k8s.NewControllerRuntimeClient(scheme)
//	if err != nil {
//	    return err
//	}
//
//	// Now you can work with both standard resources and CRDs
//	var myCustomResource mycrdv1alpha1.MyResource
//	err = k8sClient.Get(ctx, types.NamespacedName{Name: "example", Namespace: "default"}, &myCustomResource)
//
// Working with dynamic/unstructured resources:
//
//	import "github.com/stacklok/toolhive/pkg/k8s"
//	import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
//	import "k8s.io/apimachinery/pkg/runtime/schema"
//
//	// Create dynamic client
//	dynamicClient, err := k8s.NewDynamicClient()
//	if err != nil {
//	    return err
//	}
//
//	// Define the resource you want to work with
//	gvr := schema.GroupVersionResource{
//	    Group:    "example.com",
//	    Version:  "v1",
//	    Resource: "myresources",
//	}
//
//	// Get resources
//	list, err := dynamicClient.Resource(gvr).Namespace("default").List(ctx, metav1.ListOptions{})
//
// Detecting the current namespace:
//
//	import "github.com/stacklok/toolhive/pkg/k8s"
//
//	// Get current namespace with automatic detection
//	namespace := k8s.GetCurrentNamespace()
//	fmt.Printf("Current namespace: %s\n", namespace)
//
//	// Use in operations
//	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
//
// Checking Kubernetes availability:
//
//	import "github.com/stacklok/toolhive/pkg/k8s"
//
//	if k8s.IsAvailable() {
//	    fmt.Println("Kubernetes is available")
//	    // Proceed with Kubernetes operations
//	} else {
//	    fmt.Println("Kubernetes is not available, falling back to local mode")
//	    // Use alternative runtime
//	}
//
// # Client Types
//
// The package provides three specialized client creation functions:
//
//  1. NewClient() - Standard Kubernetes clientset (kubernetes.Interface)
//     - Use for working with built-in Kubernetes resources (Pods, Services, etc.)
//     - Type-safe access to core API groups
//     - Most common choice for basic Kubernetes operations
//
//  2. NewControllerRuntimeClient() - Controller-runtime client (client.Client)
//     - Use when working with Custom Resource Definitions (CRDs)
//     - Requires a runtime.Scheme with registered types
//     - Provides unified access to both standard and custom resources
//     - Ideal for operators, controllers, and CRD-heavy applications
//
//  3. NewDynamicClient() - Dynamic client (dynamic.Interface)
//     - Use for working with arbitrary resources without compile-time types
//     - Works with unstructured.Unstructured objects
//     - Useful for discovery, generic tooling, or when resource types are unknown
//
// # Design Considerations
//
// This package is designed to:
//
//   - Provide a single source of truth for Kubernetes client creation
//   - Enable reuse across different packages without circular dependencies
//   - Support both in-cluster and out-of-cluster deployments
//   - Support multiple client types for different use cases
//   - Follow Kubernetes client-go conventions and best practices
//   - Maintain compatibility with standard Kubernetes tooling (kubectl, etc.)
//   - Keep the config/scheme layers separate to avoid circular dependencies
//
// # Testing
//
// When writing tests that use this package:
//
//   - Use fake clientsets from k8s.io/client-go/kubernetes/fake for standard clients
//   - Use controller-runtime fake client for CRD testing
//   - Pass fake clients directly to functions that accept the respective interfaces
//   - Mock config and namespace detection as needed for your test scenarios
//
// Example test setup for standard clients:
//
//	import (
//	    "k8s.io/client-go/kubernetes/fake"
//	    "k8s.io/client-go/rest"
//	)
//
//	func TestMyFunction(t *testing.T) {
//	    // Create fake client
//	    fakeClient := fake.NewSimpleClientset()
//
//	    // Use with functions that accept kubernetes.Interface
//	    result, err := MyFunction(fakeClient)
//	    // assertions...
//	}
//
// Example test setup for controller-runtime clients:
//
//	import (
//	    "k8s.io/apimachinery/pkg/runtime"
//	    "sigs.k8s.io/controller-runtime/pkg/client/fake"
//	)
//
//	func TestMyControllerFunction(t *testing.T) {
//	    // Create scheme
//	    scheme := runtime.NewScheme()
//	    utilruntime.Must(clientgoscheme.AddToScheme(scheme))
//	    utilruntime.Must(mycrdv1alpha1.AddToScheme(scheme))
//
//	    // Create fake controller-runtime client
//	    fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
//
//	    // Use with functions that accept client.Client
//	    result, err := MyControllerFunction(fakeClient)
//	    // assertions...
//	}
package k8s
