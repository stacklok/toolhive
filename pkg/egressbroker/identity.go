// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Pod-annotation ↔ broker-env contract between the vMCP untrusted pod
// lifecycle (writer, pkg/vmcp/session/untrusted) and the egress broker
// sidecar (reader, this package's PodIdentityResolver). The pod is the
// registry: the vMCP stamps these annotations at clone time, and the broker
// receives them via downward-API env (THV_UNTRUSTED_*), injected by the clone
// wiring from IdentityEnvMappings. Both sides import this package — the
// annotation keys and env names exist nowhere else. The operator side keeps
// its own mirrors because cmd/thv-operator is a separate module boundary.
const (
	// AnnotationIssuer carries the raw OIDC issuer (public, non-sensitive).
	AnnotationIssuer = "toolhive.stacklok.dev/iss"
	// AnnotationSubjectRaw carries the raw OIDC subject. The broker needs the
	// raw sub for its ExpectedBinding{UserID: sub, Strict: true} check when
	// loading the user's upstream credential. An annotation (not a label)
	// carries it: annotations have no charset limit and are reachable only
	// via the downward API from inside the pod itself; K8s RBAC on pods
	// (list/watch/get) is an operator/vMCP-only grant.
	AnnotationSubjectRaw = "toolhive.stacklok.dev/sub-raw"
	// AnnotationSessionID carries the vMCP session ID the pod serves; it is
	// the token-store session key (tsid) and is checked on pod reuse (a
	// mismatch means a hash collision or a cross-session pod — fail closed,
	// never attach).
	AnnotationSessionID = "toolhive.stacklok.dev/session-id"
	// AnnotationMCPServerName carries the MCPServer CR name the pod backends.
	AnnotationMCPServerName = "toolhive.stacklok.dev/mcpserver-name"
)

// Downward-API env var names the clone-time wiring sets on the broker
// container; each mirrors one pod annotation (see AnnotationForEnv).
const (
	// EnvIssuer mirrors the pod's AnnotationIssuer annotation.
	EnvIssuer = "THV_UNTRUSTED_ISS"
	// EnvSubjectRaw mirrors the pod's AnnotationSubjectRaw annotation (raw
	// OIDC sub; a hashed value is unusable for binding).
	EnvSubjectRaw = "THV_UNTRUSTED_SUB_RAW"
	// EnvSessionID mirrors the pod's AnnotationSessionID annotation.
	EnvSessionID = "THV_UNTRUSTED_SESSION"
	// EnvMCPServer mirrors the pod's AnnotationMCPServerName annotation.
	EnvMCPServer = "THV_UNTRUSTED_MCPSERVER"
	// EnvPodName carries the pod's own name (metadata.name fieldRef) for audit
	// context. Not part of the annotation contract (the name needs no
	// annotation) and not identity-load-bearing: optional, empty tolerated.
	EnvPodName = "THV_UNTRUSTED_POD_NAME"
)

// EnvToAnnotation is the complete downward-API contract: each env var name to
// the pod annotation it mirrors. Iteration order is irrelevant — consumers
// index by env name.
var EnvToAnnotation = map[string]string{
	EnvIssuer:     AnnotationIssuer,
	EnvSubjectRaw: AnnotationSubjectRaw,
	EnvSessionID:  AnnotationSessionID,
	EnvMCPServer:  AnnotationMCPServerName,
}

// AnnotationForEnv returns the pod annotation the given broker identity env
// var mirrors, and whether the env var is part of the contract.
func AnnotationForEnv(envName string) (annotation string, ok bool) {
	annotation, ok = EnvToAnnotation[envName]
	return annotation, ok
}

// AnnotationFieldRef is the downward-API fieldPath form of an annotation key,
// e.g. metadata.annotations['toolhive.stacklok.dev/iss'].
func AnnotationFieldRef(annotation string) string {
	return fmt.Sprintf("metadata.annotations['%s']", annotation)
}

// ResourceNaming derives the operator-managed data-plane resource names for
// one MCPServer from its CR name. The bump-CA objects are generation-named:
// the Secret carries cert+key of ONE CA generation together, so a pod cloned
// mid-rotation always mounts a consistent cert/key pair (no Update-in-place
// rotation race). The bundle ConfigMap carries only the public cert of the
// same generation.
type ResourceNaming struct {
	// CASecret is the bump-CA Secret name (generation-qualified).
	CASecret string
	// CABundle is the bump-CA public bundle ConfigMap name
	// (generation-qualified, same generation as CASecret).
	CABundle string
	// EgressPolicy is the egress-policy ConfigMap name.
	EgressPolicy string
	// Generation is the CA generation suffix (sha256 of the CA cert, hex,
	// first 16 chars); empty for the un-generated base names.
	Generation string
}

const (
	// caSecretSuffix is the base suffix of the per-tenant bump-CA Secret.
	caSecretSuffix = "-bump-ca"
	// caBundleSuffix is the base suffix of the bump-CA public bundle ConfigMap.
	caBundleSuffix = "-bump-ca-bundle"
	// egressPolicySuffix is the suffix of the egress-policy ConfigMap.
	egressPolicySuffix = "-egress-policy"

	// caGenerationLen bounds the cert-hash generation suffix.
	caGenerationLen = 16
)

