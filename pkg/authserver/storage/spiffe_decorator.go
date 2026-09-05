// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
)

// SPIFFEStorageDecorator resolves immutable configured SPIFFE clients before
// the dynamic DCR/CIMD storage backend. Static clients exist only in this
// overlay; they are never persisted or eligible for registration.
type SPIFFEStorageDecorator struct {
	Storage
	clients map[string]fosite.Client
}

// NewSPIFFEStorageDecorator overlays static SPIFFE clients over base. It
// durably claims each configured client ID in base's durable backend (see
// preflightDurableCollisions) before creating the overlay.
func NewSPIFFEStorageDecorator(ctx context.Context, base Storage, clients map[string]fosite.Client) (Storage, error) {
	if base == nil {
		return nil, fmt.Errorf("storage is required")
	}
	if len(clients) == 0 {
		return base, nil
	}
	privateClients := maps.Clone(clients)
	for clientID, client := range privateClients {
		if client == nil {
			return nil, fmt.Errorf("static SPIFFE client %q is required", clientID)
		}
		if client.GetID() != clientID {
			return nil, fmt.Errorf("static SPIFFE client map key %q does not match client ID %q", clientID, client.GetID())
		}
	}
	if err := preflightDurableCollisions(ctx, base, privateClients); err != nil {
		return nil, err
	}
	return &SPIFFEStorageDecorator{Storage: base, clients: privateClients}, nil
}

// PreflightSPIFFEStaticClientCollisions durably claims each configured static
// client ID in base's durable backend before the caller starts serving static
// SPIFFE clients from an in-process overlay. See preflightDurableCollisions.
func PreflightSPIFFEStaticClientCollisions(ctx context.Context, base Storage, clients map[string]fosite.Client) error {
	if base == nil {
		return fmt.Errorf("storage is required")
	}
	return preflightDurableCollisions(ctx, base, clients)
}

// preflightDurableCollisions durably claims each configured static SPIFFE
// client ID in base's durable backend using an inert placeholder (see
// staticClientPlaceholder) — never the real SPIFFE client. Reconciliation is
// idempotent: a restart with unchanged association config reconstructs the
// identical placeholder and succeeds silently; a collision with a DCR-issued
// client, or with a durably-claimed placeholder for a *different* association
// at the same ID, fails loudly.
//
// This closes a cross-replica race that a read-only GetClient check cannot:
// with Redis and multiple replicas, an older/still-rolling replica without
// this SPIFFE config could otherwise DCR-register the same client ID after a
// newer replica's read-only check passed, leaving different replicas
// resolving different clients for the same ID. Durably claiming the ID here
// makes the reservation visible to every replica's ReconcileConfiguredClient
// and RegisterClient calls immediately.
func preflightDurableCollisions(ctx context.Context, base Storage, clients map[string]fosite.Client) error {
	underlying := Unwrap(base)
	for clientID, client := range clients {
		placeholder := staticClientPlaceholder(client)
		if err := underlying.ReconcileConfiguredClient(ctx, placeholder); err != nil {
			return fmt.Errorf("claim durable placeholder for static SPIFFE client ID %q: %w", clientID, err)
		}
	}
	return nil
}

// resourceScopedClient is implemented by fosite.Client types (e.g.
// registration.SPIFFEClient) that maintain an RFC 8707 resource allowlist
// independent of GetAudience's audience allowlist. This mirrors the
// identically-shaped interface in pkg/authserver/server/tokenexchange -- both
// packages match it structurally against the same concrete client types
// without either importing the other's unexported interface.
type resourceScopedClient interface {
	Resources() []string
}

// spiffeIdentityClient is implemented by fosite.Client types (e.g.
// spiffeStaticClient) that carry a durable SPIFFE association identity
// fingerprint independent of OAuth policy. Mirrors resourceScopedClient's
// structural-matching pattern above.
type spiffeIdentityClient interface {
	IdentityFingerprint() string
}

