// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionbinding

import (
	"context"
	"errors"
	"net/http"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/transport/session"
)

// BindOwner sets ownership before insertion, never on a published session.
func BindOwner(ctx context.Context, sess session.Session) error {
	identity, _ := auth.IdentityFromContext(ctx)
	owner, err := FromIdentity(identity)
	if err != nil {
		return err
	}
	sess.SetMetadata(MetadataKey, owner)
	return nil
}

// Store is the ordinary session manager surface required for conditional publication.
type Store interface {
	AddSession(session.Session) error
	LookupOwner(context.Context, string) (string, error)
}

// AddOwnedSession binds an ordinary proxy session before atomic publication.
// sess must be newly constructed and unpublished; its metadata is modified.
// A collision may reuse only the same owner's record, never replace metadata.
// Shared storage does not provide cross-replica live-stream delivery.
func AddOwnedSession(ctx context.Context, manager Store, sess session.Session) error {
	if err := BindOwner(ctx, sess); err != nil {
		return ErrNotFound
	}
	if err := manager.AddSession(sess); err != nil {
		if !errors.Is(err, session.ErrSessionAlreadyExists) {
			return err
		}
		owner, err := manager.LookupOwner(ctx, sess.ID())
		if err != nil {
			return err
		}
		identity, _ := auth.IdentityFromContext(ctx)
		return Validate(owner, identity)
	}
	return nil
}

// WriteOwnershipError uses the ordinary proxies' non-disclosing JSON-RPC 404.
func WriteOwnershipError(w http.ResponseWriter, requestID any, err error) {
	if errors.Is(err, ErrNotFound) {
		session.WriteNotFound(w, requestID)
		return
	}
	http.Error(w, "Session store unavailable", http.StatusServiceUnavailable)
}
