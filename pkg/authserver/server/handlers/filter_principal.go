// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import "github.com/stacklok/toolhive/pkg/authserver/upstreamfilter"

// FilterPrincipal is a compatibility alias for upstreamfilter.FilterPrincipal.
// See that interface for the identity and read-only claims contract.
type FilterPrincipal = upstreamfilter.FilterPrincipal

// filterPrincipal contains only the credential-free values exposed to an
// upstream filter.
type filterPrincipal struct {
	subject         string
	canonicalUserID string
	name            string
	email           string
	claims          map[string]any
}

func (p filterPrincipal) GetSubject() string         { return p.subject }
func (p filterPrincipal) GetCanonicalUserID() string { return p.canonicalUserID }
func (p filterPrincipal) GetName() string            { return p.name }
func (p filterPrincipal) GetEmail() string           { return p.email }
func (p filterPrincipal) GetClaims() map[string]any  { return p.claims }

var _ FilterPrincipal = filterPrincipal{}