// inertPlaceholderClient wraps a *fosite.DefaultClient to suppress its
// implicit defaulting: fosite.DefaultClient.GetGrantTypes and
// GetResponseTypes each return a single-element default
// (["authorization_code"] / ["code"]) when the underlying field is nil,
// treating "omitted" as "the interactive default" per the OIDC registration
// metadata convention they follow. staticClientPlaceholder needs the
// opposite: a client that can never be issued a token via any grant.
// Overriding both getters at the type — rather than storing an empty
// (non-nil) slice on the embedded struct — is required, not just clearer:
// fosite defaults on len() == 0 regardless of nil vs. non-nil-empty, so an
// empty slice alone would not suppress the default.
//
// This override is only reliable for the live Go value. A round trip through
// Redis serializes the *fields* (buildStoredClient reads GetGrantTypes() /
// GetResponseTypes() into JSON, which correctly capture the override's empty
// result), but clientFromStored ordinarily reconstructs a bare
// *fosite.DefaultClient on read, which reintroduces the very defaulting this
// type exists to suppress. storedClient.Reserved closes that gap: it marks a
// row as this placeholder so clientFromStored re-wraps the reconstructed
// client in inertPlaceholderClient on every read, independent of backend.
// See buildStoredClient and clientFromStored in redis.go.
type inertPlaceholderClient struct {
	registration.BackChannelOnlyMarker
	*fosite.DefaultClient
	resources           []string
	identityFingerprint string
}

func (inertPlaceholderClient) GetGrantTypes() fosite.Arguments    { return nil }
func (inertPlaceholderClient) GetResponseTypes() fosite.Arguments { return nil }

// Resources returns the placeholder's RFC 8707 resource allowlist, satisfying
// resourceScopedClient so the fingerprint comparison (see clientFingerprint)
// distinguishes associations that differ only in their resource allowlist.
// Returns a defensive copy, matching SPIFFEClient.Resources and
// SPIFFEAuthorizationPolicy.Resources.
func (c inertPlaceholderClient) Resources() []string { return slices.Clone(c.resources) }

func (c inertPlaceholderClient) IdentityFingerprint() string { return c.identityFingerprint }

// isReservedPlaceholder reports whether client is a staticClientPlaceholder
// value, letting buildStoredClient (redis.go) mark the persisted row so
// clientFromStored can restore the same inert wrapper on read-back. Mirrors
// registration.DCRIssued's marker-detection pattern.
func isReservedPlaceholder(client fosite.Client) bool {
	_, ok := client.(inertPlaceholderClient)
	return ok
}

// staticClientPlaceholder returns an inert stand-in for a real static SPIFFE
// client, safe to durably persist. It is never returned to a caller of
// GetClient — GetClient always resolves a reserved ID from the in-process
// overlay first (see SPIFFEStorageDecorator.GetClient) — but must still be
// structurally impossible to authenticate as or exchange a token for, in
// case that invariant is ever violated: no grant types and no response types
// mean fosite can never issue it a token via any grant handler this auth
// server registers, and it carries no secret. This guarantee holds on every
// backend, including a bare read-back from Redis by a replica with no SPIFFE
// overlay — see inertPlaceholderClient and storedClient.Reserved. It keeps
// the real client's Scopes, Audience, (RFC 8707) Resources, and SPIFFE
// association identity fingerprint so the fingerprint comparison in
// ReconcileConfiguredClient can distinguish "same association, restarted"
// (idempotent) from "different, colliding association at this ID" (a loud
// failure).
// Scopes and Audience are taken directly from actual.GetScopes() /
// GetAudience() without an extra defensive clone here: the only production
// caller (registration.SPIFFEClient) already returns a fresh slice.Clone
// from both getters (see its doc comments), so there is no live backing
// array to alias. The same applies to Resources() when actual implements
// resourceScopedClient; a client that doesn't (falls back to nil).
func staticClientPlaceholder(actual fosite.Client) fosite.Client {
	var resources []string
	var identityFingerprint string
	if rc, ok := actual.(resourceScopedClient); ok {
		resources = rc.Resources()
	}
	if identity, ok := actual.(spiffeIdentityClient); ok {
		identityFingerprint = identity.IdentityFingerprint()
	}
	return inertPlaceholderClient{
		DefaultClient: &fosite.DefaultClient{
			ID:       actual.GetID(),
			Scopes:   actual.GetScopes(),
			Audience: actual.GetAudience(),
			Public:   false,
		},
		resources:           resources,
		identityFingerprint: identityFingerprint,
	}
}

