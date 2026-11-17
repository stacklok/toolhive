// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package subjectresolver implements subject.Resolver for the standalone
// authorization server, using the provider-identity rows it keeps for users.
//
// Provider-identity rows are written only on the identity-provider sign-in
// leg. A subject from a later leg (for example GitHub) therefore never
// resolves. The resolver knows only found or not found: it has no notion of
// deactivated users and never returns subject.ErrAmbiguous.
//
// A provider-identity row alone does not prove the user exists. Deleting a user
// in the Redis storage removes the user record first and cleans up the provider
// links on a best-effort basis, so a stale link can outlive its user. The
// resolver therefore also loads the user record, as the sign-in path does.
package subjectresolver

import (
	"context"
	"errors"
	"fmt"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/subject"
)

// IdentityStore is the part of storage.UserStorage the resolver reads.
type IdentityStore interface {
	GetUser(ctx context.Context, id string) (*storage.User, error)
	GetProviderIdentity(ctx context.Context, providerID, providerSubject string) (*storage.ProviderIdentity, error)
}

// Provider names an identity provider and the claim that carries its subject.
//
// Mapping an issuer to a Provider asserts that the values of that issuer's
// SubjectClaim are the subjects Name's upstream stores for its users. Rows are
// written only by that upstream's own sign-in and are keyed by upstream name
// and subject, so nothing mixes unless an operator maps an issuer to it. A
// wrong mapping resolves to the wrong user.
type Provider struct {
	// Name is the provider ID under which the provider's identities are stored.
	Name string
	// SubjectClaim is the claim whose value is the provider's subject, such as "sub".
	SubjectClaim string
}

// Resolver resolves a subject.Ref to a local user ID.
type Resolver struct {
	providers map[string]Provider
	store     IdentityStore
}

var _ subject.Resolver = (*Resolver)(nil)

// New returns a Resolver for the given identity providers, keyed by issuer.
// Each entry asserts what Provider documents: that the issuer's SubjectClaim
// values are the subjects the named upstream stores. New accepts several
// issuers for the same provider name with different subject claims (an Okta
// custom authorization server carries the sign-in subject in uid), but nothing
// configures that yet; it belongs to the third-party issuers work.
// It returns an error if the store is nil, no providers are given, an entry has
// an empty issuer, name or subject claim, or subject.ValidateBindingClaim
// refuses an entry's claim (the error wraps subject.ErrInvalidBindingClaim).
// The warning ValidateBindingClaim can return, for sub on an Okta custom
// authorization server, is ignored here; callers who want it call
// ValidateBindingClaim themselves.
func New(providers map[string]Provider, store IdentityStore) (*Resolver, error) {
	if store == nil {
		return nil, errors.New("subjectresolver: store is required")
	}
	if len(providers) == 0 {
		return nil, errors.New("subjectresolver: at least one provider is required")
	}
	copied := make(map[string]Provider, len(providers))
	for issuer, p := range providers {
		if issuer == "" || p.Name == "" || p.SubjectClaim == "" {
			return nil, errors.New("subjectresolver: provider entries need an issuer, a name and a subject claim")
		}
		if _, err := subject.ValidateBindingClaim(issuer, p.SubjectClaim); err != nil {
			return nil, fmt.Errorf("subjectresolver: provider %q: %w", p.Name, err)
		}
		copied[issuer] = p
	}
	return &Resolver{providers: copied, store: store}, nil
}

// Resolve returns the local user ID for ref. Callers check Ref.Claim against
// their own configuration first so they can return a precise error. A claim
// that is not the provider's subject claim is ErrUnknown here by design, so a
// value from another claim is never looked up as a stored subject.
//
// It returns subject.ErrUnknown when the issuer or claim is not configured, no
// provider identity matches, or the identity points at a user that no longer
// exists, and subject.ErrUnavailable when the store cannot answer.
func (r *Resolver) Resolve(ctx context.Context, ref subject.Ref) (string, error) {
	p, ok := r.providers[ref.Issuer]
	if !ok || ref.Claim != p.SubjectClaim {
		return "", subject.ErrUnknown
	}
	identity, err := r.store.GetProviderIdentity(ctx, p.Name, ref.Value)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", subject.ErrUnknown
		}
		return "", fmt.Errorf("%w: %w", subject.ErrUnavailable, err)
	}
	if identity == nil || identity.UserID == "" {
		return "", subject.ErrUnknown
	}
	user, err := r.store.GetUser(ctx, identity.UserID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", subject.ErrUnknown
		}
		return "", fmt.Errorf("%w: %w", subject.ErrUnavailable, err)
	}
	if user == nil || user.ID == "" {
		return "", subject.ErrUnknown
	}
	return user.ID, nil
}
