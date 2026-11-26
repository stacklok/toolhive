// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// ClientAdmissionStorage hides a stored client that ToolHive wrote from
// configuration but whose configuration no longer declares it, so a configured
// client removed from configuration stops authenticating and stops using its
// grants even though its row is still in the backend.
//
// Registered clients (DCR and CIMD) are always served, and so are unmarked rows
// an embedder wrote itself with RegisterClient, except a confidential row whose
// grant types are exactly [token-exchange] (the legacy delegate shape), which is
// treated as a configured client. See clientClass for how a row is classified.
//
// Among client methods it overrides only GetClient, and no write method
// (ConsumeAssertionJWT and Unwrap only forward): rows are never deleted here,
// which keeps rolling updates safe, since each replica enforces its own
// configuration against the shared store.
//
// It does not revoke access JWTs that were already issued; those stay valid
// until they expire. It does not hide the row from code that reads the backend
// directly, and it does not protect binaries that lack it.
type ClientAdmissionStorage struct {
	Storage
	configured map[string]struct{}
	warned     sync.Map
}

// NewClientAdmissionStorage wraps base. configuredIDs are the IDs this
// process's configuration declares. It fails on a nil base or an empty ID, and
// ignores repeated IDs.
func NewClientAdmissionStorage(base Storage, configuredIDs []string) (*ClientAdmissionStorage, error) {
	if base == nil {
		return nil, errors.New("client admission: base storage is required")
	}
	set := make(map[string]struct{}, len(configuredIDs))
	for _, id := range configuredIDs {
		if id == "" {
			return nil, errors.New("client admission: configured client ID is required")
		}
		set[id] = struct{}{}
	}
	return &ClientAdmissionStorage{Storage: base, configured: set}, nil
}

// GetClient returns the stored client only when it is admitted. Backend errors
// pass through unchanged.
func (s *ClientAdmissionStorage) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	client, err := s.Storage.GetClient(ctx, id)
	if err != nil {
		return client, err
	}
	if client == nil {
		return nil, notFoundRFC6749Error("Client not found")
	}
	class := classifyClient(client)
	clientID := client.GetID()
	if s.admits(clientID, class) {
		return client, nil
	}
	if _, seen := s.warned.LoadOrStore(clientID, struct{}{}); !seen {
		slog.Warn("stored client is not declared by this server's configuration; treating as unknown",
			"client_id", clientID, "class", class.String())
	}
	return nil, notFoundRFC6749Error("Client not found")
}

// ConsumeAssertionJWT forwards to the wrapped backend. Storage does not
// include this capability, so it is not promoted automatically.
func (s *ClientAdmissionStorage) ConsumeAssertionJWT(
	ctx context.Context, purpose, issuer, jti string, exp time.Time,
) error {
	consumer, ok := s.Storage.(AssertionJWTConsumer)
	if !ok {
		return fmt.Errorf("wrapped storage %T does not support assertion JWT replay consumption", s.Storage)
	}
	return consumer.ConsumeAssertionJWT(ctx, purpose, issuer, jti, exp)
}

// Unwrap returns the wrapped storage.
func (s *ClientAdmissionStorage) Unwrap() Storage { return s.Storage }

// ClientStoreBelowCIMD returns the storage to use for a client lookup that must
// bypass the CIMD decorator but still pass client admission: the first
// *ClientAdmissionStorage found walking Unwrap, or the innermost store when the
// chain has none (handler unit tests, or an embedder calling handlers.NewHandler
// directly without admission).
func ClientStoreBelowCIMD(s Storage) Storage {
	for {
		if _, ok := s.(*ClientAdmissionStorage); ok {
			return s
		}
		unwrapper, ok := s.(interface{ Unwrap() Storage })
		if !ok {
			return s
		}
		s = unwrapper.Unwrap()
	}
}

// admits reports whether a stored client with this ID and class is served.
func (s *ClientAdmissionStorage) admits(id string, c clientClass) bool {
	switch c {
	case classRegistered, classUnmarked:
		return true
	case classConfigured:
		_, ok := s.configured[id]
		return ok
	case classPlaceholder:
		return false
	default:
		return false
	}
}

// clientClass records how a stored client row came to exist, as far as the row
// itself can tell.
type clientClass uint8

const (
	// classPlaceholder marks the inert durable placeholder that reserves a
	// static SPIFFE client ID. It is never an authenticatable client.
	classPlaceholder clientClass = iota + 1

	// classConfigured marks a client ToolHive wrote from configuration. It is
	// served only while this process's configuration declares it.
	classConfigured

	// classRegistered marks a client that carries the registration marker
	// (registration.DCRIssued): dynamic client registration and CIMD documents.
	classRegistered

	// classUnmarked marks every other stored client, such as one an embedder
	// writes into storage itself with RegisterClient.
	classUnmarked
)

// String returns a log-friendly name for the class.
func (c clientClass) String() string {
	switch c {
	case classPlaceholder:
		return "placeholder"
	case classConfigured:
		return "configured"
	case classRegistered:
		return "registered"
	case classUnmarked:
		return "unmarked"
	default:
		return "invalid"
	}
}

// classifyClient reports the class of a stored client. It is total and checks
// Placeholder, Configured, Registered, then Unmarked. The classes are disjoint:
// stamped rows come back as the MarkConfigured wrapper, reconcile refuses
// DCR-marked input, and the legacy shape needs a bare DefaultClient.
func classifyClient(client fosite.Client) clientClass {
	switch {
	case isReservedPlaceholder(client):
		return classPlaceholder
	case registration.IsConfigured(client), isLegacyDelegateShape(client):
		return classConfigured
	case registration.DCRIssued(client):
		return classRegistered
	default:
		return classUnmarked
	}
}

// isLegacyDelegateShape matches the delegate row every binary wrote from
// v0.43.0 (#6320) before the configured stamp existed. On Redis a confidential
// token-exchange-only OIDC client with no auth method reads back as a bare
// DefaultClient and so matches too; memory serves it.
func isLegacyDelegateShape(client fosite.Client) bool {
	c, ok := client.(*fosite.DefaultClient)
	return ok && !c.Public && slices.Equal(c.GetGrantTypes(), []string{oauthproto.GrantTypeTokenExchange})
}
