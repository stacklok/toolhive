// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package secrets provides utilities for working with Kubernetes Secrets.
//
// This package offers a Client that wraps the controller-runtime client
// and provides convenience methods for common Secret operations like
// Get, GetValue, and Upsert with optional owner references.
//
// Example usage:
//
//	client := secrets.NewClient(ctrlClient, scheme)
//
//	// Get a secret value
//	value, err := client.GetSecretValue(ctx, "namespace", secretKeySelector)
//
//	// Upsert a secret with owner reference
//	result, err := client.UpsertWithOwnerReference(ctx, secret, ownerObject)
package secrets