// CAKeyCert is the public-cert data key of the bump-CA Secret and bundle
// ConfigMap.
const CAKeyCert = "ca.crt"

// CAKeyKey is the private-key data key of the bump-CA Secret. The key lives
// in the same Secret object as the cert precisely so a mounted pair is always
// one consistent generation.
const CAKeyKey = "ca.key" //nolint:gosec // G101: a Secret data key name, not a credential

// CAGeneration returns the generation suffix for a CA certificate: the first
// 16 hex chars of its sha256. Two distinct certs never share a generation;
// the same cert always maps to the same generation (idempotent reconcile).
func CAGeneration(certPEM []byte) string {
	sum := sha256.Sum256(certPEM)
	return hex.EncodeToString(sum[:])[:caGenerationLen]
}

// ResourceNamesFor renders the naming for one CA generation of the named
// MCPServer. generation must come from CAGeneration of the Secret's cert.
// Names stay under the 63-char label limit by construction (MCPServer names
// are themselves DNS-1123).
func ResourceNamesFor(mcpserverName, generation string) ResourceNaming {
	genSuffix := ""
	if generation != "" {
		genSuffix = "-" + generation
	}
	return ResourceNaming{
		CASecret:     mcpserverName + caSecretSuffix + genSuffix,
		CABundle:     mcpserverName + caBundleSuffix + genSuffix,
		EgressPolicy: mcpserverName + egressPolicySuffix,
		Generation:   generation,
	}
}

// BaseCASecretName is the generation-less prefix every CA generation Secret
// shares; used for label-free generation sweeps (GC lists by prefix).
func BaseCASecretName(mcpserverName string) string {
	return mcpserverName + caSecretSuffix
}

// BaseCABundleName is the generation-less prefix every bundle generation
// shares.
func BaseCABundleName(mcpserverName string) string {
	return mcpserverName + caBundleSuffix
}

// TrimGeneration splits a generation-qualified CA resource name into base
// name and generation. ok is false when name does not carry the base prefix
// plus a generation suffix.
func TrimGeneration(name, base string) (generation string, ok bool) {
	rest, found := strings.CutPrefix(name, base+"-")
	if !found || rest == "" {
		return "", false
	}
	return rest, true
}

// PodIdentity is the (user, session, server) tuple the broker serves,
// resolved from the broker's own pod metadata. The token-selection key and
// the Strict binding user both come from here — never from anything the
// request carries (D5: credential choice is driven by pod identity, not by a
// server-supplied value).
type PodIdentity struct {
	// Issuer is the OIDC issuer of the user this pod serves.
	Issuer string
	// Subject is the raw OIDC sub; asserted as ExpectedBinding.UserID on every
	// credential load.
	Subject string
	// SessionID is the vMCP session ID; used as the tsid token-store key.
	SessionID string
	// MCPServer is the MCPServer CR name this pod backends.
	MCPServer string
}

// PodIdentityResolver resolves the broker's identity from its environment
// (downward API). Resolution happens once at construction; the hot path
// performs no K8s or Redis reads (security: the pod object is the registry).
type PodIdentityResolver struct {
	identity PodIdentity
}

// NewPodIdentityResolver validates that all four downward-API identity env
// vars are present and non-empty. Missing identity is a startup error — a
// broker that cannot prove which user/session it serves must never run
// (fail closed on unknown pod identity).
func NewPodIdentityResolver(getenv func(string) string) (*PodIdentityResolver, error) {
	if getenv == nil {
		return nil, fmt.Errorf("egressbroker: env lookup must not be nil")
	}
	identity := PodIdentity{
		Issuer:    strings.TrimSpace(getenv(EnvIssuer)),
		Subject:   strings.TrimSpace(getenv(EnvSubjectRaw)),
		SessionID: strings.TrimSpace(getenv(EnvSessionID)),
		MCPServer: strings.TrimSpace(getenv(EnvMCPServer)),
	}
	for name, value := range map[string]string{
		EnvIssuer:     identity.Issuer,
		EnvSubjectRaw: identity.Subject,
		EnvSessionID:  identity.SessionID,
		EnvMCPServer:  identity.MCPServer,
	} {
		if value == "" {
			return nil, fmt.Errorf("egressbroker: required identity env var %s is empty or unset "+
				"(downward-API annotation missing); refusing to start", name)
		}
	}
	return &PodIdentityResolver{identity: identity}, nil
}

// PodIdentity returns the validated identity.
func (r *PodIdentityResolver) PodIdentity() PodIdentity {
	return r.identity
}

// PodName returns the optional pod-name env (audit context only; "" when the
// deployment does not set it — never identity-load-bearing).
func (*PodIdentityResolver) PodName(getenv func(string) string) string {
	return strings.TrimSpace(getenv(EnvPodName))
}
