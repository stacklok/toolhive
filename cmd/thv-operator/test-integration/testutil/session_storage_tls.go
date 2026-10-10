// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// ServedAPIVersions are the served toolhive.stacklok.dev versions whose CRD
// schemas carry the same validation rules.
var ServedAPIVersions = []string{mcpv1beta1.GroupVersion.Version, mcpv1alpha1.GroupVersion.Version}

// SessionStorageTLSCase is one admission case for the sessionStorage.tls CEL
// rules, which MCPServer, MCPRemoteProxy and VirtualMCPServer share.
type SessionStorageTLSCase struct {
	// Name is a short identifier, unique among the cases, usable in object names.
	Name string
	// SessionStorage is the spec.sessionStorage under test.
	SessionStorage *mcpv1beta1.SessionStorageConfig
	// WantErr is a substring of the admission error, or empty when the object
	// must be accepted.
	WantErr string
}

// SessionStorageTLSCases returns the sessionStorage.tls admission cases, so
// every workload kind is checked against the same rules.
func SessionStorageTLSCases() []SessionStorageTLSCase {
	caRef := &mcpv1beta1.SecretKeyRef{Name: "redis-ca", Key: "ca.crt"}
	redis := func(tls *mcpv1beta1.RedisTLSConfig) *mcpv1beta1.SessionStorageConfig {
		return &mcpv1beta1.SessionStorageConfig{Provider: "redis", Address: "redis:6380", TLS: tls}
	}
	return []SessionStorageTLSCase{
		{Name: "empty-tls", SessionStorage: redis(&mcpv1beta1.RedisTLSConfig{})},
		{Name: "ca-ref", SessionStorage: redis(&mcpv1beta1.RedisTLSConfig{CACertSecretRef: caRef})},
		{Name: "skip-verify", SessionStorage: redis(&mcpv1beta1.RedisTLSConfig{InsecureSkipVerify: true})},
		{
			Name:           "memory-provider",
			SessionStorage: &mcpv1beta1.SessionStorageConfig{Provider: "memory", TLS: &mcpv1beta1.RedisTLSConfig{}},
			WantErr:        "tls is only supported when provider is redis",
		},
		{
			Name:           "skip-verify-with-ca",
			SessionStorage: redis(&mcpv1beta1.RedisTLSConfig{InsecureSkipVerify: true, CACertSecretRef: caRef}),
			WantErr:        "tls.caCertSecretRef would be ignored",
		},
		{
			Name: "empty-ca-name",
			SessionStorage: redis(&mcpv1beta1.RedisTLSConfig{
				CACertSecretRef: &mcpv1beta1.SecretKeyRef{Name: "", Key: "ca.crt"},
			}),
			WantErr: "tls.caCertSecretRef requires a non-empty name and key",
		},
		{
			Name: "empty-ca-key",
			SessionStorage: redis(&mcpv1beta1.RedisTLSConfig{
				CACertSecretRef: &mcpv1beta1.SecretKeyRef{Name: "redis-ca", Key: ""},
			}),
			WantErr: "tls.caCertSecretRef requires a non-empty name and key",
		},
	}
}
