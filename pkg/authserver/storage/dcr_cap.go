// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// ClientCapacityError is the error RegisterClient returns when the DCR client
// cap is reached. It matches ErrClientCapacity under errors.Is.
type ClientCapacityError struct {
	// RetryAfter is how long until the oldest counted client expires, which
	// is the earliest moment a slot can free up. Zero when unknown.
	RetryAfter time.Duration
}

// Error implements error.
func (e *ClientCapacityError) Error() string {
	return fmt.Sprintf("%s: retry after %s", ErrClientCapacity, e.RetryAfter)
}

// Is makes errors.Is(err, ErrClientCapacity) report true.
func (*ClientCapacityError) Is(target error) bool {
	return target == ErrClientCapacity
}

// newClientCapacityError builds a ClientCapacityError from the oldest counted
// client's expiry. A past expiry yields a zero RetryAfter.
func newClientCapacityError(oldestExpiry, now time.Time) *ClientCapacityError {
	return &ClientCapacityError{RetryAfter: max(oldestExpiry.Sub(now), 0)}
}

// DCRClientCounter is implemented by storage backends that can report how many
// live clients count against the DCR cap. It backs the live-client gauge and
// is read at metric collection time, never on a request path. Reach it through
// Unwrap, since the CIMD and SPIFFE decorators do not forward it.
type DCRClientCounter interface {
	CountDCRClients(ctx context.Context) (int64, error)
}

// countsTowardDCRCap reports whether client counts against the DCR client
// cap. That is every client carrying the registration.DCRIssued marker except
// a CIMD client: the CIMD decorator persists its resolved clients with the
// same marker so they expire, but they are not minted by /oauth/register and
// are not counted. Operator-configured clients carry no marker.
func countsTowardDCRCap(client fosite.Client) bool {
	return registration.DCRIssued(client) && !oauthproto.IsClientIDMetadataDocumentURL(client.GetID())
}

// ResolvedMaxDCRClients returns the DCR client cap c configures: unset (or a
// nil c) means DefaultMaxDCRClients, zero means unlimited, and a negative
// value is an error.
func (c *RunConfig) ResolvedMaxDCRClients() (int, error) {
	if c == nil || c.MaxDCRClients == nil {
		return DefaultMaxDCRClients, nil
	}
	if *c.MaxDCRClients < 0 {
		return 0, fmt.Errorf("max_dcr_clients must not be negative, got %d", *c.MaxDCRClients)
	}
	return *c.MaxDCRClients, nil
}
