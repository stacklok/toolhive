// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

// RuntimeConfig is the on-disk shape the operator writes to the vMCP
// ConfigMap and the vMCP binary parses on startup. It embeds the public
// Config (the user-facing API surface that VirtualMCPServerSpec exposes) and
// is the place to add operator-resolved fields that should NOT appear in the
// CRD schema.
//
// Why a wrapper: cmd/thv-operator/api/v1beta1/virtualmcpserver_types.go
// types Spec.Config as `config.Config`. controller-gen renders the CRD
// OpenAPI schema by walking the type graph from CRD roots, so any field
// added anywhere under Config — directly or transitively — leaks into the
// public CRD. Operator-resolved fields (per-backend secret-identifier maps,
// resolved CA bundle paths, and similar) should travel through the
// ConfigMap to the vMCP runtime without becoming public API surface.
//
// Today RuntimeConfig embeds Config inline and adds nothing. Marshalled YAML
// is byte-identical to marshalling Config directly. Future operator-only
// fields are added on this struct, leaving Config — and therefore the CRD —
// untouched.
//
// Invariants (enforced by tests in this package and in
// cmd/thv-operator/pkg/spectoconfig/runtime_config_drift_test.go):
//
//   - Not a CRD type. RuntimeConfig has no kubebuilder markers and must
//     never be field-referenced from any v1beta1 type. The single way to
//     leak this struct's fields into the CRD is to retype
//     VirtualMCPServerSpec.Config from `config.Config` to
//     `config.RuntimeConfig`. Don't.
//
//   - No top-level field on RuntimeConfig may share a JSON or YAML key with
//     any Config field. encoding/json (which inlines via anonymous-field
//     promotion) and gopkg.in/yaml.v3 (which honors `,inline`) handle key
//     collisions differently — yaml.v3 errors or has a different
//     precedence than encoding/json's outer-wins rule. The disjoint-tag
//     test in runtime_config_test.go pins this.
type RuntimeConfig struct {
	Config `json:",inline" yaml:",inline"` //nolint:revive // inline is a valid kubernetes json/yaml tag option
}
