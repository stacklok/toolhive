// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// Decorator wraps a MultiSession with additional behavior.
// It is called after session creation and must return the (possibly decorated) session.
// On error the caller closes the current session (which may already be wrapped by
// earlier decorators); the decorator must not close it.
type Decorator func(ctx context.Context, sess MultiSession) (MultiSession, error)

// NewDecoratingFactory wraps base, applying decorators in order after each MakeSessionWithID.
// If no decorators are provided, base is returned unchanged.
func NewDecoratingFactory(base MultiSessionFactory, decorators ...Decorator) MultiSessionFactory {
	if len(decorators) == 0 {
		return base
	}
	return &decoratingMultiSessionFactory{base: base, decorators: decorators}
}

type decoratingMultiSessionFactory struct {
	base       MultiSessionFactory
	decorators []Decorator
}

// RestoreSession delegates to the base factory and re-applies all decorators,
// just as MakeSessionWithID does for new sessions.
func (f *decoratingMultiSessionFactory) RestoreSession(
	ctx context.Context,
	id string,
	storedMetadata map[string]string,
	allBackends []*vmcp.Backend,
) (MultiSession, error) {
	sess, err := f.base.RestoreSession(ctx, id, storedMetadata, allBackends)
	if err != nil {
		return nil, err
	}
	for _, dec := range f.decorators {
		var decorated MultiSession
		decorated, err = dec(ctx, sess)
		if err != nil {
			if closeErr := sess.Close(); closeErr != nil {
				slog.Warn("failed to close session after decorator error on restore", "error", closeErr)
			}
			return nil, err
		}
		if decorated == nil {
			if closeErr := sess.Close(); closeErr != nil {
				slog.Warn("failed to close session after decorator returned nil on restore", "error", closeErr)
			}
			return nil, fmt.Errorf("decorator returned nil session without error on restore")
		}
		sess = decorated
	}
	return sess, nil
}

func (f *decoratingMultiSessionFactory) MakeSessionWithID(
	ctx context.Context,
	id string,
	identity *auth.Identity,
	allowAnonymous bool,
	backends []*vmcp.Backend,
) (MultiSession, error) {
	sess, err := f.base.MakeSessionWithID(ctx, id, identity, allowAnonymous, backends)
	if err != nil {
		return nil, err
	}
	for _, dec := range f.decorators {
		var decorated MultiSession
		decorated, err = dec(ctx, sess)
		if err != nil {
			if closeErr := sess.Close(); closeErr != nil {
				slog.Warn("failed to close session after decorator error", "error", closeErr)
			}
			return nil, err
		}
		if decorated == nil {
			if closeErr := sess.Close(); closeErr != nil {
				slog.Warn("failed to close session after decorator returned nil", "error", closeErr)
			}
			return nil, fmt.Errorf("decorator returned nil session without error")
		}
		sess = decorated
	}
	return sess, nil
}