// ConsumeAssertionJWT delegates assertion replay consumption to the wrapped
// storage, one level down. Storage intentionally does not include this narrow
// capability, so this decorator fails closed when its wrapped storage does
// not provide it, rather than silently skipping past this layer via Unwrap.
func (d *SPIFFEStorageDecorator) ConsumeAssertionJWT(
	ctx context.Context, purpose, issuer, jti string, exp time.Time,
) error {
	consumer, ok := d.Storage.(AssertionJWTConsumer)
	if !ok {
		return fmt.Errorf("wrapped storage %T does not support assertion JWT replay consumption", d.Storage)
	}
	return consumer.ConsumeAssertionJWT(ctx, purpose, issuer, jti, exp)
}

// GetClient returns a configured SPIFFE client before consulting the dynamic backend.
func (d *SPIFFEStorageDecorator) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	if client, ok := d.clients[id]; ok {
		return client, nil
	}
	return d.Storage.GetClient(ctx, id)
}

// RegisterClient rejects reserved static SPIFFE IDs and delegates other registrations.
func (d *SPIFFEStorageDecorator) RegisterClient(ctx context.Context, client fosite.Client) error {
	if client == nil {
		return fmt.Errorf("register client: client is required")
	}
	if _, reserved := d.clients[client.GetID()]; reserved {
		return fmt.Errorf("%w: client ID %q is reserved for a static SPIFFE client", ErrAlreadyExists, client.GetID())
	}
	return d.Storage.RegisterClient(ctx, client)
}

// ReconcileConfiguredClient rejects reserved static SPIFFE IDs and delegates
// other configured-client reconciliation to the base backend.
func (d *SPIFFEStorageDecorator) ReconcileConfiguredClient(ctx context.Context, client fosite.Client) error {
	if client == nil {
		return fmt.Errorf("reconcile configured client: client is required")
	}
	if _, reserved := d.clients[client.GetID()]; reserved {
		return fmt.Errorf("%w: client ID %q is reserved for a static SPIFFE client", ErrAlreadyExists, client.GetID())
	}
	return d.Storage.ReconcileConfiguredClient(ctx, client)
}

// UpsertDCRIssuedClient rejects reserved static SPIFFE IDs and delegates
// other DCR-issued upsert-on-fetch calls (e.g. CIMDStorageDecorator) to the
// base backend. Without this override the guard would be skipped whenever
// the durable SPIFFE placeholder row is absent (reconciliation hasn't run
// yet, or persistence failed), letting a DCR row get created at a reserved
// SPIFFE ID.
func (d *SPIFFEStorageDecorator) UpsertDCRIssuedClient(ctx context.Context, client fosite.Client) error {
	if client == nil {
		return fmt.Errorf("upsert DCR-issued client: client is required")
	}
	if _, reserved := d.clients[client.GetID()]; reserved {
		return fmt.Errorf("%w: client ID %q is reserved for a static SPIFFE client", ErrAlreadyExists, client.GetID())
	}
	return d.Storage.UpsertDCRIssuedClient(ctx, client)
}

// Unwrap returns the dynamic DCR/CIMD storage backend.
func (d *SPIFFEStorageDecorator) Unwrap() Storage { return d.Storage }
