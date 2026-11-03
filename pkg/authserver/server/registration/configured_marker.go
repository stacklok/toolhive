// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"github.com/ory/fosite"
)

// configuredClient is implemented only by the wrappers MarkConfigured returns.
// It is deliberately separate from dcrIssued: a configured client must keep
// every behaviour that skips DCR-issued clients (audience binding, TTL
// renewal) applying to it.
type configuredClient interface {
	configured()
}

// IsConfigured reports whether client carries the configured stamp, meaning
// storage read it back from a row written by ReconcileConfiguredClient.
func IsConfigured(client fosite.Client) bool {
	_, ok := client.(configuredClient)
	return ok
}

// MarkConfigured wraps a client read back from a row written by
// ReconcileConfiguredClient so the stamp survives the storage round-trip.
//
// client must be *fosite.DefaultOpenIDConnectClient or *fosite.DefaultClient.
// The wrapper embeds the concrete pointer rather than the fosite.Client
// interface so every optional interface the concrete type implements
// (fosite.OpenIDConnectClient, ClientWithSecretRotation) is promoted.
//
// For any other type it returns (nil, false). Callers must treat that as a
// denial: handing back the client unmarked would let a configured client
// escape the check the stamp exists for.
func MarkConfigured(client fosite.Client) (fosite.Client, bool) {
	switch c := client.(type) {
	case *fosite.DefaultOpenIDConnectClient:
		return &configuredOIDC{DefaultOpenIDConnectClient: c}, true
	case *fosite.DefaultClient:
		return &configuredDefault{DefaultClient: c}, true
	default:
		return nil, false
	}
}

type configuredOIDC struct {
	*fosite.DefaultOpenIDConnectClient
}

func (configuredOIDC) configured() {}

type configuredDefault struct {
	*fosite.DefaultClient
}

func (configuredDefault) configured() {}
