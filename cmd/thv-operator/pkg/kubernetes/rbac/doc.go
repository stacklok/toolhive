// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package rbac provides convenience methods for working with Kubernetes RBAC resources.
// This includes ServiceAccounts, Roles, and RoleBindings, with support for owner references
// and automatic garbage collection.
//
// # Error Handling and Reconciliation
//
// All methods in this package return errors directly without performing internal retries.
// This follows the standard Kubernetes controller pattern where the controller-runtime's
// work queue handles retries automatically. When an error is returned from a reconcile
// function, the controller-runtime will:
//
//  1. Requeue the reconciliation request
//  2. Apply exponential backoff
//  3. Automatically retry until success or max retries
//
// Therefore, callers should NOT use client-go's RetryOnConflict or implement manual retry
// logic. Simply return the error and let the controller work queue handle it.
//
// # Usage Example
//
//	func (r *MyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
//	    rbacClient := rbac.NewClient(r.Client, r.Scheme)
//
//	    // Create RBAC resources - errors are automatically retried by controller-runtime
//	    if err := rbacClient.EnsureRBACResources(ctx, rbac.EnsureRBACResourcesParams{
//	        Name:      "my-service-account",
//	        Namespace: "default",
//	        Rules:     myRBACRules,
//	        Owner:     myCustomResource,
//	    }); err != nil {
//	        // Simply return the error - controller-runtime handles retries
//	        return ctrl.Result{}, err
//	    }
//
//	    return ctrl.Result{}, nil
//	}
package rbac
