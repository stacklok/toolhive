// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package k8s

// Test helper constants and functions shared across test files

// validKubeconfigYAML is a valid kubeconfig YAML for testing purposes
const validKubeconfigYAML = `apiVersion: v1
kind: Config
current-context: test-context
clusters:
- cluster:
    server: https://localhost:6443
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
users:
- name: test-user
  user:
    token: fake-token
`
