// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package obo

import "errors"

// ErrEnterpriseRequired is returned by every default OBO dispatch point — the
// controllerutil handler hook, the vMCP converter stub, and the middleware
// stub — when no out-of-tree handler/factory has been registered. Callers must
// use errors.Is to compare; the error wraps cleanly through
// fmt.Errorf("...: %w", ...).
//
// Lives in pkg/auth/obo (a leaf package) so that callers in
// cmd/thv-operator/... and pkg/vmcp/... can share the same sentinel without
// either layer importing the other. To register an out-of-tree handler, see
// controllerutil.RegisterOBOHandler (for the operator dispatch points) and
// obo.RegisterFactory (for the proxy middleware factory).
var ErrEnterpriseRequired = errors.New(
	"on-behalf-of (OBO) external auth type requires an enterprise build")
