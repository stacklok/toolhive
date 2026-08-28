// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

// InvalidCABundleError identifies a terminal CA bundle configuration or content error.
// ConfigMap read errors are deliberately excluded because they may be transient.
type InvalidCABundleError struct {
	err error
}

func (e *InvalidCABundleError) Error() string {
	return e.err.Error()
}

func (e *InvalidCABundleError) Unwrap() error {
	return e.err
}

func invalidCABundleError(err error) error {
	return &InvalidCABundleError{err: err}
}

// ResolveCABundle reads and validates a CA bundle referenced by a workload.
// ConfigMapKeySelector is deliberately resolved here rather than left to pod
// admission: an optional or malformed reference must never result in a
// workload without the trust roots it requires.
//
// The returned bytes are not retained by the operator. Callers only need the
// successful result to gate reconciliation; the workload still mounts the
// ConfigMap so updates can be observed by kubelet.
func ResolveCABundle(ctx context.Context, c client.Reader, namespace string, ref *mcpv1beta1.CABundleSource) ([]byte, error) {
	// Shape-only: the CA volume name is index-derived, so the OIDC
	// ConfigMap-name length cap does not apply here.
	if err := validation.ValidateCABundleSourceShape(ref); err != nil {
		return nil, invalidCABundleError(err)
	}
	if ref == nil || ref.ConfigMapRef == nil {
		return nil, nil
	}
	if ref.ConfigMapRef.Optional != nil && *ref.ConfigMapRef.Optional {
		return nil, invalidCABundleError(fmt.Errorf("caBundleRef.configMapRef.optional must be false or omitted"))
	}

	configMap := &corev1.ConfigMap{}
	name := ref.ConfigMapRef.Name
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, configMap); err != nil {
		return nil, fmt.Errorf("failed to get CA bundle ConfigMap %q in namespace %q: %w", name, namespace, err)
	}

	key := ref.ConfigMapRef.Key
	if key == "" {
		key = validation.OIDCCABundleDefaultKey
	}
	value, ok := configMap.Data[key]
	if !ok {
		binaryValue, binaryOK := configMap.BinaryData[key]
		if binaryOK {
			value = string(binaryValue)
			ok = true
		}
	}
	if !ok {
		return nil, invalidCABundleError(fmt.Errorf("CA bundle ConfigMap %q does not contain key %q in data or binaryData", name, key))
	}
	if err := validatePEMCertificates([]byte(value)); err != nil {
		return nil, invalidCABundleError(fmt.Errorf("CA bundle ConfigMap %q key %q is invalid: %w", name, key, err))
	}
	return []byte(value), nil
}

func validatePEMCertificates(value []byte) error {
	if len(bytes.TrimSpace(value)) == 0 {
		return fmt.Errorf("certificate data is empty")
	}
	remaining := value
	count := 0
	for len(bytes.TrimSpace(remaining)) > 0 {
		block, rest := pem.Decode(remaining)
		if block == nil {
			return fmt.Errorf("contains non-PEM certificate data")
		}
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("contains PEM block of type %q, expected CERTIFICATE", block.Type)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("contains an invalid certificate: %w", err)
		}
		count++
		remaining = rest
	}
	if count == 0 {
		return fmt.Errorf("certificate data is empty")
	}
	return nil
}

// ValidateEmbeddedAuthServerCABundles resolves every embedded auth server CA reference.
func ValidateEmbeddedAuthServerCABundles(
	ctx context.Context, c client.Reader, namespace string, cfg *mcpv1beta1.EmbeddedAuthServerConfig,
) error {
	if cfg == nil {
		return nil
	}
	for i := range cfg.UpstreamProviders {
		provider := &cfg.UpstreamProviders[i]
		ref := provider.CABundleRef()
		if ref != nil {
			if _, err := ResolveCABundle(ctx, c, namespace, ref); err != nil {
				return fmt.Errorf("upstreamProviders[%d] (%q) caBundleRef: %w", i, provider.Name, err)
			}
		}
	}
	for i := range cfg.TrustedIssuers {
		issuer := &cfg.TrustedIssuers[i]
		if issuer.CABundleRef != nil {
			if _, err := ResolveCABundle(ctx, c, namespace, issuer.CABundleRef); err != nil {
				return fmt.Errorf("trustedIssuers[%d] (%q) caBundleRef: %w", i, issuer.IssuerURL, err)
			}
		}
	}
	return nil
}
